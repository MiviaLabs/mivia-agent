package clichat

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestCLISyncOptionsInjectsTheLeafSeams covers the half of settled decision 7
// that a leaf package cannot enforce for itself. chatsync stopped importing
// internal/chat and internal/tools and takes ErrorMessage and RedactToolArgs
// as injected values instead - which means the two host wiring sites are now
// the ONLY thing that supplies them, and a site that forgets gets the zero
// value silently.
//
// RedactToolArgs is the one that bites: its zero value is false, which reads
// as "the operator did not ask for redaction". A host where the operator DID
// ask therefore ships tool arguments the operator asked to have hidden, and
// no test of the projector can see it, because the projector is behaving
// exactly as it was told.
func TestCLISyncOptionsInjectsTheLeafSeams(t *testing.T) {
	res := &config.Resolved{}
	res.Sync.IncludeToolIO = true

	t.Run("ErrorMessage is supplied", func(t *testing.T) {
		opts := cliSyncOptions(&chat.Session{}, t.TempDir(), res, nil)
		if opts.ProjectorOptions.ErrorMessage == nil {
			t.Fatal("ErrorMessage is nil; the CLI site does not inject the classifier, so turn errors fall back to the package default instead of the host's own redaction policy")
		}
		got := opts.ProjectorOptions.ErrorMessage(errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"))
		if got == "" {
			t.Fatal("ErrorMessage returned empty for a real error")
		}
		if got == "dial tcp 10.0.0.1:5432: connect: connection refused" {
			t.Fatalf("ErrorMessage returned the raw error text %q; the wire must never carry it (DC-14)", got)
		}
	})

	// Table over BOTH values: a site that hardcodes either constant passes one
	// case and fails the other, so this cannot be satisfied by a literal.
	for _, redact := range []bool{true, false} {
		t.Run("RedactToolArgs propagates", func(t *testing.T) {
			prev := tools.RedactToolArgs()
			t.Cleanup(func() { tools.SetRedactToolArgs(prev) })
			tools.SetRedactToolArgs(redact)

			opts := cliSyncOptions(&chat.Session{}, t.TempDir(), res, nil)
			if opts.ProjectorOptions.RedactToolArgs != redact {
				t.Fatalf("RedactToolArgs = %v, want %v: the host's redaction decision does not reach the projector, so sync uploads tool arguments the operator asked to hide", opts.ProjectorOptions.RedactToolArgs, redact)
			}
		})
	}
}
