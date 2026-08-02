package config

// Unknown-key enforcement for the closed model object, for the TOML spelling
// the decoder never sees.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// rawModelKeyDoc is a permissive view of the one region of the document the
// audit reads. Everything outside providers.*.models is absent on purpose: the
// config file is multi-owner (hooks, for one, are decoded by other packages
// through their own partial views), so this must never behave like a schema
// for the whole document.
type rawModelKeyDoc struct {
	Providers map[string]rawModelKeyProvider `toml:"providers"`
}

type rawModelKeyProvider struct {
	Models []map[string]rawModelKeyValue `toml:"models"`
}

// rawModelKeyValue is the audit's value side. The audit reads KEYS, and a view
// that decoded values would let a value the operator also got wrong - an
// integer past int64, say - fail the view before the key check runs, reporting
// a broken checker instead of the typo sitting in the same entry. Accepting the
// raw bytes of any node and discarding them keeps the diagnosis on the key.
type rawModelKeyValue struct{}

func (*rawModelKeyValue) UnmarshalText([]byte) error { return nil }

// auditModelKeys rejects a model entry carrying a key the closed model object
// does not define.
//
// ModelSpec.UnmarshalTOML already does this, but go-toml dispatches
// unstable.Unmarshaler from handleValue, which an [[providers.x.models]] entry
// never reaches - only an inline table does. The keys are therefore gone by the
// time normalizeModels sees []ModelSpec, and loadFile holding the raw bytes is
// the last place they exist. Re-decoding that one region into a permissive
// shape is what makes the guarantee hold for both spellings; validation that
// fires for one way of writing the same config is not validation.
func auditModelKeys(data []byte) error {
	var doc rawModelKeyDoc
	if err := toml.Unmarshal(data, &doc); err != nil {
		// The strict decode of the same bytes has already succeeded and this
		// view decodes no values, so the document parses and every scalar is
		// beside the point. What is left is a providers region shaped so this
		// view cannot reach the model entries at all - providers or models not
		// being the collections they must be - and an unauditable model catalog
		// must not pass as an audited one.
		return fmt.Errorf("model keys cannot be checked: %w", err)
	}
	providers := make([]string, 0, len(doc.Providers))
	for name := range doc.Providers {
		providers = append(providers, name)
	}
	// Sorted so a document with several offending entries always names the same
	// one; map order would make the reported error change between runs.
	slices.Sort(providers)
	for _, provider := range providers {
		if err := auditProviderModelKeys(provider, doc.Providers[provider].Models); err != nil {
			return err
		}
	}
	return nil
}

func auditProviderModelKeys(provider string, models []map[string]rawModelKeyValue) error {
	for i, entry := range models {
		keys := make([]string, 0, len(entry))
		for key := range entry {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if knownModelKey(key) {
				continue
			}
			// Both the provider name and the key are operator text that reaches
			// the terminal through cmd/mivia; quoting keeps an escape sequence
			// in a TOML key from recolouring the reader's screen.
			return fmt.Errorf("[providers.%s]: models[%d]: %w",
				strconv.Quote(provider), i, unknownModelKeyError(key))
		}
	}
	return nil
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
