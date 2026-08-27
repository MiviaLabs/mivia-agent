package definition

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func registerTestProfile(t *testing.T, c *Catalogue, name string) {
	t.Helper()
	p := newDeclaredProfile(name, []commandSpec{{check: name, program: "go", args: []string{"version"}}}, nil, secretPolicy(t))
	if err := c.Register(p); err != nil {
		t.Fatalf("register %q: %v", name, err)
	}
}

func TestCatalogueRegistersDeclaredProfiles(t *testing.T) {
	c := NewCatalogue()
	wantNames := []string{"go-final", "go-test", "go-verify"}
	for _, name := range wantNames {
		registerTestProfile(t, c, name)
	}
	gotNames := c.Names()
	sort.Strings(gotNames)
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("names = %#v, want %#v", gotNames, wantNames)
	}
	for _, name := range wantNames {
		p, err := c.Lookup(name)
		if err != nil {
			t.Fatalf("lookup %q: %v", name, err)
		}
		if p.Name() != name {
			t.Fatalf("name = %q, want %q", p.Name(), name)
		}
	}
}

func TestNewCatalogueStartsEmpty(t *testing.T) {
	c := NewCatalogue()
	if names := c.Names(); len(names) != 0 {
		t.Fatalf("names = %#v, want empty", names)
	}
}

func TestLookupUnknownFailsClosed(t *testing.T) {
	c := NewCatalogue()
	registerTestProfile(t, c, "go-test")
	_, err := c.Lookup("shell-from-toml")
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v", err)
	}
	_, err = c.Lookup("")
	if err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestRegisterDuplicateFails(t *testing.T) {
	c := NewCatalogue()
	registerTestProfile(t, c, "go-test")
	p := newDeclaredProfile("go-test", []commandSpec{{check: "go-test", program: "go", args: []string{"version"}}}, nil)
	if err := c.Register(p); err == nil {
		t.Fatal("duplicate accepted")
	}
}
