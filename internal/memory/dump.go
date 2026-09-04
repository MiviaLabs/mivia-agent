package memory

import (
	"context"
	"encoding/json"
	"io"
)

// Dump writes a deterministic JSONL view of the current store. It is a
// compatibility export for operators; Markdown files remain the source of
// truth for durable memory.
func Dump(store Store, w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, scope := range []Scope{ScopeProject, ScopeOrg} {
		entries, err := store.Search(context.Background(), Query{Scope: scope, MaxResults: 0})
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := enc.Encode(entry); err != nil {
				return err
			}
		}
	}
	return nil
}
