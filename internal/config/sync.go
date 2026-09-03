package config

import (
	"fmt"
	"strings"
)

// SyncConfig is the `[sync]` table exactly as a mivia.toml holds it.
//
// Sync is not something a user turns on. It runs whenever the CLI has a
// login, because a logged-in user asked for the remote product; `enabled =
// false` exists only so a user who wants a local-only session can say so.
type SyncConfig struct {
	// Enabled is a THREE-STATE opt-out switch: nil (key absent), true, or
	// false. The nil state is load-bearing and must never be collapsed into
	// false. "The user did not mention sync" means sync runs; "the user
	// wrote enabled = false" means it does not, and a plain bool cannot tell
	// those two apart, which would make the opt-out impossible to express.
	Enabled *bool `toml:"enabled"`
	// IncludeToolIO and IncludeThinking are THREE-STATE opt-out switches, for
	// the same reason as Enabled and StreamAssistant: nil (key absent), true,
	// or false, where absent means ON.
	//
	// Both were plain bools, so absent read as OFF and the only reachable
	// state was off. That is the wrong default for the same reason
	// StreamAssistant's was: the remote viewer is a LIVE VIEW of the session,
	// and a viewer that silently omits what the agent reasoned - or what its
	// tools read and wrote - is not showing the session. `include_thinking`
	// defaulting off meant every user who had not heard of the key saw a
	// transcript with no reasoning in it and no indication any was withheld,
	// which is exactly how it was reported.
	//
	// The rule is now uniform: if remote sync is on, the session streams in
	// full unless the user says otherwise. Opting out stays one explicit
	// `false` away, and a plain bool could not express that at all.
	IncludeToolIO   *bool `toml:"include_tool_io"`
	IncludeThinking *bool `toml:"include_thinking"`
	// StreamAssistant is a THREE-STATE opt-out switch, like Enabled: nil (key
	// absent), true, or false. Absent means ON.
	//
	// It was off by default, on the reasoning that a durable log wants one
	// settled message per turn rather than the partial states a live view
	// needs. That reasoning weighed the wrong thing: the remote viewer IS a
	// live view, and with streaming off it showed nothing at all until a turn
	// finished, then the whole answer at once. The settled message still
	// ships either way - INV-1 keeps the aggregate, carrying the full text
	// when nothing streamed - so a reader of the durable log loses nothing by
	// this default.
	//
	// A plain bool cannot express the opt-out: absent and "false" would look
	// identical, and the only reachable state would be off.
	StreamAssistant  *bool  `toml:"stream_assistant"`
	APIURL           string `toml:"api_url"`
	PollWaitSeconds  int    `toml:"poll_wait_seconds"`
	HeartbeatSeconds int    `toml:"heartbeat_seconds"`
	MaxUnflushed     int    `toml:"max_unflushed"`
}

// ResolvedSync is the resolved `[sync]` configuration. It is a separate type
// from SyncConfig on purpose: the file form has to carry "unset", and every
// consumer downstream wants a settled answer instead of a pointer it could
// forget to nil-check.
type ResolvedSync struct {
	// Disabled records an explicit `enabled = false`. It is the opt-out, not
	// the activation switch: see Active.
	Disabled bool

	IncludeToolIO    bool
	IncludeThinking  bool
	StreamAssistant  bool
	APIURL           string
	PollWaitSeconds  int
	HeartbeatSeconds int
	MaxUnflushed     int
}

// Active reports whether chat sync must run for this configuration.
// loggedIn is whether a local CLI session exists to authenticate with.
//
// Authentication is the activation rule. A logged-out CLI never uploads
// anything, which is the fail-closed half; a logged-in one syncs unless the
// user opted out, which is the "if I am logged in and using remote it must
// just work" half.
func (s ResolvedSync) Active(loggedIn bool) bool {
	return loggedIn && !s.Disabled
}

func resolveSyncConfig(cfg SyncConfig) ResolvedSync {
	out := ResolvedSync{
		Disabled: cfg.Enabled != nil && !*cfg.Enabled,
		// Absent means on for all three. Only an explicit false turns one off.
		IncludeToolIO:    cfg.IncludeToolIO == nil || *cfg.IncludeToolIO,
		IncludeThinking:  cfg.IncludeThinking == nil || *cfg.IncludeThinking,
		StreamAssistant:  cfg.StreamAssistant == nil || *cfg.StreamAssistant,
		APIURL:           cfg.APIURL,
		PollWaitSeconds:  cfg.PollWaitSeconds,
		HeartbeatSeconds: cfg.HeartbeatSeconds,
		MaxUnflushed:     cfg.MaxUnflushed,
	}
	if out.PollWaitSeconds <= 0 {
		out.PollWaitSeconds = 25
	}
	if out.HeartbeatSeconds <= 0 {
		out.HeartbeatSeconds = 30
	}
	if out.MaxUnflushed <= 0 {
		out.MaxUnflushed = 5000
	}
	return out
}

// validateSyncConfig gates the transcript-upload endpoint.
//
// api_url decides where every message of every conversation is sent, so it
// gets the same shape internal/miviaauth/client.go applies to the auth
// endpoint: a well-formed absolute https URL, or plain http on a literal
// loopback host so a local API (typically http://localhost:3001) still works.
//
// It deliberately does NOT go through validateBaseURL. That function honours
// MIVIA_ALLOW_INSECURE_HTTP, which exists for local provider mocks; inheriting
// it here would let one environment variable redirect conversation content
// onto a cleartext host that is not loopback.
//
// An EMPTY api_url is valid and is now the normal case: nothing asks a user
// to configure sync, and the wiring falls back to the API root
// internal/miviaauth already resolves. A disabled sync is not validated at
// all: an unused key is not a misconfiguration, and refusing to start over
// one would make the opt-out harder to reach than the state it protects
// against.
func validateSyncConfig(cfg ResolvedSync) error {
	if cfg.Disabled {
		return nil
	}
	raw := strings.TrimSpace(cfg.APIURL)
	if raw == "" {
		return nil
	}
	if _, err := ValidateHTTPSURL(raw); err == nil {
		return nil
	}
	if IsOllamaLoopback(raw) {
		return nil
	}
	// Fixed literal on purpose: api_url may carry credentials or control
	// characters, and this message reaches logs.
	return fmt.Errorf("[sync] api_url must be an absolute https URL, or http on a loopback host")
}
