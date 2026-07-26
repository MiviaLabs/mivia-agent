// Package tools implements workspace-bound agent tools.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// searchTool provides unified search across local files, web, and URLs.
// Uses only stdlib — no external dependencies.
type searchTool struct {
	ws         *workspace.Root
	maxLocalKB int
	maxFetchKB int
	httpClient *http.Client
}

func (t *searchTool) Name() string { return "search" }
func (t *searchTool) Description() string {
	return "Unified search: scope=local (grep & glob files), scope=web (web search via DuckDuckGo), scope=url (fetch and read URL contents). All return text results."
}
func (t *searchTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"scope": map[string]any{
			"type":        "string",
			"description": "'local' for file search, 'web' for internet search, 'url' to fetch a URL",
			"enum":        []string{"local", "web", "url"},
		},
		"query": map[string]any{
			"type":        "string",
			"description": "Search query (for local/web) or path/pattern (for local)",
		},
		"path": map[string]any{
			"type":        "string",
			"description": "Directory to search in (local scope only, default '.')",
		},
		"glob": map[string]any{
			"type":        "string",
			"description": "Filename glob filter (local scope only, e.g. *.go, **/*.md)",
		},
		"url": map[string]any{
			"type":        "string",
			"description": "URL to fetch (url scope only)",
		},
		"max_results": map[string]any{
			"type":        "integer",
			"description": "Max results/candidates (default 15 for local, 8 for web)",
		},
	}, []string{"scope"})
}

var ddgRE = regexp.MustCompile(`(?is)<tr[^>]*>.*?</tr>`)
var linkCellRE = regexp.MustCompile(`(?is)<td[^>]*class="result-link"[^>]*>.*?<a\s+(?:[^>]*?\s+)?href="(.*?)"[^>]*>(.*?)</a>`)
var snippetCellRE = regexp.MustCompile(`(?is)<td[^>]*class="result-snippet"[^>]*>(.*?)</td>`)

func (t *searchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Scope      string `json:"scope"`
		Query      string `json:"query"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		URL        string `json:"url"`
		MaxResults int    `json:"max_results"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}

	switch in.Scope {
	case "local":
		return t.searchLocal(ctx, in)
	case "web":
		return t.searchWeb(ctx, in)
	case "url":
		return t.fetchURL(ctx, in)
	default:
		return "", fmt.Errorf("invalid scope %q: must be local, web, or url", in.Scope)
	}
}

// --- Local search ---

func (t *searchTool) searchLocal(ctx context.Context, in struct {
	Scope      string `json:"scope"`
	Query      string `json:"query"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	URL        string `json:"url"`
	MaxResults int    `json:"max_results"`
}) (string, error) {
	if in.Query == "" {
		return "", fmt.Errorf("query is required for local search")
	}
	if in.Path == "" {
		in.Path = "."
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 15
	}

	re, err := regexp.Compile(in.Query)
	if err != nil {
		return "", fmt.Errorf("invalid regexp query: %w", err)
	}

	root, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}

	var results []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Early exit if we have enough results.
		if len(results) >= in.MaxResults {
			return fmt.Errorf("max results")
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Glob filter on filename.
		if in.Glob != "" {
			ok, _ := filepath.Match(in.Glob, d.Name())
			if !ok {
				return nil
			}
		}
		rel := t.ws.Rel(path)
		if isSecretPath(rel) {
			return nil
		}

		// Check if filename matches first (fast path).
		if re.MatchString(d.Name()) {
			results = append(results, fmt.Sprintf("%s (filename match)", rel))
			if len(results) >= in.MaxResults {
				return fmt.Errorf("max results")
			}
		}

		// Check content (slower).
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()

		// Read first 8KB to check for binary.
		header := make([]byte, 8192)
		n, _ := f.Read(header)
		if !utf8.Valid(header[:n]) {
			return nil // skip binary
		}

		// Reset and read line by line.
		f.Seek(0, 0)
		sc := bufio.NewScanner(f)
		buf := make([]byte, t.maxLocalKB*1024)
		sc.Buffer(buf, t.maxLocalKB*1024)

		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if re.MatchString(line) {
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				results = append(results, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
				if len(results) >= in.MaxResults {
					return fmt.Errorf("max results")
				}
			}
		}
		return nil
	})
	if err != nil {
		// "max results" is our sentinel to stop walking — it's not an error.
		if err.Error() != "max results" && err != context.Canceled {
			return "", err
		}
	}
	if len(results) == 0 {
		return "no matches found", nil
	}
	out := strings.Join(results, "\n")
	if len(results) >= in.MaxResults {
		out += fmt.Sprintf("\n... truncated at %d results", in.MaxResults)
	}
	return out, nil
}

// --- Web search (DuckDuckGo Lite) ---

func (t *searchTool) searchWeb(ctx context.Context, in struct {
	Scope      string `json:"scope"`
	Query      string `json:"query"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	URL        string `json:"url"`
	MaxResults int    `json:"max_results"`
}) (string, error) {
	if in.Query == "" {
		return "", fmt.Errorf("query is required for web search")
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 8
	}

	u := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(in.Query))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", fmt.Errorf("web search request: %w", err)
	}
	req.Header.Set("User-Agent", "Mivia/1.0 (research agent)")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web search failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxFetchKB)*1024))
	if err != nil {
		return "", fmt.Errorf("web search read: %w", err)
	}

	html := string(body)
	results := parseDDGResults(html, in.MaxResults)
	if len(results) == 0 {
		return "no web results found", nil
	}
	return strings.Join(results, "\n"), nil
}

