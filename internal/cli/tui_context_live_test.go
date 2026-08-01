package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestLiveContextUsageAppearsInStatusDuringTurn(t *testing.T) {
	m := newReadyChatModel(24, 80)
	// Seed the session so ContextUsage reports a non-zero percentage.
	if err := m.session.SetPromptBudget(1000); err != nil {
		t.Fatal(err)
	}
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("x", 800)},
	}
	m.messages = []string{"user: hi"}
	m.renderVP()

	m.waiting = true
	m.turnStart = time.Now()
	usage := m.session.ContextUsage()
	if usage.Percent <= 0 {
		t.Fatalf("precondition: usage = %+v, want Percent > 0", usage)
	}
	want := fmt.Sprintf("ctx %d%%", usage.Percent)

	view := m.View()
	plain := stripANSI(view)
	if !strings.Contains(plain, want) {
		t.Fatalf("waiting render missing live context %q:\n%s", want, plain)
	}

	// Idle turns must not show a context percentage.
	m.waiting = false
	plainIdle := stripANSI(m.View())
	if strings.Contains(plainIdle, "ctx ") {
		t.Fatalf("idle render must not show context percentage:\n%s", plainIdle)
	}
}

func TestStatusDetailAppendsContextOnlyWhileWaiting(t *testing.T) {
	m := newReadyChatModel(24, 80)
	if err := m.session.SetPromptBudget(1000); err != nil {
		t.Fatal(err)
	}
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("x", 800)},
	}
	m.stepDetail = "searching"

	m.waiting = true
	if got := m.statusDetail(); !strings.HasPrefix(got, "searching") || !strings.Contains(got, "ctx ") {
		t.Fatalf("waiting statusDetail = %q, want stepDetail + ctx", got)
	}
	if got := m.composerDetail(); !strings.HasPrefix(got, "searching") || !strings.Contains(got, "ctx ") {
		t.Fatalf("waiting composerDetail = %q, want stepDetail + ctx", got)
	}

	// Empty stepDetail: status bar keeps ctx, composer keeps "queued" fallback.
	m.stepDetail = ""
	if got := m.statusDetail(); !strings.Contains(got, "ctx ") {
		t.Fatalf("waiting statusDetail with empty step = %q, want ctx", got)
	}
	if got := m.composerDetail(); got != "" {
		t.Fatalf("waiting composerDetail with empty step = %q, want empty (queued fallback)", got)
	}

	m.waiting = false
	m.stepDetail = "searching"
	if got := m.statusDetail(); got != "searching" {
		t.Fatalf("idle statusDetail = %q, want stepDetail unchanged", got)
	}
	if got := m.composerDetail(); got != "searching" {
		t.Fatalf("idle composerDetail = %q, want stepDetail unchanged", got)
	}
}
