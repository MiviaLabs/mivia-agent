// Package blockedpath detects when task text or agent output instructs a
// write to a workspace path that the host write-path policy blocklists for
// workflow agents.
//
// Workflow agents cannot write such paths (the write tools refuse), so any
// instruction that demands editing one is an admission-time or
// settle-time failure, never a repair-loop candidate. The detection is
// deliberately conservative: a path token plus a demand verb on the same line
// is treated as an instruction to write. A read-only mention (no demand verb)
// is not.
package blockedpath

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// demandVerbRe matches the demand verbs that, next to a blocklisted path
// token, indicate the text instructs writing that path. Kept small on
// purpose: prose that merely describes a path ("review .mivia/workflows and
// report") must not match, while "edit .mivia/workflows/bug-fix.toml to lower
// max_bytes" must.
var demandVerbRe = regexp.MustCompile(`(?i)\b(?:edit|change|modify|update|write|fix|apply|set|lower|raise|add|remove|rewrite|implement|create|delete|bump|cap|revert)\b`)

// nounWritePhrases strips the noun uses of "write" that must not count as the
// verb: "write access", "write path", "write permission", "write policy",
// "write-blocklisted". A line that only says a path is behind write access is
// describing a fact, not instructing a write.
var nounWritePhrases = regexp.MustCompile(`(?i)\bwrite[\s_-]+(?:access|path|permission|policy|blocklist|denylist|list)\b`)

// LineDemandsEdit reports whether one line of text both names path and
// instructs editing it: a whitespace-delimited token references the path and a
// demand verb is present. A token references the path only when the path
// appears at a token or "/" boundary and is followed by the end of the token
// or another "/": ".git" inside a URL ("raw.githubusercontent.com") or a
// sibling name (".gitignore", ".mivia/workflows-x") never matches, while a
// quoted or backticked reference ("`.mivia/workflows/bug-fix.toml`",
// "foo/.git/HEAD") does. Word boundaries keep verbs inside other words
// ("assets", "prefix") from matching, and each matched path token itself is
// stripped from the line before verb matching so a verb inside a file name
// ("fix" in "bug-fix.toml") never counts. Noun phrases ("write access") are
// stripped too. A "do not edit X" instruction still matches: the text
// instructs a write to a blocked path either way, and the caller should
// refuse or route the task to a host-owned process.
func LineDemandsEdit(line, blockedPath string) bool {
	blockedPath = normalizePath(blockedPath)
	if blockedPath == "" {
		return false
	}
	cleaned := line
	matched := false
	for _, token := range strings.Fields(line) {
		if tokenNamesPath(token, blockedPath) {
			matched = true
			cleaned = strings.Replace(cleaned, token, " ", 1)
		}
	}
	if !matched {
		return false
	}
	cleaned = nounWritePhrases.ReplaceAllString(cleaned, " ")
	return demandVerbRe.MatchString(cleaned)
}

// tokenNamesPath reports whether a whitespace-delimited token references
// blockedPath as a workspace path: the normalized path appears at the start of
// the token or right after a "/" and is followed by the end of the token or
// another "/". Wrapping punctuation (backticks, quotes, brackets) is trimmed
// from the start, and sentence punctuation (period, comma, closing brackets)
// from the end, so quoted references and sentence-final paths still match; a
// leading dot that is part of a dotfile name (.git, .mivia) is never trimmed.
// An entry embedded inside a longer word (".git" inside
// "raw.githubusercontent.com") or followed by a name character (".gitignore",
// ".mivia/workflows-x") never counts: such a token does not name the blocked
// path.
func tokenNamesPath(token, blockedPath string) bool {
	t := strings.TrimLeft(token, "`\"'([{</*")
	t = strings.TrimRight(t, "`\"'.,;:!?/)]}>*")
	s := "/" + t
	idx := strings.Index(s, "/"+blockedPath)
	if idx < 0 {
		return false
	}
	after := s[idx+len("/"+blockedPath):]
	return after == "" || strings.HasPrefix(after, "/")
}

// IsBlockedPath reports whether rel (a slash-separated workspace-relative
// path) falls under any blocklist entry. Matching is a cleaned prefix match:
// an entry ".mivia/workflows" blocks ".mivia/workflows" and
// ".mivia/workflows/bug-fix.toml" but not ".mivia/workflows-x/file". Entries
// and the checked path are normalized (dot-slash and trailing slashes
// stripped) before comparison. An empty blocklist blocks nothing.
//
// Kept in sync with internal/tools.isWritePathDenied, which enforces the same
// rule at the write-tool boundary.
func IsBlockedPath(rel string, blocklist []string) bool {
	if len(blocklist) == 0 {
		return false
	}
	rel = normalizePath(rel)
	if rel == "" {
		return false
	}
	for _, entry := range blocklist {
		entry = normalizePath(entry)
		if entry == "" {
			continue
		}
		if rel == entry || strings.HasPrefix(rel, entry+"/") {
			return true
		}
	}
	return false
}

// PathsDemandedInText returns the blocklisted paths that text instructs
// writing, one entry per matched blocklist entry (not per file), deduplicated
// and sorted for deterministic error messages.
func PathsDemandedInText(text string, blocklist []string) []string {
	if len(blocklist) == 0 {
		return nil
	}
	var demanded []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		for _, entry := range blocklist {
			if !seen[entry] && LineDemandsEdit(line, normalizePath(entry)) {
				seen[entry] = true
				demanded = append(demanded, normalizePath(entry))
			}
		}
	}
	sort.Strings(demanded)
	return demanded
}

// normalizePath cleans a workspace-relative slash path: strips a leading
// "./", collapses internal dot segments, and removes a trailing slash.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	p = strings.TrimSuffix(p, "/")
	return p
}
