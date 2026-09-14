package theme

import "testing"

func TestSearchStatusPaletteBeatsHandPicked(t *testing.T) {
	// research-panes.md section 3.2: a hand-picked-by-eye dark palette
	// measured a worst-case dE of 6.9; the search should comfortably beat
	// naive taste on the same axis. This is not a reproduction of that
	// exact number (different implementation), only the qualitative claim
	// the shipped package must satisfy: search beats a poor hand pick.
	handPicked := map[Role]string{
		RoleSuccess: "#4a7a4a",
		RoleWarning: "#7a7a4a",
		RoleDanger:  "#7a4a4a",
		RoleInfo:    "#4a4a7a",
	}
	handWorst, _, err := WorstCaseSeparation(handPicked, []Role{RoleSuccess, RoleWarning, RoleDanger, RoleInfo})
	if err != nil {
		t.Fatal(err)
	}

	result, err := SearchStatusPalette("#0a0a0b", SearchOptions{
		MinContrast: 3.0,
		Iterations:  500,
		Seed:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorstDE <= handWorst {
		t.Fatalf("search worst dE %.2f did not beat hand-picked %.2f", result.WorstDE, handWorst)
	}
	for _, r := range []Role{RoleSuccess, RoleWarning, RoleDanger, RoleInfo} {
		hex, ok := result.Colors[r]
		if !ok {
			t.Fatalf("missing role %s in search result", r)
		}
		ratio, err := contrastRatio(hex, "#0a0a0b")
		if err != nil {
			t.Fatal(err)
		}
		if ratio < 3.0 {
			t.Errorf("role %s: contrast %.2f below required 3.0", r, ratio)
		}
	}
}

func TestSearchStatusPaletteDeterministic(t *testing.T) {
	opts := SearchOptions{MinContrast: 3.0, Iterations: 100, Seed: 42}
	a, err := SearchStatusPalette("#fcfcfc", opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SearchStatusPalette("#fcfcfc", opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.WorstDE != b.WorstDE {
		t.Fatalf("same seed produced different results: %.4f vs %.4f", a.WorstDE, b.WorstDE)
	}
	for _, r := range []Role{RoleSuccess, RoleWarning, RoleDanger, RoleInfo} {
		if a.Colors[r] != b.Colors[r] {
			t.Fatalf("role %s: same seed produced different colours: %s vs %s", r, a.Colors[r], b.Colors[r])
		}
	}
}

func TestSearchStatusPaletteRejectsBadInput(t *testing.T) {
	if _, err := SearchStatusPalette("#000000", SearchOptions{}); err == nil {
		t.Fatal("expected error for zero Iterations")
	}
	if _, err := SearchStatusPalette("not-a-colour", SearchOptions{Iterations: 10}); err == nil {
		t.Fatal("expected error for invalid bg")
	}
}

func TestSearchStatusPaletteNoCandidateFound(t *testing.T) {
	// An unreachable contrast floor (WCAG's ratio maxes out at 21:1) means
	// no random sample can ever qualify, forcing the "found no candidate"
	// error path.
	_, err := SearchStatusPalette("#0a0a0b", SearchOptions{MinContrast: 100, Iterations: 5, Seed: 1})
	if err == nil {
		t.Fatal("expected error when no candidate can meet an unreachable contrast floor")
	}
}
