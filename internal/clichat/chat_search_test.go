package clichat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestPreprocessChatLineRequiresExactSearchToken(t *testing.T) {
	res := &config.Resolved{Model: "test"}
	sess := chat.NewSession(res, welcomeStubCompleter{})
	output := new(bytes.Buffer)
	term := &Terminal{out: output}
	line, stop, err := preprocessChatLine("/searchtypo", sess, res, false, term, NewChatRenderer(term, "test"))
	if err != nil || !stop || line != "/searchtypo" {
		t.Fatalf("preprocess = (%q, %v, %v)", line, stop, err)
	}
	if !strings.Contains(output.String(), `unknown command "/searchtypo"`) {
		t.Fatalf("unknown command not reported: %q", output.String())
	}
}

func TestPreprocessChatLineRewritesExactSearchToken(t *testing.T) {
	res := &config.Resolved{Model: "test"}
	sess := chat.NewSession(res, welcomeStubCompleter{})
	term := &Terminal{out: new(bytes.Buffer)}
	line, stop, err := preprocessChatLine("/search documentation", sess, res, false, term, NewChatRenderer(term, "test"))
	if err != nil || stop || line != "search the web for: documentation" {
		t.Fatalf("preprocess = (%q, %v, %v)", line, stop, err)
	}
}
