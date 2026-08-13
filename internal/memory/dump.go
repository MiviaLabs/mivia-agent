package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
)

// Dump writes store's rows as deterministic JSONL to w (D5). Only the
// sqlite backend supports it - dump exists to make a committed
// .mivia/memory.db reviewable, and the in-memory backend has nothing
// committed to review. store must be a value returned by Open with
// Backend: BackendSQLite; any other Store returns ErrDumpUnsupported.
func Dump(store Store, w io.Writer) error {
	impl, ok := store.(*sqliteStore)
	if !ok {
		return ErrDumpUnsupported
	}
	return impl.DumpJSONL(w)
}

// ErrDumpUnsupported is returned by Dump for a non-sqlite-backed Store.
var ErrDumpUnsupported = errors.New("memory: dump is only supported for the sqlite backend")

// dumpRow is one exported row, field order fixed by struct declaration
// order (Go's json.Marshal preserves it) - decision 4: id, scope, org,
// tier, verdict, tags, title, summary, content, created_at.
type dumpRow struct {
	ID        string   `json:"id"`
	Scope     string   `json:"scope"`
	Org       string   `json:"org"`
	Tier      string   `json:"tier"`
	Verdict   string   `json:"verdict"`
	Tags      []string `json:"tags"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	CreatedAt string   `json:"created_at"`
}

// DumpJSONL writes every row across both the project and org databases (in
// that order) as one JSON object per line, sorted by id within each
// database. id is a stable content hash (entryID), so the same DB always
// produces byte-identical output (D5) - the point is a meaningful git diff
// for a single changed row, not a second binary-shaped artifact.
func (s *sqliteStore) DumpJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, db := range []*sql.DB{s.projectDB, s.orgDB} {
		if db == nil {
			continue
		}
		if err := dumpDB(context.Background(), db, enc); err != nil {
			return err
		}
	}
	return nil
}

func dumpDB(ctx context.Context, db *sql.DB, enc *json.Encoder) error {
	rows, err := db.QueryContext(ctx,
		"SELECT id, scope, org, tier, verdict, tags, title, summary, content, created_at FROM memories ORDER BY id ASC")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r dumpRow
		var tags string
		if err := rows.Scan(&r.ID, &r.Scope, &r.Org, &r.Tier, &r.Verdict, &tags, &r.Title, &r.Summary, &r.Content, &r.CreatedAt); err != nil {
			return err
		}
		r.Tags = splitTags(tags)
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
