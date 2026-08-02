package chat

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The rolling calibration is an accumulating measurement, not turn history:
// it is seeded into every agent loop from the session and written back when
// the turn ends, whether or not that turn's history was adopted.

// calibrationCompleter reports usage until failAfter calls, then errors.
type calibrationCompleter struct {
	usage     provider.TokenUsage
	calls     int
	failAfter int // 0 = never fail
}

func (c *calibrationCompleter) Name() string { return "calibration" }

func (c *calibrationCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *calibrationCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	if w != nil {
		_, _ = io.WriteString(w, resp.Content)
	}
	return resp.Content, nil
}

func (c *calibrationCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	if c.failAfter > 0 && c.calls > c.failAfter {
		return nil, errors.New("provider unavailable")
	}
	return &provider.Response{Content: "ok", FinishReason: "stop", TokenUsage: c.usage}, nil
}

func calibrationSession(comp provider.Completer) *Session {
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = tools.NewRegistry()
	return s
}

func TestSessionCalibrationAccumulatesEveryTurn(t *testing.T) {
	// Seeding, not just last-write-wins: each turn must start from the
	// session's calibrator, so the sample count grows one per turn.
	s := calibrationSession(&calibrationCompleter{
		usage: provider.TokenUsage{Reported: true, InputTokens: 200, OutputTokens: 50},
	})
	for turn := 1; turn <= 3; turn++ {
		if _, err := s.SendUser(context.Background(), "ask", io.Discard); err != nil {
			t.Fatal(err)
		}
		if s.Calibration.Samples != turn {
			t.Fatalf("after turn %d: samples=%d want %d (loop was not seeded from the session)", turn, s.Calibration.Samples, turn)
		}
	}
	// Bounded to [0.5, 3.0]; the epsilon absorbs EWMA float rounding at the clamp.
	const epsilon = 1e-9
	if s.Calibration.Ratio < 0.5-epsilon || s.Calibration.Ratio > 3.0+epsilon {
		t.Fatalf("ratio %f escaped the bounded range", s.Calibration.Ratio)
	}
}

func TestSessionCalibrationSurvivesAFailedTurn(t *testing.T) {
	// A turn that errors drops its history, but the observations already made
	// stay true - discarding them would leave the heuristic uncorrected on
	// exactly the turns that drift most.
	comp := &calibrationCompleter{
		usage:     provider.TokenUsage{Reported: true, InputTokens: 200, OutputTokens: 50},
		failAfter: 1,
	}
	s := calibrationSession(comp)
	if _, err := s.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	before := s.Calibration
	if before.Samples != 1 {
		t.Fatalf("first turn did not record calibration: %+v", before)
	}

	if _, err := s.SendUser(context.Background(), "second", io.Discard); err == nil {
		t.Fatal("expected the provider failure to surface")
	}
	if s.Calibration != before {
		t.Fatalf("failed turn changed calibration: %+v want %+v", s.Calibration, before)
	}
}

func TestAdoptCalibrationKeepsTheMostInformedValue(t *testing.T) {
	// Concurrent turns are seeded from the same value and each only adds
	// samples, so the larger sample count is the more informed one. A turn
	// that observed nothing must never overwrite one that did.
	s := calibrationSession(&calibrationCompleter{})
	s.Calibration = contextmgr.Calibration{Ratio: 1.4, Samples: 5}

	s.adoptCalibration(contextmgr.Calibration{})
	if s.Calibration.Samples != 5 || s.Calibration.Ratio != 1.4 {
		t.Fatalf("a turn with no observations clobbered the calibrator: %+v", s.Calibration)
	}

	s.adoptCalibration(contextmgr.Calibration{Ratio: 2.0, Samples: 3})
	if s.Calibration.Samples != 5 || s.Calibration.Ratio != 1.4 {
		t.Fatalf("a less-informed turn won: %+v", s.Calibration)
	}

	s.adoptCalibration(contextmgr.Calibration{Ratio: 2.0, Samples: 7})
	if s.Calibration.Samples != 7 || s.Calibration.Ratio != 2.0 {
		t.Fatalf("a more-informed turn was dropped: %+v", s.Calibration)
	}
}
