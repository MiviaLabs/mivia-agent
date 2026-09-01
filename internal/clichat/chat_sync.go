package clichat

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// syncNoticeWriter is where the plain-CLI surface reports sync status.
//
// stderr, not stdout, and for the same reason the REPL banner uses it: stdout
// is the answer channel that `-p` and `--json` callers parse, and a status
// line interleaved into it would corrupt a machine-readable stream. It is a
// variable so a test can read what a real attach actually wrote.
var syncNoticeWriter io.Writer = os.Stderr

// cliSyncOptions builds the SessionOptions the plain-CLI chat surface hands to
// chatsync.OpenSession. It is a separate function so a test can drive the
// exact value production uses, instead of asserting on a hand-built copy that
// can drift away from the wiring it claims to cover.
//
// wsRoot, not sess.SessionDir: that field belongs to the legacy file-backed
// session store and is unconditionally nulled the moment context state is
// enabled (internal/chat/context_integration.go's SetContextManager), which
// happens on every real `mivia chat` invocation - reading it here made
// chat-sync's identity permanently ephemeral in production, so every resume
// forked a brand-new remote session. See chatSyncAnchor for how wsRoot itself
// gets resolved into a real storage directory.
func cliSyncOptions(sess *chat.Session, wsRoot string, res *config.Resolved, tokens chatsync.TokenProvider) chatsync.SessionOptions {
	// The sync identity is resolved HERE, before the options exist, because
	// chatsync.OpenSession opens the outbox before it attaches: OutboxDir must
	// already carry the local handle. A load error still yields a usable
	// identity - this run syncs under a handle the next run will not find,
	// which is strictly better than not syncing.
	anchor := chatSyncAnchor(wsRoot)
	identityDir := chatsync.IdentityDir(anchor)
	key := chatsync.IdentityKey(sess.SessionID)
	ident, _ := chatsync.LoadOrCreateIdentity(identityDir, key)

	return chatsync.SessionOptions{
		TokenProvider: tokens,
		ClientOptions: chatsync.ClientOptions{
			BaseURL: chatsync.DefaultBaseURL(res.Sync.APIURL),
		},
		ProjectorOptions: chatsync.ProjectorOptions{
			IncludeToolIO:   res.Sync.IncludeToolIO,
			IncludeThinking: res.Sync.IncludeThinking,
			StreamAssistant: res.Sync.StreamAssistant,
			// chatsync is a leaf (settled decision 7), so these two are the
			// host's to supply. Both zero values are wrong rather than merely
			// absent: a nil ErrorMessage drops back to a default that does not
			// know this app's sentinels, and a false RedactToolArgs reads as
			// "the operator did not ask for redaction" when they may have.
			ErrorMessage: chat.TurnErrorMessage,
			// From the PERSISTED identity, never a per-run random: attach
			// compares this against the writer id on events the server holds
			// past our cursor, so a value that changed every run would read
			// our own previous run as foreign, end the remote session and
			// fork (REVIEW CHANGE 8's permanent data loss).
			WriterID:       ident.WriterID,
			RedactToolArgs: tools.RedactToolArgs(),
		},
		OutboxDir:       chatsync.OutboxDirFor(anchor, ident.LocalHandle),
		LocalHandle:     ident.LocalHandle,
		RemoteSessionID: ident.RemoteSessionID,
		Identity:        chatsync.IdentityRef{Dir: identityDir, Key: key},
		MaxUnflushed:    res.Sync.MaxUnflushed,
		PollWaitSeconds: res.Sync.PollWaitSeconds,
		HeartbeatPeriod: config.SaturatingSeconds(res.Sync.HeartbeatSeconds),
		CreateTitle:     "CLI Session",
		// Remote input (chat-sync "steering") is TUI-only, deliberately.
		// internal/uiadapter/session_pool.go's poolSyncOptions enables it
		// there because internal/ui/screen/conversation.Screen has an
		// explicit turn-ownership seam - awaitSessionEvent - built for
		// exactly this: starting a turn from something other than the
		// composer's Enter key and draining its events without blocking the
		// next local action. The plain/line CLI (`mivia chat`) has no
		// equivalent: it runs one synchronous request/response loop with no
		// standing screen to arm a background drain from, so a remote input
		// arriving mid-request has nothing safe to hand it to. Revisit this
		// if `mivia chat` ever grows an event loop of its own; until then,
		// leave this false rather than half-wire a path with no consumer.
		EnablePolling: false,
	}
}

