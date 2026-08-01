package hooks

import "testing"

func hashOf(t *testing.T, body string) string {
	t.Helper()
	groups, err := Parse([]byte(body), "cfg.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("no groups parsed")
	}
	return groups[0].Hash
}

const hashBase = `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./a.sh", "--x"]
  timeout = 10
  on_timeout = "block"

  [[hooks.handlers]]
  type = "command"
  argv = ["./b.sh"]
  timeout = 10
  on_timeout = "block"
`

// Reformatting a config must not revoke trust: doing so trains the user to
// re-confirm without reading, which is how a confirmation stops meaning
// anything.
func TestHashStableAcrossWhitespaceAndKeyOrder(t *testing.T) {
	reformatted := `[[hooks]]
matcher   =   "run_command"
event     =   "PreToolUse"


  [[hooks.handlers]]
  on_timeout = "block"
  timeout    = 10
  argv       = [ "./a.sh" , "--x" ]
  type       = "command"

  [[hooks.handlers]]
  argv       = ["./b.sh"]
  on_timeout = "block"
  type       = "command"
  timeout    = 10
`
	if hashOf(t, hashBase) != hashOf(t, reformatted) {
		t.Fatal("whitespace and key order must not change the definition hash")
	}
}

// Reordering handlers changes behaviour, so it must change the hash.
func TestHashUnstableAcrossHandlerReordering(t *testing.T) {
	swapped := `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./b.sh"]
  timeout = 10
  on_timeout = "block"

  [[hooks.handlers]]
  type = "command"
  argv = ["./a.sh", "--x"]
  timeout = 10
  on_timeout = "block"
`
	if hashOf(t, hashBase) == hashOf(t, swapped) {
		t.Fatal("reordering handlers must change the definition hash")
	}
}

func TestHashCoversEveryDefinitionField(t *testing.T) {
	base := hashOf(t, hashBase)
	variants := map[string]string{
		"argv value":      `[[hooks]]` + "\n" + `event = "PreToolUse"` + "\n" + `matcher = "run_command"` + "\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"--y\"]\n  timeout = 10\n  on_timeout = \"block\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./b.sh\"]\n  timeout = 10\n  on_timeout = \"block\"\n",
		"timeout":         `[[hooks]]` + "\n" + `event = "PreToolUse"` + "\n" + `matcher = "run_command"` + "\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"--x\"]\n  timeout = 11\n  on_timeout = \"block\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./b.sh\"]\n  timeout = 10\n  on_timeout = \"block\"\n",
		"on_timeout":      `[[hooks]]` + "\n" + `event = "PreToolUse"` + "\n" + `matcher = "run_command"` + "\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"--x\"]\n  timeout = 10\n  on_timeout = \"allow\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./b.sh\"]\n  timeout = 10\n  on_timeout = \"block\"\n",
		"matcher":         `[[hooks]]` + "\n" + `event = "PreToolUse"` + "\n" + `matcher = "write_file"` + "\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"--x\"]\n  timeout = 10\n  on_timeout = \"block\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./b.sh\"]\n  timeout = 10\n  on_timeout = \"block\"\n",
		"event":           `[[hooks]]` + "\n" + `event = "PostToolUse"` + "\n" + `matcher = "run_command"` + "\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"--x\"]\n  timeout = 10\n  on_timeout = \"block\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./b.sh\"]\n  timeout = 10\n  on_timeout = \"block\"\n",
		"handler removed": `[[hooks]]` + "\n" + `event = "PreToolUse"` + "\n" + `matcher = "run_command"` + "\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"--x\"]\n  timeout = 10\n  on_timeout = \"block\"\n",
	}
	for name, body := range variants {
		if got := hashOf(t, body); got == base {
			t.Errorf("changing the %s must change the definition hash", name)
		}
	}
}

// An argv split differently must not collide with the original: joining
// elements without a length or separator discipline is how ["a b"] and
// ["a","b"] become the same trusted definition.
func TestHashDistinguishesArgvSplits(t *testing.T) {
	one := "[[hooks]]\nevent = \"Stop\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh b\"]\n"
	two := "[[hooks]]\nevent = \"Stop\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\", \"b\"]\n"
	if hashOf(t, one) == hashOf(t, two) {
		t.Fatal("argv element boundaries must be part of the hash")
	}
}

// The hash must be independent of which file declared the group. Trust is keyed
// on the PAIR (source, hash); folding the path into the hash would make a
// definition moved between files look like a different definition.
func TestHashIsIndependentOfSourcePath(t *testing.T) {
	a, err := Parse([]byte(hashBase), "/home/u/.mivia/mivia.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b, err := Parse([]byte(hashBase), "/etc/mivia/managed.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a[0].Hash != b[0].Hash {
		t.Fatal("the definition hash must not depend on the declaring file path")
	}
}
