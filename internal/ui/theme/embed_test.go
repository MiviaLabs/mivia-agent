package theme

import "testing"

func TestEmbeddedThemesLoad(t *testing.T) {
	themes, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"mivia-dark": false, "mivia-light": false, "mivia-high-contrast": false}
	for _, th := range themes {
		if _, ok := want[th.Name]; ok {
			want[th.Name] = true
		}
		if !th.FirstParty {
			t.Errorf("%s: expected FirstParty=true", th.Name)
		}
		for _, r := range AllRoles() {
			if _, ok := th.Color(r); !ok {
				t.Errorf("%s: missing colour for role %s", th.Name, r)
			}
			if _, ok := th.Ansi16(r); !ok {
				t.Errorf("%s: missing ansi16 index for role %s", th.Name, r)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected embedded theme %q not found", name)
		}
	}
}

func TestLoadUserDirMissingIsNotError(t *testing.T) {
	themes, err := LoadUserDir("/nonexistent/path/for/theme/test")
	if err != nil {
		t.Fatalf("missing user theme dir should not error, got %v", err)
	}
	if len(themes) != 0 {
		t.Fatalf("expected no themes for missing dir, got %v", themes)
	}
}