// parseDDGResults extracts search results from DuckDuckGo Lite HTML.
// The Lite page uses a simple table with alternating result-link and result-snippet rows.
func parseDDGResults(html string, max int) []string {
	var out []string
	rows := ddgRE.FindAllString(html, -1)
	var pendingTitle, pendingURL string

	for _, row := range rows {
		if m := linkCellRE.FindStringSubmatch(row); len(m) >= 3 {
			href := decodeHTMLEntities(strings.TrimSpace(m[1]))
			title := stripHTMLTags(strings.TrimSpace(m[2]))
			if pendingTitle == "" {
				pendingURL = href
				pendingTitle = title
			}
		}
		if m := snippetCellRE.FindStringSubmatch(row); len(m) >= 2 {
			snippet := stripHTMLTags(strings.TrimSpace(m[1]))
			if pendingTitle != "" {
				line := fmt.Sprintf("• %s", pendingTitle)
				if pendingURL != "" {
					line += fmt.Sprintf("\n  %s", pendingURL)
				}
				if snippet != "" {
					line += fmt.Sprintf("\n  %s", truncateUTF8(snippet, 150))
				}
				out = append(out, line)
				pendingTitle = ""
				pendingURL = ""
				if len(out) >= max {
					break
				}
			}
		}
	}
	return out
}

// --- URL fetch ---

func (t *searchTool) fetchURL(ctx context.Context, in struct {
	Scope      string `json:"scope"`
	Query      string `json:"query"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	URL        string `json:"url"`
	MaxResults int    `json:"max_results"`
}) (string, error) {
	if in.URL == "" {
		return "", fmt.Errorf("url is required for url scope")
	}

	// Validate and sanitize URL.
	parsed, err := url.Parse(in.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid URL: must be http or https")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
	if err != nil {
		return "", fmt.Errorf("fetch request: %w", err)
	}
	req.Header.Set("User-Agent", "Mivia/1.0 (research agent)")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	// Reject non-text content types.
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !isTextContentType(ct) {
		return "", fmt.Errorf("skipped non-text content: %s", ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxFetchKB)*1024))
	if err != nil {
		return "", fmt.Errorf("fetch read: %w", err)
	}

	text := stripHTMLTags(string(body))
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		return "(empty page)", nil
	}
	// Cap output at maxLocalKB.
	if len(text) > t.maxLocalKB*1024 {
		text = truncateUTF8(text, t.maxLocalKB*1024) + "\n... (content truncated)"
	}

	return fmt.Sprintf("URL: %s\nStatus: %s\n\n%s", in.URL, resp.Status, text), nil
}

// --- Helpers ---

func stripHTMLTags(s string) string {
	var out strings.Builder
	inTag := false
	inEntity := false
	var entity strings.Builder
	for _, r := range s {
		if inTag {
			if r == '>' {
				inTag = false
			}
			continue
		}
		if r == '<' {
			inTag = true
			continue
		}
		if r == '&' {
			inEntity = true
			entity.Reset()
			continue
		}
		if inEntity {
			if r == ';' {
				inEntity = false
				// Decode common entities.
				code := entity.String()
				switch code {
				case "amp":
					out.WriteRune('&')
				case "lt":
					out.WriteRune('<')
				case "gt":
					out.WriteRune('>')
				case "quot":
					out.WriteRune('"')
				case "nbsp":
					out.WriteRune(' ')
				default:
					if strings.HasPrefix(code, "#") {
						var num int
						fmt.Sscanf(code[1:], "%d", &num)
						if num > 0 && num < 0x10FFFF {
							out.WriteRune(rune(num))
						}
					} else {
						out.WriteRune('&')
						out.WriteString(code)
						out.WriteRune(';')
					}
				}
				continue
			}
			entity.WriteRune(r)
			continue
		}
		// Normalize whitespace: collapse runs of spaces, newlines to single space.
		if isWhitespace(r) {
			if out.Len() > 0 {
				last := out.String()[out.Len()-1]
				if last != ' ' && last != '\n' {
					out.WriteRune(' ')
				}
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func decodeHTMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#x27;", "'")
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Truncate at byte boundary for valid UTF-8.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func isTextContentType(ct string) bool {
	if ct == "" {
		return false
	}
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") ||
		strings.HasPrefix(ct, "application/json") ||
		strings.HasPrefix(ct, "application/xml") ||
		strings.HasPrefix(ct, "application/javascript") ||
		strings.HasPrefix(ct, "application/xhtml") ||
		strings.Contains(ct, "charset=utf") ||
		strings.Contains(ct, "charset=iso")
}

func isWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
}
