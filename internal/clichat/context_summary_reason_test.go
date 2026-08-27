package clichat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// A workspace that has not configured summarization gets structural-only
// compaction: /compact returns instantly, makes no LLM call, and - before
// this - said nothing at all. The operator sees a compaction "work" while the
// summary they configured never runs, with no way to tell which of the three
// conditions is missing. summaryWiring's own doc calls the false return "a
// policy state, never an error", which is right, but a silent policy state is
// undiagnosable.
func summaryReasonResolved(t *testing.T, mutate func(*config.Resolved)) *config.Resolved {
	t.Helper()
	res := summaryWiringResolved(t, true)
	mutate(res)
	return res
}

// TestSummaryDisabledReasonStaysSilentWithoutConfig pins that a caller with
// no resolved configuration reports nothing rather than inventing a cause.
func TestSummaryDisabledReasonStaysSilentWithoutConfig(t *testing.T) {
	if reason := SummaryDisabledReason(nil, nil); reason != "" {
		t.Fatalf("reason = %q, want silence when there is no configuration", reason)
	}
}

func TestSummaryDisabledReasonNamesTheMissingCondition(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Resolved)
		want   string
	}{
		{"flag off", func(r *config.Resolved) { off := false; r.Context.Summary.Enabled = &off }, "context.summary"},
		{"no endpoint", func(r *config.Resolved) { r.BaseURL = "" }, "endpoint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := summaryReasonResolved(t, tc.mutate)
			sess := chat.NewSession(res, nullCompleter{})
			_, _, ok := summaryWiring(sess, res)
			if ok {
				t.Fatal("summaryWiring reported enabled for a workspace missing a condition")
			}
			reason := SummaryDisabledReason(sess, res)
			if reason == "" {
				t.Fatal("a disabled summary reported no reason")
			}
			if !strings.Contains(strings.ToLower(reason), tc.want) {
				t.Fatalf("reason = %q, want it to name %q", reason, tc.want)
			}
		})
	}
}

// TestSummaryDisabledReasonIsEmptyWhenWired pins the quiet path: a fully
// configured workspace must report nothing, so the notice never becomes noise.
// TestSummaryDisabledReasonIgnoresMissingRedaction pins that [privacy] is no
// longer a blocker: a workspace without it summarizes, so nothing should be
// reported as disabling the summary.
func TestSummaryDisabledReasonIgnoresMissingRedaction(t *testing.T) {
	res := summaryWiringResolved(t, true)
	res.RedactionPolicy = nil
	res.Privacy = config.PrivacyConfig{}
	sess := chat.NewSession(res, nullCompleter{})
	if reason := SummaryDisabledReason(sess, res); reason != "" {
		t.Fatalf("missing [privacy] reported as disabling the summary: %q", reason)
	}
}

func TestSummaryDisabledReasonIsEmptyWhenWired(t *testing.T) {
	res := summaryWiringResolved(t, true)
	sess := chat.NewSession(res, nullCompleter{})
	if _, _, ok := summaryWiring(sess, res); !ok {
		t.Fatal("harness precondition: summaryWiring should be enabled here")
	}
	if reason := SummaryDisabledReason(sess, res); reason != "" {
		t.Fatalf("a wired summary still reported %q", reason)
	}
}