// attachCLISync starts chat sync for the plain-CLI surface and returns the
// detach closure.
//
// Activation is authentication: a logged-in user syncs, a logged-out one does
// not, and neither state needs a flag or a prompt. `enabled = false` is the
// only way to say no while logged in - see config.ResolvedSync.Active. Every
// refusal here is silent by design: sync failing is never a reason to break
// the local chat the user actually asked for.
func attachCLISync(sess *chat.Session, wsRoot string, res *config.Resolved) func() {
	if res == nil || sess == nil || sess.EventBus == nil {
		return func() {}
	}
	tokens := chatsync.DefaultTokenProvider()
	if !res.Sync.Active(tokens != nil) {
		return func() {}
	}
	opts := cliSyncOptions(sess, wsRoot, res, tokens)
	// "Stop syncing and SAY SO": without this the terminal stop is recorded
	// where only a getter can reach it, and no host polls SyncSession, so a
	// dead uploader looks exactly like a healthy idle one.
	opts.OnStop = func(reason string) {
		_, _ = fmt.Fprintf(syncNoticeWriter, "mivia: chat sync stopped: %s\n", reason)
	}
	syncSess, err := chatsync.OpenSession(context.Background(), sess.EventBus, sess.SessionID, opts)
	if err != nil {
		return func() {}
	}
	_, _ = fmt.Fprintln(syncNoticeWriter, "mivia: chat sync is running")
	return func() {
		// Flush BEFORE Stop, not after: Publish() only enqueues onto this
		// subscription's own bounded queue (internal/events.Bus, default 256)
		// and returns immediately - it does not wait for chatsync's
		// HandleEvent to actually run. A one-shot turn that just finished
		// (oneShot returns as soon as the model/tool loop is done, then this
		// closure runs on the very next line via defer) can still have a
		// backlog of already-published events sitting in that queue, not yet
		// delivered. Stop()'s drainAndFlushFinal only drains what has ALREADY
		// reached SyncSession's own eventCh, so without this Flush a
		// heavy-volume turn ([sync].stream_assistant = true multiplies event
		// count 5-10x) can race Stop() and silently lose its tail - the final
		// assistant.message, turn.ended, and any tool.ended pairs still in
		// flight - because the process exits right after Stop returns.
		// Bus.Flush blocks until every event published before this call has
		// been delivered to HandleEvent, which closes that gap deterministically.
		sess.EventBus.Flush()
		ctx, cancel := context.WithTimeout(context.Background(), chatsync.RecommendedStopTimeout)
		defer cancel()
		_ = syncSess.Stop(ctx)
	}
}

// chatSyncAnchor resolves wsRoot into the directory chat-sync keeps its
// identity/outbox files under - wsRoot's .mivia/ namespace, not the bare
// workspace root, so chat-sync's durable state (and, in the outbox, real
// conversation transcript content queued for upload) does not scatter into
// the project tree the user actually works in. Mirrors
// internal/uiadapter/session_pool.go's chatSyncAnchor - kept as two small
// copies rather than a shared helper so neither host package grows a
// dependency the other doesn't need for this alone.
//
// The empty check happens on wsRoot BEFORE NamespacePath, not after:
// workspace.NamespacePath("") returns the RELATIVE ".mivia" (its own doc
// comment says so - correct for its other callers, wrong here), so
// IdentityDir/OutboxDirFor's own empty-storeDir guards would never see an
// empty string and would happily write under cwd's ".mivia" instead of
// refusing - the same class of leak this anchoring exists to close.
func chatSyncAnchor(wsRoot string) string {
	if wsRoot == "" {
		return ""
	}
	return workspace.NamespacePath(wsRoot)
}
