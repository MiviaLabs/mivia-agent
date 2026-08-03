package config

import (
	"bytes"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// modelKeyProbes gives every key of the closed model object a value that
// differs from the base entry below, so decoding an entry carrying the key can
// be compared against decoding one without it. A key that parses but lands in
// no field produces an identical struct, which is exactly the silent failure
// the round trip below exists to expose.
var modelKeyProbes = map[string]string{
	"name":                  `"drift-probe"`,
	"context_window_tokens": "424242",
	"max_output_tokens":     "4096",
	"reasoning":             `"high"`,
	"reasoning_efforts":     `["low", "high"]`,
	"reasoning_dialect":     `"thinking_effort"`,
}

// modelKeyProbeBase is the entry every probe is compared against. It carries
// the two required keys, so probing one of them overwrites its base value
// rather than repeating the key.
var modelKeyProbeBase = map[string]string{
	"name":                  `"base-model"`,
	"context_window_tokens": "128000",
}

func modelKeyProbeEntry(extra map[string]string) string {
	pairs := make(map[string]string, len(modelKeyProbeBase)+len(extra))
	maps.Copy(pairs, modelKeyProbeBase)
	maps.Copy(pairs, extra)
	lines := make([]string, 0, len(pairs))
	for _, key := range slices.Sorted(maps.Keys(pairs)) {
		lines = append(lines, key+" = "+pairs[key])
	}
	return strings.Join(lines, "\n") + "\n"
}

type modelKeyProbeDoc struct {
	Providers map[string]modelKeyProbeProvider `toml:"providers"`
}

type modelKeyProbeProvider struct {
	Models []ModelSpec `toml:"models"`
}

// modelSpecTOMLTags is the key set go-toml reflection can fill. An
// [[providers.x.models]] entry is decoded through these tags and never through
// UnmarshalTOML, so they are the second declaration of the model object's keys
// whether or not anyone maintains them as a list.
func modelSpecTOMLTags(t *testing.T) map[string]struct{} {
	t.Helper()
	specType := reflect.TypeOf(ModelSpec{})
	tags := make(map[string]struct{}, specType.NumField())
	for i := range specType.NumField() {
		field := specType.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
		if tag == "" || tag == "-" {
			t.Fatalf("field %s has no toml key, so no config can ever set it", field.Name)
		}
		tags[tag] = struct{}{}
	}
	return tags
}

// A key added to one of the two declarations and not the other is drift. The
// tag-without-decoder direction is loud on its own - the audit rejects the key
// - but a decoder entry whose tag was forgotten or mistyped is silent: the
// audit calls the key known, reflection finds no field, and the setting does
// nothing.
func TestModelKeyDeclarationsAgree(t *testing.T) {
	tags := modelSpecTOMLTags(t)
	for key := range modelFieldDecoders {
		if _, ok := tags[key]; !ok {
			t.Errorf("decoder key %q has no matching ModelSpec toml tag, so [[providers.x.models]] silently drops it", key)
		}
	}
	for tag := range tags {
		if !knownModelKey(tag) {
			t.Errorf("ModelSpec toml tag %q has no decoder entry, so the audit rejects a key the struct defines", tag)
		}
	}
	for key := range modelFieldDecoders {
		if _, ok := modelKeyProbes[key]; !ok {
			t.Errorf("key %q has no probe value, so no test proves it changes anything", key)
		}
	}
	for key := range modelKeyProbes {
		if !knownModelKey(key) {
			t.Errorf("probe key %q is not a key of the closed model object", key)
		}
	}
}

// Both TOML spellings of the same entry must decode to the same ModelSpec, and
// every key must actually move a field. Set equality between the two
// declarations is not enough: a one-character tag typo keeps both sets the same
// size while the array-of-tables spelling quietly loses the value.
func TestEveryModelKeyChangesTheDecodedSpecInBothSpellings(t *testing.T) {
	base := decodeModelKeyProbe(t, modelKeyProbeEntry(nil))
	for key, value := range modelKeyProbes {
		t.Run(key, func(t *testing.T) {
			got := decodeModelKeyProbe(t, modelKeyProbeEntry(map[string]string{key: value}))
			for spelling, spec := range got {
				if reflect.DeepEqual(spec, base[spelling]) {
					t.Errorf("%s: key %q decoded into nothing: %+v", spelling, key, spec)
				}
			}
			if !reflect.DeepEqual(got["inline table"], got["array of tables"]) {
				t.Errorf("key %q decodes differently per spelling: inline %+v, array of tables %+v",
					key, got["inline table"], got["array of tables"])
			}
		})
	}
}

// decodeModelKeyProbe runs one entry body through both TOML spellings. The
// decode is direct rather than through Load because the cross-field model rules
// would reject probe entries this check is not about; the drift being hunted
// happens before any of them run. The decoder is configured exactly as loadFile
// configures it, because UnmarshalTOML dispatch is what separates the two
// spellings and a plain toml.Unmarshal here would test neither of them.
func decodeModelKeyProbe(t *testing.T, entry string) map[string]ModelSpec {
	t.Helper()
	bodies := map[string]string{
		"array of tables": "[[providers.zai.models]]\n" + entry,
		"inline table":    "[providers.zai]\nmodels = [{ " + inlineEntry(entry) + " }]\n",
	}
	specs := make(map[string]ModelSpec, len(bodies))
	for spelling, body := range bodies {
		if err := auditModelKeys([]byte(body)); err != nil {
			t.Fatalf("%s: audit rejected a known key: %v", spelling, err)
		}
		var doc modelKeyProbeDoc
		dec := toml.NewDecoder(bytes.NewReader([]byte(body))).EnableUnmarshalerInterface()
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("%s: decode failed: %v", spelling, err)
		}
		models := doc.Providers["zai"].Models
		if len(models) != 1 {
			t.Fatalf("%s: want one model, got %d", spelling, len(models))
		}
		specs[spelling] = models[0]
	}
	return specs
}

func inlineEntry(entry string) string {
	return strings.Join(strings.Split(strings.TrimSuffix(entry, "\n"), "\n"), ", ")
}
