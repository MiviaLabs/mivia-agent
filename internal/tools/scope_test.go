package tools_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type plainTool struct{ name string }

func (t plainTool) Name() string               { return t.name }
func (t plainTool) Description() string        { return t.name }
func (t plainTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t plainTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

type privilegedTool struct{ plainTool }

func (privilegedTool) Privileged() {}

func TestScopedRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	read := plainTool{name: "read_file"}
	write := plainTool{name: "write_file"}
	priv := privilegedTool{plainTool: plainTool{name: "dispatch_tasks"}}
	reg.Register(read)
	reg.Register(write)
	reg.Register(priv)

	// Spawned: denylist + privileged dropped; allowlist keeps read only.
	spawned := tools.ScopedRegistry(reg, tools.ScopeOptions{
		Mode:      tools.ScopeSpawned,
		Allowlist: map[string]struct{}{"read_file": {}, "dispatch_tasks": {}},
	})
	if _, ok := spawned.Get("write_file"); ok {
		t.Fatal("spawned must not keep write_file outside allowlist")
	}
	if _, ok := spawned.Get("dispatch_tasks"); ok {
		t.Fatal("spawned must drop privileged tool even if allowlisted")
	}
	got, ok := spawned.Get("read_file")
	if !ok {
		t.Fatal("spawned must keep allowlisted read_file")
	}
	// Object identity preserved (M8: not name-only reconstruction).
	if got != read {
		t.Fatal("ScopedRegistry must preserve tool object identity/markers")
	}

	// Root: privileged survives; allowlist still filters non-privileged.
	root := tools.ScopedRegistry(reg, tools.ScopeOptions{
		Mode:      tools.ScopeRoot,
		Allowlist: map[string]struct{}{"read_file": {}},
	})
	if _, ok := root.Get("write_file"); ok {
		t.Fatal("root allowlist must drop write_file")
	}
	if _, ok := root.Get("read_file"); !ok {
		t.Fatal("root must keep allowlisted read_file")
	}
	gotPriv, ok := root.Get("dispatch_tasks")
	if !ok {
		t.Fatal("root must retain privileged delegation tool")
	}
	if _, privileged := gotPriv.(tools.PrivilegedTool); !privileged {
		t.Fatal("PrivilegedTool marker must survive ScopedRegistry (M8)")
	}
	if gotPriv != priv {
		t.Fatal("root must preserve privileged tool object identity")
	}
}

func TestMandatoryDenylistMatchesPrivilegedMarker(t *testing.T) {
	// Every compiled denylist name that is also a PrivilegedTool in a typical
	// session registry must be excluded from spawned scope by BOTH mechanisms.
	reg := tools.NewRegistry()
	for _, name := range tools.CompiledMandatoryDenylist {
		reg.Register(privilegedTool{plainTool: plainTool{name: name}})
	}
	reg.Register(plainTool{name: "read_file"})

	spawned := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeSpawned})
	for _, name := range tools.CompiledMandatoryDenylist {
		if _, ok := spawned.Get(name); ok {
			t.Errorf("spawned registry retained denylist/privileged tool %q", name)
		}
	}
	if _, ok := spawned.Get("read_file"); !ok {
		t.Fatal("spawned must keep ordinary tools")
	}

	root := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeRoot})
	for _, name := range tools.CompiledMandatoryDenylist {
		if _, ok := root.Get(name); !ok {
			t.Errorf("root must retain denylist/privileged tool %q", name)
		}
	}
}

func TestMandatoryDenylist_RootExempt_SpawnedFiltered(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(plainTool{name: "delegate"})
	reg.Register(plainTool{name: "dispatch_tasks"})
	reg.Register(plainTool{name: "read_file"})

	root := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeRoot})
	for _, name := range []string{"delegate", "dispatch_tasks", "read_file"} {
		if _, ok := root.Get(name); !ok {
			t.Errorf("root missing %q", name)
		}
	}
	spawned := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeSpawned})
	if _, ok := spawned.Get("delegate"); ok {
		t.Fatal("spawned must filter delegate")
	}
	if _, ok := spawned.Get("dispatch_tasks"); ok {
		t.Fatal("spawned must filter dispatch_tasks")
	}
	if _, ok := spawned.Get("read_file"); !ok {
		t.Fatal("spawned must keep read_file")
	}
}

func TestScopedRegistryConcurrentFromImmutableSource(t *testing.T) {
	// Many concurrent instances derive fresh registries from one immutable source.
	src := tools.NewRegistry()
	src.Register(plainTool{name: "read_file"})
	src.Register(plainTool{name: "write_file"})
	src.Register(privilegedTool{plainTool: plainTool{name: "spawn_agent"}})

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scoped := tools.ScopedRegistry(src, tools.ScopeOptions{
				Mode:      tools.ScopeSpawned,
				Allowlist: map[string]struct{}{"read_file": {}},
			})
			if _, ok := scoped.Get("read_file"); !ok {
				errs <- errString("missing read_file")
				return
			}
			if _, ok := scoped.Get("write_file"); ok {
				errs <- errString("write_file leaked")
				return
			}
			if _, ok := scoped.Get("spawn_agent"); ok {
				errs <- errString("spawn_agent leaked")
				return
			}
			// Source remains complete.
			if _, ok := src.Get("write_file"); !ok {
				errs <- errString("source mutated")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
