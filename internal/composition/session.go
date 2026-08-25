package composition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// SessionInput carries every value BuildSession needs to construct a working
// chat.Session: the resolved config and completer chat.NewSession itself
// takes, the registry and dispatcher inputs BuildRegistry/BuildDispatcher
// already accept, and the checkpoint store BuildSession opens and wires as
// the session's context store.
type SessionInput struct {
	// Config is the resolved workspace configuration. Required:
	// chat.NewSession reads it for the model, prompt, and token budgets.
	Config *config.Resolved
	// Completer is the provider completer the session's initial binding
	// uses. May be nil (chat.NewSession accepts a nil completer for
	// construction), but a nil completer cannot run a turn.
	Completer provider.Completer

	// Registry configures the tool registry BuildRegistry builds. Its
	// Workspace field may be nil, which yields a registry with no
	// filesystem tools.
	Registry RegistryInput
	// PrebuiltRegistry, when non-nil, skips BuildRegistry(in.Registry)
	// entirely and assigns sess.Tools = in.PrebuiltRegistry directly. Use
	// this when the caller has already merged additional tools (e.g. MCP
	// wrappers via AttachMCPServers) into a registry it owns. The dispatcher's
	// view matches regardless of the path taken: dispatcher.Registry is set
	// to whatever sess.Tools ends up as.
	PrebuiltRegistry *tools.Registry
	// Dispatcher configures the dispatcher BuildDispatcher builds.
	// Dispatcher.Registry is overwritten with the registry BuildSession just
	// built, so the caller does not need to set it.
	Dispatcher DispatcherInput

	// EventBus is the session's event bus. Nil mints a fresh events.New().
	EventBus *events.Bus

	// StorePath is the SQLite file BuildSession opens for the session's
	// checkpoint store. Required: chat.Session has no durable checkpoint
	// history without one.
	StorePath string
	// WorkspaceID identifies the checkpoint principal's workspace scope.
	// Ignored when Principal is already bound.
	WorkspaceID string
	// SubjectID identifies the checkpoint principal's subject scope. Empty
	// defaults to the session's own SessionID. Ignored when Principal is
	// already bound.
	SubjectID string
	// Principal, when bound (IsBound() true), is used as-is instead of
	// minting a fresh one from WorkspaceID/SubjectID. A caller that needs to
	// read its own checkpoint back later (contextstate.Principal's capability
	// is random per mint - see contextstate.NewPrincipal - and store.Load
	// rejects a principal whose capability does not match what was written)
	// must supply the same Principal value it will read with.
	Principal contextstate.Principal
}

// BuildSession wires a chat.Session end to end: the completer chat.NewSession
// takes directly, the tool registry (BuildRegistry), the dispatcher
// (BuildDispatcher), the event bus, and a SQLite-backed checkpoint store. It
// returns the checkpoint principal actually installed (in.Principal as-is,
// or a freshly minted one), so a caller can read its own checkpoint back
// through the returned store. The caller owns the store's Close; the session
// holds no reference that outlives it.
func BuildSession(in SessionInput) (*chat.Session, *storage.SQLite, contextstate.Principal, error) {
	if in.Config == nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: config is required")
	}
	sess := chat.NewSession(in.Config, in.Completer)
	sess.UseTools = true

	var registry *tools.Registry
	var err error
	if in.PrebuiltRegistry != nil {
		registry = in.PrebuiltRegistry
	} else {
		registry, err = BuildRegistry(in.Registry)
		if err != nil {
			return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: %w", err)
		}
	}
	sess.Tools = registry

	dispatcherInput := in.Dispatcher
	dispatcherInput.Registry = registry
	dispatcher, err := BuildDispatcher(dispatcherInput)
	if err != nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: %w", err)
	}
	sess.SetDispatcher(dispatcher)

	bus := in.EventBus
	if bus == nil {
		bus = events.New()
	}
	sess.EventBus = bus

	store, principal, err := buildSessionCheckpointStore(sess, in)
	if err != nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: %w", err)
	}
	return sess, store, principal, nil
}

