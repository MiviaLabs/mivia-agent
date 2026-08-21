// Package matcher provides closed structural transition matching for workflows.
// It evaluates attempt status plus exact scalar/enum output fields only.
// It is not an expression language: no regex, arithmetic, negation, or prose.
package definition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Decision is the durable explanation for one selected route.
type Decision struct {
	// TransitionIndex is the index of the selected transition in the full
	// workflow transition list (-1 when no single match is selected).
	TransitionIndex int
	ToStepID        string
	Loop            string
	MaxIterations   int
	// PartialTarget is the loop-exhaustion escape declared on the selected
	// transition, forwarded from Transition.
	PartialTarget string
	MatchDigest   string
	// Selected holds the exact status and output field values used for the match.
	Selected map[string]string
	// Outcome is "matched", "zero_match", "multi_match", or "invalid_output".
	Outcome      string
	DecisionJSON []byte
}

type matchCandidate struct {
	index  int
	tr     Transition
	sel    map[string]string
	digest string
}

// Match selects exactly one transition for fromStep against status and output.
// Zero-match and multi-match fail closed. Output leaves must be scalar or enum
// string values when compared; non-scalar values never match an output key.
func Match(fromStep, status string, output map[string]any, transitions []Transition) (Decision, error) {
	if strings.TrimSpace(fromStep) == "" {
		return failDecision("invalid_output", nil, fmt.Errorf("match from step is empty"))
	}
	if strings.TrimSpace(status) == "" {
		return failDecision("invalid_output", nil, fmt.Errorf("match status is empty"))
	}
	var hits []matchCandidate
	for i, tr := range transitions {
		if tr.From != fromStep {
			continue
		}
		sel, ok := matchOne(status, output, tr.Match)
		if !ok {
			continue
		}
		hits = append(hits, matchCandidate{
			index:  i,
			tr:     tr,
			sel:    sel,
			digest: matchDigest(tr.Match),
		})
	}
	switch len(hits) {
	case 0:
		err := fmt.Errorf("step %q: no matching transition (zero matches for status %q)", fromStep, status)
		d, _ := failDecision("zero_match", map[string]string{"status": status}, err)
		return d, err
	case 1:
		h := hits[0]
		d := Decision{
			TransitionIndex: h.index,
			ToStepID:        h.tr.To,
			Loop:            h.tr.Loop,
			MaxIterations:   h.tr.MaxIterations,
			PartialTarget:   h.tr.PartialTarget,
			MatchDigest:     h.digest,
			Selected:        h.sel,
			Outcome:         "matched",
		}
		raw, err := marshalDecision(d)
		if err != nil {
			return Decision{}, err
		}
		d.DecisionJSON = raw
		return d, nil
	default:
		// Prefer the strictly most specific transition when exactly one
		// candidate has more output keys than every other: a status-only
		// fallback plus a status+output special case is a natural workflow
		// shape, and the specific route must win on its own output. Genuine
		// ties (identical criteria, or same-size non-comparable criteria)
		// still fail closed.
		if best := selectMostSpecific(hits); best != nil {
			d := Decision{
				TransitionIndex: best.index,
				ToStepID:        best.tr.To,
				Loop:            best.tr.Loop,
				MaxIterations:   best.tr.MaxIterations,
				PartialTarget:   best.tr.PartialTarget,
				MatchDigest:     best.digest,
				Selected:        best.sel,
				Outcome:         "matched",
			}
			raw, err := marshalDecision(d)
			if err != nil {
				return Decision{}, err
			}
			d.DecisionJSON = raw
			return d, nil
		}
		indices := make([]string, len(hits))
		for i, h := range hits {
			indices[i] = strconv.Itoa(h.index)
		}
		sel := map[string]string{"status": status}
		d, _ := failDecision("multi_match", sel, fmt.Errorf("step %q: %d matching transitions (indices %s)", fromStep, len(hits), strings.Join(indices, ",")))
		return d, fmt.Errorf("step %q: multiple matching transitions (indices %s)", fromStep, strings.Join(indices, ","))
	}
}

