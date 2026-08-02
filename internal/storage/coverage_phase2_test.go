package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPhase2StoreLifecycleContract(t *testing.T) {
	ctx := context.Background()
	for name, open := range map[string]func(*testing.T) Store{
		"memory": func(*testing.T) Store { return NewMemory() },
		"sqlite": func(t *testing.T) Store {
			store, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			return store
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := open(t)
			defer func() {
				if err := store.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			}()

			for _, event := range []Event{
				{ID: "one", RunID: "run-a", Sequence: 1, Kind: "step", Payload: []byte(`{}`)},
				{ID: "two", RunID: "run-a", Sequence: 2, Kind: "step", Payload: []byte(`{}`)},
				{ID: "three", RunID: "run-b", Sequence: 1, Kind: "step", Payload: []byte(`{}`)},
			} {
				if err := store.Append(ctx, event); err != nil {
					t.Fatalf("Append(%s): %v", event.ID, err)
				}
			}
			if count, err := store.Count(ctx); err != nil || count != 3 {
				t.Fatalf("Count = %d, %v; want 3, nil", count, err)
			}
			if ids, err := store.ListRunIDs(ctx); err != nil || !reflect.DeepEqual(ids, []string{"run-a", "run-b"}) {
				t.Fatalf("ListRunIDs = %v, %v", ids, err)
			}

			if err := store.ClaimRun(ctx, "run-a", "holder-a"); err != nil {
				t.Fatalf("ClaimRun holder-a: %v", err)
			}
			if err := store.ClaimRun(ctx, "run-a", "holder-b"); !errors.Is(err, ErrClaimHeld) {
				t.Fatalf("ClaimRun holder-b = %v, want ErrClaimHeld", err)
			}
			if err := store.ReleaseClaim(ctx, "run-a", "holder-b"); !errors.Is(err, ErrClaimNotHeld) {
				t.Fatalf("ReleaseClaim holder-b = %v, want ErrClaimNotHeld", err)
			}
			if err := store.ReleaseClaim(ctx, "run-a", "holder-a"); err != nil {
				t.Fatalf("ReleaseClaim holder-a: %v", err)
			}
			if err := store.ClaimRun(ctx, "run-a", "holder-a"); err != nil {
				t.Fatalf("ClaimRun after release: %v", err)
			}
			if err := store.ClearClaim(ctx, "run-a"); err != nil {
				t.Fatalf("ClearClaim: %v", err)
			}
			if err := store.ClaimRun(ctx, "run-a", "holder-b"); err != nil {
				t.Fatalf("ClaimRun after clear: %v", err)
			}

			if err := store.DeleteRun(ctx, "run-a", 1); err != nil {
				t.Fatalf("DeleteRun: %v", err)
			}
			events, err := store.Events(ctx, "run-a")
			if err != nil || len(events) != 1 || events[0].ID != "two" {
				t.Fatalf("Events after DeleteRun = %v, %v", events, err)
			}
			if count, err := store.Count(ctx); err != nil || count != 2 {
				t.Fatalf("Count after DeleteRun = %d, %v; want 2, nil", count, err)
			}
		})
	}
}