// buildSessionCheckpointStore opens in.StorePath and installs it as sess's
// context store and checkpoint publisher, the same
// StructuralPreparationManager/PreparationCommitter pairing
// internal/cli/context_setup_session.go's enableSessionContext installs (no
// LLM summarizer: BuildSession stays structural-only, matching an
// unconfigured [context.summary] workspace).
func buildSessionCheckpointStore(sess *chat.Session, in SessionInput) (*storage.SQLite, contextstate.Principal, error) {
	if in.StorePath == "" {
		return nil, contextstate.Principal{}, fmt.Errorf("store path is required")
	}
	store, err := storage.OpenSQLite(in.StorePath)
	if err != nil {
		return nil, contextstate.Principal{}, fmt.Errorf("open checkpoint store: %w", err)
	}
	principal := in.Principal
	if !principal.IsBound() {
		subjectID := in.SubjectID
		if subjectID == "" {
			subjectID = sess.SessionID
		}
		principal, err = contextstate.NewPrincipal(in.WorkspaceID, sess.SessionID, subjectID)
		if err != nil {
			_ = store.Close()
			return nil, contextstate.Principal{}, fmt.Errorf("mint checkpoint principal: %w", err)
		}
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
		UsageWriter:         storage.NewUsageWriter(store, principal.WorkspaceID),
	}
	policy := contextstate.PolicySnapshot{}
	if summarizer, snapshot, ok := buildSessionSummarizer(sess, in); ok {
		manager.Summarizer = summarizer
		policy = snapshot
	}
	if err := sess.SetContextManager(manager, principal, policy); err != nil {
		_ = store.Close()
		return nil, contextstate.Principal{}, fmt.Errorf("set context manager: %w", err)
	}
	sess.SetContextRedactionPolicy(contextRedactionPolicy(in.Config))
	if err := sess.SetContextStore(store); err != nil {
		_ = store.Close()
		return nil, contextstate.Principal{}, fmt.Errorf("set context store: %w", err)
	}
	// Prime the token-estimate correction from what this workspace already
	// measured for this binding, so the first request is not planned blind.
	// Best effort by contract: a miss leaves the session uncorrected.
	sess.SeedCalibration(context.Background(), store, principal.WorkspaceID)
	return store, principal, nil
}

func buildSessionSummarizer(sess *chat.Session, in SessionInput) (*contextmgr.Summarizer, contextstate.PolicySnapshot, bool) {
	if sess == nil || in.Config == nil || !in.Config.Context.Summary.SummaryEnabled() {
		return nil, contextstate.PolicySnapshot{}, false
	}
	endpoint := strings.TrimSpace(in.Config.BaseURL)
	if endpoint == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	completer := in.Completer
	if completer == nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	providerName := in.Config.ProviderName
	model := in.Config.Model
	if providerName == "" || model == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	redaction := contextRedactionPolicy(in.Config)
	policy := contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: redaction.Configured, NetworkEnabled: true,
		Provider: providerName, Model: model,
		CredentialScope:   "env-api-key",
		EndpointAllowlist: []string{endpoint},
		PolicyDigest: summaryPolicyDigest(providerName, model, endpoint,
			in.Config.Privacy.RedactionPatterns, in.Config.Privacy.RedactionKeyNames),
	}
	adapter, err := contextmgr.NewLLMSummaryProvider(completer, sess.SessionID)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	generation := uint64(1)
	if binding := sess.CurrentBinding(); binding.ModelGeneration > 0 {
		generation = binding.ModelGeneration
	}
	summarizer, err := contextmgr.NewSummarizer(adapter, contextstate.BindingRevision{
		Provider: providerName, Model: model, Generation: generation,
	}, policy)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	return &summarizer, policy, true
}

func contextRedactionPolicy(res *config.Resolved) contextstate.RedactionPolicy {
	if res == nil || res.RedactionPolicy == nil {
		return contextstate.RedactionPolicy{}
	}
	patterns := res.Privacy.RedactionPatterns
	keyNames := res.Privacy.RedactionKeyNames
	if len(patterns) == 0 && len(keyNames) == 0 {
		return contextstate.RedactionPolicy{}
	}
	policy := res.RedactionPolicy
	return contextstate.RedactionPolicy{
		Configured: true, Patterns: patterns, KeyNames: keyNames,
		Redactor: func(data []byte) []byte { return []byte(policy.Text(string(data))) },
	}
}

func summaryPolicyDigest(providerName, model, endpoint string, patterns, keyNames []string) string {
	parts := make([]string, 0, 5)
	parts = append(parts, providerName, model, endpoint,
		strings.Join(patterns, "\x00"), strings.Join(keyNames, "\x00"))
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(strconv.Itoa(len(part))))
		digest.Write([]byte(":"))
		digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}
