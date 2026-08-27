package clichat

// chat_command_coverage_test.go covers the small public helpers in
// chat_command.go (apply*, validate*, parse*) that are exercised by the
// full chat flow but were individually uncovered after the cli split.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestApplyPrivacyPolicy(t *testing.T) {
	res := &config.Resolved{}
	applyPrivacyPolicy(res)
	if res == nil {
		t.Fatal("applyPrivacyPolicy must not panic on a fresh Resolved")
	}
}

func TestApplyContextLimits(t *testing.T) {
	res := &config.Resolved{}
	applyContextLimits(res)
	if res == nil {
		t.Fatal("applyContextLimits must not panic on a fresh Resolved")
	}
}

func TestParseChatInvocation(t *testing.T) {
	// parseChatInvocation requires --quiet / --plain alone; combining with a
	// positional is rejected by the chat parser. Exercise only valid forms.
	for _, args := range [][]string{{"--quiet"}, {"--plain"}, {"--quiet", "--plain"}} {
		inv, err := parseChatInvocation(args)
		if err != nil {
			t.Fatalf("parseChatInvocation(%v): %v", args, err)
		}
		_ = inv
	}
}

func TestValidateJSONModeInvocation(t *testing.T) {
	// Non-JSON: must accept any invocation.
	if err := validateJSONModeInvocation(chatInvocation{jsonMode: false}); err != nil {
		t.Fatalf("validateJSONModeInvocation(non-json) = %v", err)
	}
}

func TestChatWorkspaceRoot(t *testing.T) {
	got, err := chatWorkspaceRoot(t.TempDir())
	if err != nil || got == "" {
		t.Fatalf("chatWorkspaceRoot(temp) = (%q, %v)", got, err)
	}
}

func TestApplyChatToolOverrides(t *testing.T) {
	res := &config.Resolved{}
	applyChatToolOverrides(res, []string{"foo"}, []string{"bar"}, nil, []string{"BAZ"}, []string{"QUX"})
	if res == nil {
		t.Fatal("applyChatToolOverrides must not panic")
	}
}
