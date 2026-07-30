package cli

import (
	"strings"
	"testing"
)

// The state logo engine renders the brand diamond as a seamless animation
// loop per brand phase (the "state language"): idle clockwork, thinking
// orbit, streaming caustic, tool glints, multi twin lights, frozen error.

func brailleLitCount(plain string) int {
	n := 0
	for _, r := range plain {
		if r > 0x2800 && r <= 0x28FF {
			n++
		}
	}
	return n
}

func TestStateLogoFramesShape(t *testing.T) {
	for _, phase := range []brandPhase{
		phaseIdle, phaseWelcome, phaseAwaiting, phaseThinking,
		phaseStreaming, phaseTools, phaseMulti, phaseQueued,
		phaseError, phaseCancel,
	} {
		frames := stateLogoFrames(phase)
		if len(frames) != stateLogoNFrames {
			t.Fatalf("phase %v: %d frames, want %d", phase, len(frames), stateLogoNFrames)
		}
		for i, f := range frames {
			plain := stripANSI(f)
			rows := strings.Count(plain, "\n") + 1
			if rows != stateLogoPxH/4 {
				t.Fatalf("phase %v frame %d: %d rows, want %d", phase, i, rows, stateLogoPxH/4)
			}
			// The outline guarantees a lit mark on every frame of every state —
			// the diamond never disappears mid-loop.
			if got := brailleLitCount(plain); got < 20 {
				t.Fatalf("phase %v frame %d: only %d lit cells", phase, i, got)
			}
		}
	}
}

func TestStateLogoAnimatedPhasesVary(t *testing.T) {
	for _, phase := range []brandPhase{phaseIdle, phaseThinking, phaseStreaming, phaseTools, phaseMulti} {
		frames := stateLogoFrames(phase)
		first := stripANSI(frames[0])
		varies := false
		for _, f := range frames[1:] {
			if stripANSI(f) != first {
				varies = true
				break
			}
		}
		if !varies {
			t.Fatalf("phase %v: all frames identical — animation lost", phase)
		}
	}
}

func TestStateLogoFrozenPhasesStatic(t *testing.T) {
	// Error and cancel freeze: motion stopping is the signal.
	for _, phase := range []brandPhase{phaseError, phaseCancel} {
		frames := stateLogoFrames(phase)
		for i, f := range frames[1:] {
			if f != frames[0] {
				t.Fatalf("phase %v frame %d differs — frozen state must not move", phase, i+1)
			}
		}
	}
}

func TestStateLogoPhaseAliases(t *testing.T) {
	// Welcome shares the idle loop; awaiting shares thinking.
	idle := stateLogoFrames(phaseIdle)
	welcome := stateLogoFrames(phaseWelcome)
	for i := range idle {
		if idle[i] != welcome[i] {
			t.Fatalf("welcome frame %d differs from idle", i)
		}
	}
	think := stateLogoFrames(phaseThinking)
	await := stateLogoFrames(phaseAwaiting)
	for i := range think {
		if think[i] != await[i] {
			t.Fatalf("awaiting frame %d differs from thinking", i)
		}
	}
}

func TestStateLogoDeterministic(t *testing.T) {
	a := renderStateLogo(phaseThinking, 7, 60)
	b := renderStateLogo(phaseThinking, 7, 60)
	if a != b {
		t.Fatal("same phase+frame must render identically")
	}
	// Negative and overflow frames are safe.
	if renderStateLogo(phaseIdle, -3, 0) == "" {
		t.Fatal("negative frame must still render")
	}
	if renderStateLogo(phaseIdle, stateLogoNFrames*5+3, 0) == "" {
		t.Fatal("overflow frame must still render")
	}
}

func TestStateLogoSeamlessLoop(t *testing.T) {
	// Frame N wraps to frame 0 — the loop has no visible seam because every
	// painter is periodic in t. Rendering frame index N must equal frame 0.
	for _, phase := range []brandPhase{phaseIdle, phaseThinking, phaseStreaming, phaseMulti} {
		if renderStateLogo(phase, stateLogoNFrames, 0) != renderStateLogo(phase, 0, 0) {
			t.Fatalf("phase %v: frame N != frame 0", phase)
		}
	}
}

func TestStateLogoNoMotionEnvFreezes(t *testing.T) {
	t.Setenv("MIVIA_NO_MOTION", "1")
	a := renderStateLogo(phaseIdle, 0, 0)
	b := renderStateLogo(phaseIdle, 9, 0)
	if a != b {
		t.Fatal("MIVIA_NO_MOTION must pin the logo to a single frame")
	}
}

func TestStatusBarMiniDiamondAnimates(t *testing.T) {
	// The two-line header's mini diamond is the live state mark: it animates
	// across frames for working phases and is drawn from the state engine,
	// not the retired single-cell glyph or wordmark.
	a := stripANSI(renderStatusBar(0, phaseThinking, "m", true, 0, 0, 0, 0, 0, 3, 80, ""))
	b := stripANSI(renderStatusBar(stateLogoNFrames/3, phaseThinking, "m", true, 0, 0, 0, 0, 0, 3, 80, ""))
	prefix := func(s string) string {
		line := strings.SplitN(s, "\n", 2)[0]
		r := []rune(line)
		return string(r[:min(len(r), miniLogoPxW/2)])
	}
	if prefix(a) == prefix(b) && strings.SplitN(a, "\n", 2)[1] == strings.SplitN(b, "\n", 2)[1] {
		t.Fatalf("mini diamond must animate across frames:\n%q\n%q", a, b)
	}
}

func TestWelcomeSingleInstructionLine(t *testing.T) {
	// The splash carries exactly one instruction line (the bottom hint, which
	// now leads with the primary action). The old centered tag under the hero
	// duplicated it and cost the session picker a row on small terminals.
	m := newTUIModel(makeTestSession(), nil, true)
	m.ready = true
	m.mode = modeWelcome
	m.width = 80
	m.height = 36
	view := stripANSI(m.View())
	if strings.Contains(view, "type a message to start · select a session") {
		t.Fatalf("welcome still renders the redundant tag line")
	}
	if !strings.Contains(view, "type to start") {
		t.Fatalf("welcome hint lost the primary action: %s", view)
	}
	if !strings.Contains(view, "ctrl+c quit") {
		t.Fatalf("welcome hint lost quit key")
	}
}

func TestWelcomeHeroAnimatesIdleState(t *testing.T) {
	// The splash hero is the idle state of the live logo, not a static mark.
	a, _ := renderHeroBraille(0, 80)
	b, _ := renderHeroBraille(stateLogoNFrames/3, 80)
	if stripANSI(a) == stripANSI(b) {
		t.Fatal("welcome hero no longer animates")
	}
	// Layout contract stays: 12 logo rows + title + slogan = 14 lines.
	if _, lines := renderHeroBraille(0, 80); lines != 14 {
		t.Fatalf("hero lines=%d want 14", lines)
	}
}
