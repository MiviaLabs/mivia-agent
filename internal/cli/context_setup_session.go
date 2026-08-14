package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

var setContextManagerForSetup = func(session *chat.Session, manager *contextmgr.ContextManager, principal contextstate.Principal, policies ...contextstate.PolicySnapshot) error {
	return session.SetContextManager(manager, principal, policies...)
}

func openRepositoryContextStore(root string) (*storage.SQLite, error) {
	path, err := repositorySessionStorePath(root, chatInvocation{}, &config.Resolved{})
	if err != nil {
		return nil, err
	}
	return openContextStorePath(path)
}

func setupSessionContext(sess *chat.Session, root string, res *config.Resolved) (*storage.SQLite, error) {
	store, err := openContextStore(root, res.Subagents)
	if err != nil {
		return nil, err
	}
	return configureSessionContext(sess, root, store, res)
}

// setupRepositorySessionContext stores sessions under the main repository.
// The active workspace supplies each session's directory metadata.
func setupRepositorySessionContext(sess *chat.Session, repositoryRoot, storePath string, res *config.Resolved) (*storage.SQLite, error) {
	store, err := openContextStorePath(storePath)
	if err != nil {
		return nil, err
	}
	return configureSessionContext(sess, repositoryRoot, store, res)
}

func configureSessionContext(sess *chat.Session, catalogRoot string, store *storage.SQLite, res *config.Resolved) (*storage.SQLite, error) {
	if err := enableSessionContext(sess, catalogRoot, store, res); err != nil {
		_ = store.Close()
		return nil, err
	}
	sess.SetContextRedactionPolicy(contextRedactionPolicy(res))
	return store, nil
}

// contextRedactionPolicy carries the workspace's [privacy] rules to the durable
// source projector, which had no caller installing them at all - so a
// configured policy classified tool previews and event bodies while context
// payloads went unclassified.
//
// The redactor is the SAME compiled policy the rest of the process uses, passed
// as a function rather than re-implemented, because four hand-rolled pattern
// lists drifting apart is the failure this repo already paid for once. An
// unconfigured workspace yields the zero policy, which stores metadata only.
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

// contextWorkspaceID is a workspace's durable identity, derived from the
// directory itself rather than from how its path was spelled.
//
// It used to hash the cleaned root as given, and `mivia chat` passes "." when
// no --workspace is set, so every project on the machine resolved to the hash
// of "." - one identity shared by all of them. That is the label the durable
// context owns state by, and `chat_sessions` is keyed on it, so two projects
// pointed at one store would have addressed each other's saved sessions.
//
// A path that cannot be resolved falls back to the cleaned input: an id that is
// merely too coarse is better than refusing to start.
func contextWorkspaceID(root string) string {
	resolved, err := filepath.Abs(root)
	if err != nil {
		resolved = filepath.Clean(root)
	}
	if linked, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = linked
	}
	digest := sha256.Sum256([]byte(resolved))
	return "workspace-" + hex.EncodeToString(digest[:8])
}

func enableSessionContext(sess *chat.Session, root string, store *storage.SQLite, res *config.Resolved) error {
	if sess == nil || store == nil {
		return fmt.Errorf("context session and store are required")
	}
	principal, err := contextstate.NewPrincipal(contextWorkspaceID(root), sess.SessionID, "local-user")
	if err != nil {
		return err
	}
	publisher := contextmgr.PreparationCommitter{Store: store}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: publisher,
		Enabled:             true,
	}
	// The summary gate is explicit: the [context.summary] flag, a configured
	// [privacy] policy, and a resolved provider endpoint together wire the LLM
	// summarizer into the request path (manager.Summarizer, read per turn by
	// the agent loop and plain chat). Anything less keeps every path
	// structural-only. The committer's summary seam stays unwired on purpose:
	// CommitPreparation fails the turn when the summary call fails, and a
	// background metadata call must never destroy a turn the model already
	// finished. The injection path degrades on every summary failure.
	policy := contextstate.PolicySnapshot{}
	if summarizer, snapshot, ok := summaryWiring(sess, res); ok {
		manager.Summarizer = summarizer
		policy = snapshot
	}
	if err := setContextManagerForSetup(sess, manager, principal, policy); err != nil {
		return err
	}
	return sess.SetContextStore(store)
}

type contextDispatcherWiring struct {
	preparation      contextmgr.PreparationManager
	preparationInput contextmgr.PrepareInput
	sharedSQLite     *storage.SQLite
}

func contextDispatcherFor(sess *chat.Session, _ config.SubagentConfig) contextDispatcherWiring {
	manager, input, ok := sess.ContextPreparation()
	if !ok {
		return contextDispatcherWiring{}
	}
	wiring := contextDispatcherWiring{preparation: manager, preparationInput: input}
	wiring.sharedSQLite, _ = sess.ContextStore().(*storage.SQLite)
	return wiring
}
