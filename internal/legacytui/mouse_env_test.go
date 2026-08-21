package legacytui

import (
	"testing"
)

func TestMouseAvailable_EnvOverride(t *testing.T) {
	t.Setenv("MIVIA_MOUSE", "0")
	if mouseAvailable() {
		t.Fatal("MIVIA_MOUSE=0 must force off")
	}
	t.Setenv("MIVIA_MOUSE", "false")
	if mouseAvailable() {
		t.Fatal("MIVIA_MOUSE=false must force off")
	}
	t.Setenv("MIVIA_MOUSE", "1")
	if !mouseAvailable() {
		t.Fatal("MIVIA_MOUSE=1 must force on")
	}
	t.Setenv("MIVIA_MOUSE", "on")
	if !mouseAvailable() {
		t.Fatal("MIVIA_MOUSE=on must force on")
	}
}

func TestMouseAvailable_DumbTERM(t *testing.T) {
	t.Setenv("MIVIA_MOUSE", "") // clear force
	t.Setenv("TERM", "dumb")
	// Even if stdin is a TTY in some CI, dumb TERM must fail unless forced.
	// Force off path via empty override and dumb term:
	if mouseAvailable() {
		// Only fails if env force-on was left; we cleared MIVIA_MOUSE.
		// On non-TTY CI this is false; on TTY with dumb TERM also false.
		t.Fatal("TERM=dumb must report mouse unavailable")
	}
}

func TestMouseAvailable_EmptyTERM(t *testing.T) {
	t.Setenv("MIVIA_MOUSE", "")
	t.Setenv("TERM", "")
	if mouseAvailable() {
		t.Fatal("empty TERM must report mouse unavailable")
	}
}

func TestNewTUIModel_MouseFollowsAvailability(t *testing.T) {
	t.Setenv("MIVIA_MOUSE", "1")
	m := newTUIModel(makeTestSession(), nil, true)
	if !m.mouseEnabled {
		t.Fatal("newTUIModel must enable mouse when MIVIA_MOUSE=1")
	}
	t.Setenv("MIVIA_MOUSE", "0")
	m2 := newTUIModel(makeTestSession(), nil, true)
	if m2.mouseEnabled {
		t.Fatal("newTUIModel must disable mouse when MIVIA_MOUSE=0")
	}
}
