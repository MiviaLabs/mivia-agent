package chat

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestBoundedNamesKeepsSmallListsUntouched: within the count cap the note reads
// exactly like the old full join, so nothing users already see changes.
func TestBoundedNamesKeepsSmallListsUntouched(t *testing.T) {
	names := []string{"grep", "glob", "rg"}
	got := boundedNames(names, maxAdmissionNoteNames)
	if want := strings.Join(names, ", "); got != want {
		t.Fatalf("boundedNames(%v) = %q, want %q", names, got, want)
	}
}

// TestBoundedNamesCapsTheCount: past the cap the list is cut and the reader is
// told how many names were left off, so a large set cannot be replayed in full
// into a note.
func TestBoundedNamesCapsTheCount(t *testing.T) {
	var names []string
	for i := 0; i < maxAdmissionNoteNames+7; i++ {
		names = append(names, fmt.Sprintf("tool%d", i))
	}
	got := boundedNames(names, maxAdmissionNoteNames)
	want := strings.Join(names[:maxAdmissionNoteNames], ", ") + "… and 7 more"
	if got != want {
		t.Fatalf("boundedNames = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("boundedNames returned invalid UTF-8: %q", got)
	}
}

// TestBoundedNamesClampsTotalLength: the count cap alone cannot stop one
// pathologically long name from blowing the note up, so the rendered length is
// clamped too.
func TestBoundedNamesClampsTotalLength(t *testing.T) {
	got := boundedNames([]string{strings.Repeat("x", 1<<20)}, maxAdmissionNoteNames)
	if n := utf8.RuneCountInString(got); n > maxAdmissionNoteRunes {
		t.Fatalf("rendered %d runes, want at most %d", n, maxAdmissionNoteRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clamped list %q lacks a truncation marker", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("boundedNames returned invalid UTF-8: %q", got)
	}
}

// TestWorstCaseAdmittedSetProducesBoundedNotes is the context-path ceiling:
// MaxAdmissionPublications (8) widenings x MaxAdmissionNamesPerCall (64 names)
// is 512 names on the admitted set, and StageToolAdmission now ENFORCES that
// total, so the worst case is a real bound rather than a theoretical one.
// Every Session.Load funnels that set through the note paths, and the notes
// must stay bounded however the record grew.
func TestWorstCaseAdmittedSetProducesBoundedNotes(t *testing.T) {
	names := make([]string, 0, tools.MaxAdmissionPublications*tools.MaxAdmissionNamesPerCall)
	for i := 0; i < tools.MaxAdmissionPublications*tools.MaxAdmissionNamesPerCall; i++ {
		names = append(names, fmt.Sprintf("tool-%d", i))
	}
	if len(names) != 512 {
		t.Fatalf("worst-case set = %d names, want 512", len(names))
	}
	got := boundedNames(names, maxAdmissionNoteNames)
	if n := utf8.RuneCountInString(got); n > maxAdmissionNoteRunes {
		t.Fatalf("worst-case list rendered %d runes, want at most %d", n, maxAdmissionNoteRunes)
	}

	// The two note paths every Session.Load can hit render the full recorded
	// set; drive them directly with the grown set. The bound below is generous
	// for a 512-rune list plus the surrounding prose, and far below the many
	// thousands of runes the old full join would produce.
	sess := newAdmissionSession(t)
	sess.noteAdmissionDrop(names)
	sess.noteAdmissionRetained(names)
	notes := sess.TakeAdmissionNotes()
	if len(notes) != 2 {
		t.Fatalf("notes = %v, want the drop and retained notes", notes)
	}
	for _, note := range notes {
		if n := utf8.RuneCountInString(note); n > 1024 {
			t.Fatalf("note is %d runes, want bounded: %q", n, note)
		}
	}
}

// TestDeferralAndNoOpNotesAreBoundedToo covers the two other unbounded note
// sites: the deferral note (PublishPendingAdmission at a quiet boundary) and
// the consecutive-no-op streak error. Both are cheaply reachable from a
// session, so they are asserted directly rather than left to helper coverage.
func TestDeferralAndNoOpNotesAreBoundedToo(t *testing.T) {
	names := make([]string, 0, 512)
	for i := 0; i < 512; i++ {
		names = append(names, fmt.Sprintf("tool-%d", i))
	}

	// deferAdmissionLocked: a stage that cannot publish at the boundary notes
	// its pending names.
	sess := newAdmissionSession(t)
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		return false, nil
	})
	if _, err := sess.StageToolAdmission(names, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.PublishPendingAdmission() // no active turn -> deferred, noted
	notes := sess.TakeAdmissionNotes()
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want one deferral note", notes)
	}
	if n := utf8.RuneCountInString(notes[0]); n > 1024 {
		t.Fatalf("deferral note is %d runes, want bounded: %q", n, notes[0])
	}

	// noOpStreakError: a loop re-requesting a large already-loaded set.
	sess2 := newAdmissionSession(t)
	admitTools(sess2, names...)
	var streakErr error
	for i := 0; i <= maxConsecutiveAdmissionNoOps; i++ {
		_, streakErr = sess2.StageToolAdmission(names, 0)
	}
	if streakErr == nil {
		t.Fatal("the no-op streak bound never fired")
	}
	if n := utf8.RuneCountInString(streakErr.Error()); n > 1024 {
		t.Fatalf("streak error is %d runes, want bounded: %q", n, streakErr)
	}
}
