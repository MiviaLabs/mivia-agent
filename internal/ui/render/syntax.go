package render

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// HighlightCode applies syntax highlighting to source code based on filename or language identifier.
// It returns a slice of styled lines. At TierASCII or TierNoTTY, ANSI escapes are omitted.
func HighlightCode(t theme.Theme, tier theme.Tier, filenameOrLang, code string) []string {
	if code == "" {
		return nil
	}
	if tier == theme.TierASCII || tier == theme.TierNoTTY {
		return strings.Split(strings.TrimRight(code, "\n"), "\n")
	}

	lexer := resolveLexer(filenameOrLang, code)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	styleName := chromaThemeFor(t, tier)
	style := chromastyles.Get(styleName)
	if style == nil {
		style = chromastyles.Fallback
	}

	formatterName := chromaFormatter(tier)
	formatter := formatters.Get(formatterName)
	if formatter == nil {
		formatter = formatters.Get("terminal16m")
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return strings.Split(strings.TrimRight(code, "\n"), "\n")
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return strings.Split(strings.TrimRight(code, "\n"), "\n")
	}

	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

func resolveLexer(filenameOrLang, code string) chroma.Lexer {
	trimmed := strings.TrimSpace(filenameOrLang)
	if trimmed != "" {
		// Try matching as a file path or extension first
		base := filepath.Base(trimmed)
		if l := lexers.Match(base); l != nil {
			return l
		}
		if l := lexers.Get(trimmed); l != nil {
			return l
		}
	}
	// Fallback to content analysis if available
	if len(code) > 0 {
		if l := lexers.Analyse(code); l != nil {
			return l
		}
	}
	return lexers.Fallback
}
