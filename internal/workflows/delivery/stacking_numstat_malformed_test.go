package delivery

import (
	"strings"
	"testing"
)

// numstat -z is parsed byte by byte, and a rename record's two paths arrive as
// two SEPARATE following records. Every malformed shape below would otherwise
// be read as a path and fed to `git reset`/`git add`, so each guard has to
// reject on its own - which means each arm of the disjunctions needs its own
// case.
func TestNumstatPerFileRejectsMalformedRenameRecords(t *testing.T) {
	rec := func(parts ...string) string { return strings.Join(parts, "\x00") }

	cases := map[string]string{
		// Rename header with neither following path.
		"missing both paths": rec("1\t1\t"),
		// Rename header with only the old path.
		"missing new path": rec("1\t1\t", "old.txt"),
		// Present but EMPTY old path - the first arm of the empty check.
		"empty old path": rec("1\t1\t", "", "new.txt", ""),
		// Present but EMPTY new path - the second arm, which a mutant that
		// turns the "||" into "&&" would let through.
		"empty new path": rec("1\t1\t", "old.txt", "", ""),
		// Not enough tab-separated fields to be a numstat record at all.
		"short record": rec("1\t1", ""),
		// Counts that are neither a number nor the binary "-".
		"unparseable count": rec("x\t1\tfile.txt", ""),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			files, total, err := numstatPerFile(out)
			if err == nil {
				t.Fatalf("numstatPerFile accepted a malformed record: files=%+v total=%d", files, total)
			}
		})
	}
}

// TestNumstatPerFileAcceptsWellFormedRecords is the complement, so the guards
// above cannot be satisfied by rejecting everything: an ordinary change and a
// complete rename must both parse, the rename as ONE record carrying BOTH of
// its real paths.
func TestNumstatPerFileAcceptsWellFormedRecords(t *testing.T) {
	out := strings.Join([]string{"30\t0\thuge.txt", "20\t20\t", "old.txt", "new.txt", ""}, "\x00")
	files, total, err := numstatPerFile(out)
	if err != nil {
		t.Fatalf("numstatPerFile: %v", err)
	}
	if total != 70 {
		t.Fatalf("total = %d, want 30 + 40", total)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want one ordinary record and one rename", files)
	}
	if len(files[0].paths) != 1 || files[0].paths[0] != "huge.txt" {
		t.Fatalf("files[0].paths = %v, want [huge.txt]", files[0].paths)
	}
	if len(files[1].paths) != 2 || files[1].paths[0] != "old.txt" || files[1].paths[1] != "new.txt" {
		t.Fatalf("files[1].paths = %v, want both halves of the rename", files[1].paths)
	}
}

// TestNumstatPerFileCountsBinaryAsZero pins the "-" branch: a binary file has
// no line counts and must contribute zero rather than fail the parse.
func TestNumstatPerFileCountsBinaryAsZero(t *testing.T) {
	files, total, err := numstatPerFile(strings.Join([]string{"-\t-\timage.png", ""}, "\x00"))
	if err != nil {
		t.Fatalf("numstatPerFile: %v", err)
	}
	if total != 0 || len(files) != 1 || files[0].lines != 0 {
		t.Fatalf("files=%+v total=%d, want one zero-weight record", files, total)
	}
}
