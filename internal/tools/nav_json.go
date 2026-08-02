package tools

import "encoding/json"

// budgetedJSON marshals a result envelope and guarantees the returned string
// never exceeds maxBytes (when set).
//
// build(keep, truncated) must return the envelope holding the FIRST keep items
// of whatever variable-length part the result has - locations, symbols, source
// lines - with the truncation flag already applied. Marshaled size is
// monotonically non-decreasing in keep, so the largest fitting prefix is found
// by binary search: O(log n) marshals of the whole envelope rather than
// dropping one item at a time and re-marshaling the remainder each time
// (O(n^2), which measured in tens of seconds for a 10,000-item result - see
// find_references' marshalBudgeted, which this generalizes).
//
// fallback is returned only when the envelope does not fit even with zero
// items, i.e. when caller- or workspace-supplied text echoed into the result
// is itself oversized. It must be valid JSON and smaller than any realistic
// budget: a tool that cannot honor its declared bound returns a bounded
// nothing rather than oversized data.
func budgetedJSON(maxBytes, n int, build func(keep int, truncated bool) any, fallback string) string {
	data, err := json.Marshal(build(n, false))
	if err != nil {
		return fallback
	}
	if maxBytes <= 0 || len(data) <= maxBytes {
		return string(data)
	}

	lo, hi, best, bestData := 0, n, -1, ([]byte)(nil)
	for lo <= hi {
		mid := (lo + hi) / 2
		d, merr := json.Marshal(build(mid, true))
		if merr != nil {
			return fallback
		}
		if len(d) <= maxBytes {
			best, bestData = mid, d
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best >= 0 {
		return string(bestData)
	}
	return fallback
}
