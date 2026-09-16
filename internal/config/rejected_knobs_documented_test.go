package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// rejectedKnobPatterns finds the config keys this package refuses outright, by
// scraping the error strings that name them. Scraping rather than listing keeps
// the literal-key set self-maintaining: a new rejection written in one of the
// established `"key must not be negative"` / `"key must be >= 0"` forms joins
// the contract automatically, which a hand-maintained slice would not.
var rejectedKnobPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"(?:\[[a-z._]+\]: )?([a-z_]+) must not be negative`),
	regexp.MustCompile(`"([a-z_]+) must be >= 0`),
}

// dynamicRejectSites covers rejections whose key is interpolated into the
// format string, so no static regex can scrape the key names. Each entry pins
// the format string (the tripwire: if the wording drifts, the assertion on the
// format string itself fails and this list gets re-audited) and the keys it
// refuses. Sources: mcp.go validateResolvedMCPConfig, agents_parse.go
// ParseAgentFileTOML.
var dynamicRejectSites = []struct {
	file   string   // source file owning the rejection
	format string   // the interpolated format string, verbatim
	keys   []string // keys the loop refuses
	agent  bool     // true: key is authored in agent definition files (agent.md), not mivia.toml
}{
	{
		file:   "mcp.go",
		format: `"MCP %s must be positive"`,
		keys: []string{
			"startup_timeout_seconds",
			"max_servers",
			"max_tools_per_server",
			"max_tool_schema_bytes",
			"max_tool_description_bytes",
			"max_tool_result_bytes",
		},
	},
	{
		file:   "agents_parse.go",
		format: `"%s must be > 0 when set"`,
		keys:   []string{"timeout_seconds", "max_tokens"},
		// Agent-definition knobs are documented in the agent authoring
		// reference, not in mivia.toml (agent files are authored per agent,
		// alongside the definition itself).
		agent: true,
	},
}

// TestRejectedKnobsAreDocumentedInShippedExample pins that every config key the
// loader refuses is described in the operator-facing reference for the file it
// is authored in: mivia.toml keys in .mivia/mivia.toml.example, agent-definition
// keys in docs/product/agent.md.
//
// The gap this closes is specific, and it was reachable. .mivia/mivia.toml.example
// is the user-facing reference, and it documents a negative MEANING for several
// [subagents] keys - `max_depth = -1`, `max_fanout = -1`, `default_budget`
// negative = unlimited, `default_total_timeout_seconds` negative = off. Against
// those neighbours, -1 reads as this table's idiom for "unbounded". An operator
// who applies that idiom to a key that refuses negatives does not get one
// ignored setting: config.Load returns an error, so the whole config fails and
// the CLI will not start. A rejection the reference never mentions is therefore
// a trap, not a guardrail.
//
// The key must appear as an actual assignment line, live or commented out, not
// merely as a substring. A plain substring match cannot fail where it should:
// "timeout_seconds" occurs inside "stream_idle_timeout_seconds", so the [mcp]
// key of that name would pass on a match to an unrelated [provider] watchdog
// even with its own entry deleted. A gate that cannot fail is not a gate.
//
// Beyond that the check stays loose on purpose: it asserts the key is present
// and documented somewhere, never the comment wording, so a legitimate
// rewording does not fail it. TestShippedExampleConfigLoads covers the other
// direction, that the example's own values still load.
func TestRejectedKnobsAreDocumentedInShippedExample(t *testing.T) {
	knobs := scrapeRejectedKnobs(t)
	example := readRepoFile(t, "..", "..", ".mivia", "mivia.toml.example")
	agentDoc := readRepoFile(t, "..", "..", "docs", "product", "agent.md")

	var undocumented []string
	for knob, src := range knobs {
		if !documented(knob, src, example, agentDoc) {
			undocumented = append(undocumented, knob+" (rejected in "+src+")")
		}
	}
	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("config keys refused at load but absent from their operator-facing reference:\n  %s\n"+
			"A refused key that the reference never mentions fails the whole "+
			"config load with no warning the operator could have read first.",
			strings.Join(undocumented, "\n  "))
	}
}

// scrapeRejectedKnobs collects every refused key with the file that rejects it:
// literal-key errors via the scrape patterns, interpolated-name loops via the
// tripwired dynamicRejectSites list.
func scrapeRejectedKnobs(t *testing.T) map[string]string {
	t.Helper()
	knobs := map[string]string{} // key -> the file that rejects it
	sources, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range rejectedKnobPatterns {
			for _, m := range pattern.FindAllStringSubmatch(string(data), -1) {
				knobs[m[1]] = filepath.Base(src)
			}
		}
	}
	if len(knobs) == 0 {
		t.Fatal("found no rejected config keys; the scrape patterns have drifted from the error strings they read")
	}
	// Dynamic-name rejections: the format strings must still exist verbatim,
	// otherwise this list has drifted from the loops it documents.
	for _, site := range dynamicRejectSites {
		data, err := os.ReadFile(filepath.Join(".", site.file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), site.format) {
			t.Errorf("dynamic reject site in %s: format string %s no longer found; "+
				"re-audit dynamicRejectSites in rejected_knobs_documented_test.go",
				site.file, site.format)
		}
		for _, key := range site.keys {
			marker := ""
			if site.agent {
				marker = ":agent-authored"
			}
			knobs[key] = site.file + " (dynamic" + marker + ")"
		}
	}
	return knobs
}

// documented reports whether knob's operator-facing reference actually
// mentions it: agent-authored keys in docs/product/agent.md, everything else
// in .mivia/mivia.toml.example.
func documented(knob, src string, example, agentDoc []byte) bool {
	// Explicit classification only: a substring test like Contains(src,
	// "agent") would misroute load_subagents.go.
	inAgentRef := strings.HasPrefix(src, "agents_parse.go") || strings.Contains(src, ":agent-authored")
	doc := example
	if inAgentRef {
		doc = agentDoc
	}
	// Live or commented-out assignment of this exact key, not a substring of
	// a longer one. Backticks are allowed around the key so Markdown table
	// rows in the agent reference count.
	assigned := regexp.MustCompile("(?m)^[#\\s|]*`?" + regexp.QuoteMeta(knob) + "`?\\s*[=|]")
	return assigned.Match(doc)
}

func readRepoFile(t *testing.T, elem ...string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(elem...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
