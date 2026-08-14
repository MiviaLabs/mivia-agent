package config

// Unknown-key enforcement for the closed model object, for the TOML spelling
// the decoder never sees.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2/unstable"
)

// auditModelKeys rejects a model entry carrying a key the closed model object
// does not define.
//
// ModelSpec.UnmarshalTOML already does this, but go-toml only dispatches
// unstable.Unmarshaler for an inline table — an [[providers.x.models]] entry
// never reaches it. Its keys are gone by the time normalizeModels sees
// []ModelSpec, so loadFile holding the raw bytes is the last place to audit
// both spellings.
//
// The audit reads KEY PATHS off the raw syntax tree rather than re-decoding
// into a Go shape: any value/table type permissive enough to absorb one
// legal document's shape rejects another's (e.g. an out-of-range integer vs.
// a nested table), and either mismatch reports a broken checker for the
// whole document instead of the actual typo. A key path has no value or
// table shape to disagree with.
//
// This uses go-toml's unstable package (already depended on by
// model_spec.go, documented there as version-fragile) — a signature change
// here is a compile error, not a silently-stopped check.
func auditModelKeys(data []byte) error {
	walk := modelKeyWalk{entries: make(map[string]int)}
	var parser unstable.Parser
	parser.Reset(data)
	for parser.NextExpression() {
		if err := walk.expression(parser.Expression()); err != nil {
			return err
		}
	}
	if err := parser.Error(); err != nil {
		// The strict decode of these same bytes has already succeeded, and it
		// runs this parser over the whole document, so a syntax error here
		// means the two disagree about what TOML is. An unauditable model
		// catalog must not pass as an audited one.
		return fmt.Errorf("model keys cannot be checked: %w", err)
	}
	return nil
}

// modelKeyWalk carries what a flat stream of expressions does not: the table
// header the following key-values hang off, and how many entries each
// provider's catalog has opened, which is the index a rejection reports.
type modelKeyWalk struct {
	prefix  []string
	entries map[string]int
}

func (w *modelKeyWalk) expression(node *unstable.Node) error {
	// The parser yields only these three kinds at expression level: comments
	// and whitespace never surface, so a header is the only other thing a
	// non-assignment can be.
	if node.Kind == unstable.KeyValue {
		return w.keyValue(w.path(nodeKeyParts(node)), node.Value())
	}
	return w.header(node)
}

// header opens a new table. A header reaching BELOW a model entry names a key
// of that entry as surely as an assignment does, so it is checked as one.
func (w *modelKeyWalk) header(node *unstable.Node) error {
	w.prefix = nodeKeyParts(node)
	provider, rest, ok := modelEntryKeyPath(w.prefix)
	if !ok {
		return nil
	}
	if len(rest) == 0 {
		// An array table opens the next entry; a [providers.x.models] table is
		// a single one, whatever the strict decode later makes of it.
		if node.Kind == unstable.ArrayTable {
			w.entries[provider]++
		} else {
			w.entries[provider] = 1
		}
		return nil
	}
	return w.entryKey(provider, w.index(provider), rest)
}

// keyValue checks one assignment, descending into inline syntax so that a
// catalog written as `providers.x = { models = [...] }` is audited the same as
// one written in headers.
func (w *modelKeyWalk) keyValue(path []string, value *unstable.Node) error {
	provider, rest, ok := modelEntryKeyPath(path)
	switch {
	case ok && len(rest) > 0:
		return w.entryKey(provider, w.index(provider), rest)
	case ok:
		return w.inlineModels(provider, value)
	case value.Kind == unstable.InlineTable && isProvidersPrefix(path):
		// Every child of an inline table is an assignment; the parser has no
		// other shape to put there.
		for child := value.Child(); child != nil; child = child.Next() {
			if err := w.keyValue(append(slices.Clone(path), nodeKeyParts(child)...), child.Value()); err != nil {
				return err
			}
		}
	}
	return nil
}

// inlineModels checks `models = [{ ... }]`. A member that is not an inline
// table carries no keys to audit and is the strict decode's to reject.
func (w *modelKeyWalk) inlineModels(provider string, value *unstable.Node) error {
	if value.Kind != unstable.Array {
		return nil
	}
	index := 0
	for item := value.Children(); item.Next(); {
		entry := item.Node()
		if entry != nil && entry.Kind == unstable.InlineTable {
			for child := entry.Child(); child != nil; child = child.Next() {
				if err := w.entryKey(provider, index, nodeKeyParts(child)); err != nil {
					return err
				}
			}
		}
		index++
	}
	return nil
}

// entryKey accepts exactly one key part naming a field of the closed model
// object. Anything longer addresses a nested table the object does not have.
func (w *modelKeyWalk) entryKey(provider string, index int, rest []string) error {
	if len(rest) == 1 && knownModelKey(rest[0]) {
		return nil
	}
	key := rest[0]
	if knownModelKey(key) {
		// Naming only the first part of `name.bogus` would report a key the
		// operator did write as the mistake.
		key = strings.Join(rest, ".")
	}
	// Both the provider name and the key are operator text that reaches the
	// terminal through cmd/mivia; quoting keeps an escape sequence in a TOML key
	// from recolouring the reader's screen.
	return fmt.Errorf("[providers.%s]: models[%d]: %w",
		strconv.Quote(provider), index, unknownModelKeyError(key))
}

// index is the entry the current keys belong to. Zero covers a key that
// addresses the catalog without any entry having been opened - a shape the
// strict decode rejects on its own, and one whose reported index must still be
// a real position.
func (w *modelKeyWalk) index(provider string) int {
	return max(0, w.entries[provider]-1)
}

func (w *modelKeyWalk) path(key []string) []string {
	return append(slices.Clone(w.prefix), key...)
}

// modelEntryKeyPath splits a key path at providers.<name>.models, returning
// whatever addresses a model entry below it. Everything outside that region is
// absent on purpose: the config file is multi-owner (hooks, for one, are
// decoded by other packages through their own partial views), so this must
// never behave like a schema for the whole document.
func modelEntryKeyPath(path []string) (provider string, rest []string, ok bool) {
	if len(path) < 3 || path[0] != "providers" || path[2] != "models" {
		return "", nil, false
	}
	return path[1], path[3:], true
}

// isProvidersPrefix reports whether a path stops short of a models list on the
// way to one, which is where an inline table may still be hiding entries.
func isProvidersPrefix(path []string) bool {
	return len(path) >= 1 && len(path) <= 2 && path[0] == "providers"
}

func nodeKeyParts(node *unstable.Node) []string {
	var parts []string
	// Next() reports whether the iterator points at a node, so the node it
	// then yields is always present.
	for key := node.Key(); key.Next(); {
		parts = append(parts, string(key.Node().Data))
	}
	return parts
}

// unknownModelKeyError is the single rejection message for a key outside the
// closed model object, so both spellings report a typo the same way. It lists
// the accepted keys because the operator's next question is always which one
// they meant.
func unknownModelKeyError(key string) error {
	return fmt.Errorf("unknown model key %s (known keys: %s)",
		strconv.Quote(key), knownModelKeysQuoted())
}

func knownModelKeysQuoted() string {
	keys := make([]string, 0, len(modelFieldDecoders))
	for key := range modelFieldDecoders {
		keys = append(keys, strconv.Quote(key))
	}
	slices.Sort(keys)
	return strings.Join(keys, ", ")
}
