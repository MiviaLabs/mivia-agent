package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"time"
)

// definitionHash is the canonical content hash trust is keyed on.
//
// It covers the NORMALISED group - event, matcher, and each handler's type,
// argv, timeout and on_timeout - never the raw TOML bytes. Reformatting a
// config or reordering keys within a table must not change it: a hash that
// moved on reformatting would revoke trust for a change that altered no
// behaviour, and training the user to re-confirm without reading is how a
// confirmation stops meaning anything. Reordering handlers DOES change
// behaviour, so it does change the hash.
//
// The declaring file path is deliberately excluded. Trust is keyed on the pair
// (source, hash); folding the path in as well would make one definition moved
// between files look like a different definition twice over.
//
// What it does NOT cover: the contents of the script at argv[0]. Editing the
// script body does not revoke trust. That is the same boundary Codex draws -
// the user confirmed "run this program on this event", and the program is their
// own file under their own version control - but it is a real limit on what a
// confirmation attests to, and /hooks and the docs state it rather than leaving
// the reader to assume otherwise.
func definitionHash(group Group) string {
	digest := sha256.New()
	writeField(digest, "event", string(group.Event))
	writeField(digest, "matcher", group.Matcher)
	writeCount(digest, "handlers", len(group.Handlers))
	for _, handler := range group.Handlers {
		writeField(digest, "type", handler.Type)
		writeCount(digest, "argv", len(handler.Argv))
		for _, arg := range handler.Argv {
			writeField(digest, "arg", arg)
		}
		writeField(digest, "timeout", strconv.FormatInt(int64(handler.Timeout/time.Second), 10))
		writeField(digest, "on_timeout", string(handler.OnTimeout))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// writeField length-prefixes every value. Concatenating values without a length
// discipline is how argv ["a b"] and ["a", "b"] become one trusted definition.
func writeField(digest hash.Hash, key, value string) {
	fmt.Fprintf(digest, "%s=%d:", key, len(value))
	digest.Write([]byte(value))
	digest.Write([]byte{0})
}

func writeCount(digest hash.Hash, key string, n int) {
	fmt.Fprintf(digest, "%s#%d\x00", key, n)
}
