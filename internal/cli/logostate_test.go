package cli

import (
	"math"
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

func TestMiniDiamondEdgeLitNotBlob(t *testing.T) {
	// At 8×8 the dithered facet fill read as a blob of pixels. The mini mark
	// is edge-lit instead: a clean diamond outline whose edges carry the
	// light. The four center pixels stay dark in every frame of every phase.
	for _, ph := range []brandPhase{phaseIdle, phaseThinking, phaseStreaming, phaseTools, phaseMulti, phaseError} {
		frames := stateLogoFramesSized(ph, miniLogoPxW, miniLogoPxH)
		for i, f := range frames {
			rows := strings.Split(stripANSI(f), "\n")
			if len(rows) != 2 {
				t.Fatalf("phase %v frame %d: %d rows", ph, i, len(rows))
			}
			r0, r1 := []rune(rows[0]), []rune(rows[1])
			if len(r0) < 4 || len(r1) < 4 {
				t.Fatalf("phase %v frame %d: rows too short", ph, i)
			}
			// Center pixels (3,3),(4,3),(3,4),(4,4) in braille bit terms.
			if (r0[1]-0x2800)&0x80 != 0 || (r0[2]-0x2800)&0x40 != 0 ||
				(r1[1]-0x2800)&0x08 != 0 || (r1[2]-0x2800)&0x01 != 0 {
				t.Fatalf("phase %v frame %d: interior lit — mini mark must be edge-only:\n%s\n%s", ph, i, rows[0], rows[1])
			}
			// The outline itself is always lit: every frame has edge dots.
			lit := 0
			for _, r := range append(r0[:4:4], r1[:4]...) {
				if r > 0x2800 && r <= 0x28FF {
					lit++
				}
			}
			if lit < 3 {
				t.Fatalf("phase %v frame %d: outline too sparse (%d lit cells)", ph, i, lit)
			}
		}
	}
}

func TestWelcomeHeroLockup(t *testing.T) {
	// Lockup+ splash: diamond left, identity right, flush-left like a tool.
	// No greeting title, no version string; facts are model + workspace.
	block, lines := renderHeroBraille(0, 80, "claude-opus-5", "~/projects/app")
	plain := stripANSI(block)
	if lines != 8 {
		t.Fatalf("lockup hero lines=%d want 8", lines)
	}
	if strings.Contains(plain, "Welcome to") {
		t.Fatalf("greeting title must be gone: %q", plain)
	}
	if !strings.Contains(plain, "mivia") {
		t.Fatalf("missing wordmark: %q", plain)
	}
	if !strings.Contains(plain, "autonomous agents") {
		t.Fatalf("missing slogan: %q", plain)
	}
	if !strings.Contains(plain, "claude-opus-5") || !strings.Contains(plain, "~/projects/app") {
		t.Fatalf("missing facts line: %q", plain)
	}
	if strings.Contains(plain, "v0.") {
		t.Fatalf("version must not appear: %q", plain)
	}
	// Left-aligned lockup, not a centered poster.
	first := strings.Split(plain, "\n")[0]
	if lead := len(first) - len(strings.TrimLeft(first, " ")); lead > 8 {
		t.Fatalf("hero centered (lead=%d), want flush-left", lead)
	}
}

func TestStatusBarMiniDiamondAnimates(t *testing.T) {
	// The mini mark's geometry is deliberately constant (edge-lit outline);
	// the animation is carried by edge luminance, which becomes color in the
	// terminal. Pin it at the engine level: the brightness field must differ
	// across the loop for a working phase.
	anim := stateAnims[phaseThinking]
	g1 := newBrightGrid(miniLogoPxW, miniLogoPxH)
	g2 := newBrightGrid(miniLogoPxW, miniLogoPxH)
	anim.paintMini(0, 0, g1, gridGeom(g1))
	anim.paintMini(2*math.Pi/3, stateLogoNFrames/3, g2, gridGeom(g2))
	same := true
	for i := range g1.v {
		if g1.v[i] != g2.v[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("mini edge luminance must animate across the loop")
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
	a, _ := renderHeroBraille(0, 80, "m", "~/w")
	b, _ := renderHeroBraille(stateLogoNFrames/3, 80, "m", "~/w")
	if stripANSI(a) == stripANSI(b) {
		t.Fatal("welcome hero no longer animates")
	}
}