func matchOne(status string, output map[string]any, criteria MatchCriteria) (map[string]string, bool) {
	if criteria.Status != status {
		return nil, false
	}
	selected := map[string]string{"status": status}
	if len(criteria.Output) == 0 {
		return selected, true
	}
	if output == nil {
		return nil, false
	}
	for key, want := range criteria.Output {
		raw, ok := output[key]
		if !ok {
			return nil, false
		}
		got, ok := scalarString(raw)
		if !ok {
			return nil, false
		}
		if got != want {
			return nil, false
		}
		selected[key] = got
	}
	return selected, true
}

// scalarString converts a JSON leaf to its canonical string form for equality.
// Objects, arrays, and null are not scalar and never match.
func scalarString(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case json.Number:
		return v.String(), true
	case float64, float32:
		// Canonicalize through encoding/json's own float serializer so the
		// matched string is exactly the form the engine writes for the value
		// (json.Marshal). strconv 'g' diverges from it for values in
		// [1e-6, 1e-4), [2^63, 1e21), and for single-digit negative exponents
		// ("1e-07" vs "1e-7"), so a criterion written as the value's JSON
		// form silently failed to match and the route fell to zero_match.
		// NaN and ±Inf are not JSON numbers: json.Marshal rejects them, so
		// they never match instead of stringifying to "NaN"/"+Inf".
		raw, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(raw), true
	case int:
		return strconv.Itoa(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(v), 10), true
	case map[string]any, []any:
		return "", false
	default:
		// Reject complex types; do not string-format structs/maps.
		return "", false
	}
}

func matchDigest(criteria MatchCriteria) string {
	// Canonical object: status + sorted output keys.
	type leaf struct {
		Status string            `json:"status"`
		Output map[string]string `json:"output,omitempty"`
	}
	out := leaf{Status: criteria.Status}
	if len(criteria.Output) > 0 {
		out.Output = make(map[string]string, len(criteria.Output))
		keys := make([]string, 0, len(criteria.Output))
		for k := range criteria.Output {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out.Output[k] = criteria.Output[k]
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func marshalDecision(d Decision) ([]byte, error) {
	payload := map[string]any{
		"transition_index": d.TransitionIndex,
		"to_step_id":       d.ToStepID,
		"match_digest":     d.MatchDigest,
		"selected":         d.Selected,
		"outcome":          d.Outcome,
	}
	if d.Loop != "" {
		payload["loop"] = d.Loop
		payload["max_iterations"] = d.MaxIterations
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal match decision: %w", err)
	}
	return raw, nil
}

func failDecision(outcome string, selected map[string]string, err error) (Decision, error) {
	d := Decision{
		TransitionIndex: -1,
		Selected:        selected,
		Outcome:         outcome,
	}
	payload := map[string]any{
		"transition_index": -1,
		"outcome":          outcome,
		"selected":         selected,
		"error":            err.Error(),
	}
	raw, mErr := json.Marshal(payload)
	if mErr == nil {
		d.DecisionJSON = raw
	}
	return d, err
}

// selectMostSpecific returns the single hit whose output-match criteria are a
// strict superset of every other hit's, or nil when no such candidate exists
// (identical criteria, same-size non-comparable criteria, or criteria that
// merely differ without one refining the other). Match uses it to let a
// status-only fallback plus a status+output special case route to the
// specific transition instead of failing with multi_match.
func selectMostSpecific(hits []matchCandidate) *matchCandidate {
	var best *matchCandidate
	for i := range hits {
		h := &hits[i]
		refinesAll := true
		for j := range hits {
			if i == j {
				continue
			}
			if !outputSupersets(h.tr.Match.Output, hits[j].tr.Match.Output) {
				refinesAll = false
				break
			}
		}
		if !refinesAll {
			continue
		}
		if best != nil {
			// Two distinct hits both refine every other hit: not unique.
			return nil
		}
		best = h
	}
	return best
}

// outputSupersets reports whether every key/value pair in other is also
// present in super, i.e. super's criteria are at least as specific as other's.
func outputSupersets(super, other map[string]string) bool {
	if len(super) < len(other) {
		return false
	}
	for k, v := range other {
		if super[k] != v {
			return false
		}
	}
	return len(super) > len(other) || len(other) == 0 && len(super) == 0
}
