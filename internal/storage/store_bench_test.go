package storage

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func BenchmarkSQLiteLogicalAgents(b *testing.B) {
	for _, agents := range []int{1, 10, 50, 100, 200} {
		b.Run(strconv.Itoa(agents), func(b *testing.B) {
			s, err := OpenSQLite(filepath.Join(b.TempDir(), "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				var wg sync.WaitGroup
				for i := 0; i < agents; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						_ = s.Append(context.Background(), Event{ID: strconv.Itoa(n) + "-" + strconv.Itoa(i), RunID: strconv.Itoa(i), Sequence: n + 1, Kind: "agent", Payload: []byte(`{"ok":true}`)})
					}(i)
				}
				wg.Wait()
			}
		})
	}
}

// BenchmarkSQLiteChangesProbe measures the freshness probe the ledger
// projection runs before every read, against a history it is already caught
// up with — the common single-process case.
func BenchmarkSQLiteChangesProbe(b *testing.B) {
	s, err := OpenSQLite(filepath.Join(b.TempDir(), "probe.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for run := 0; run < 100; run++ {
		for seq := 1; seq <= 20; seq++ {
			if err := s.Append(ctx, Event{
				ID:       strconv.Itoa(run) + "-" + strconv.Itoa(seq),
				RunID:    strconv.Itoa(run),
				Sequence: seq, Kind: "k", Payload: []byte(`{"ok":true}`),
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
	_, cursor, err := s.Changes(ctx, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if _, _, err := s.Changes(ctx, cursor); err != nil {
			b.Fatal(err)
		}
	}
}
