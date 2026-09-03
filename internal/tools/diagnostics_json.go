package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// parseJSONRows turns a decoded JSON document into rows. An array yields one
// row per element; an object yields its diagnostics/results/issues array, or
// the object itself when no such array exists.
func parseJSONRows(doc any, workspaceRoot string) []diagnosticsRow {
	var items []any
	switch v := doc.(type) {
	case []any:
		items = v
	case map[string]any:
		if arr, ok := jsonRowsArray(v); ok {
			items = arr
		} else {
			items = []any{v}
		}
	default:
		items = []any{v}
	}
	rows := make([]diagnosticsRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, parseJSONRow(item, workspaceRoot))
	}
	return rows
}

// jsonRowsArray returns the first diagnostics/results/issues array in obj. A
// present-but-wrong-typed key is skipped so a usable lower-priority alias
// still wins (audit finding P3); ok is false only when no alias holds a usable
// array.
func jsonRowsArray(obj map[string]any) ([]any, bool) {
	for _, key := range diagnosticsJSONRowKeys {
		if v, ok := jsonField(obj, key); ok {
			if arr, ok := v.([]any); ok {
				return arr, true
			}
		}
	}
	return nil, false
}

// parseJSONRow builds one row from one JSON element. An element without a
// usable message, or with a present-but-unusable position field, becomes a raw
// row that echoes the element (audit finding P2).
func parseJSONRow(item any, workspaceRoot string) diagnosticsRow {
	obj, ok := item.(map[string]any)
	if !ok {
		return diagnosticsRow{Severity: "info", Message: marshalJSON(item), Raw: true}
	}
	msg, hasMsg := jsonStringField(obj, "message", "msg", "text", "description")
	if !hasMsg || strings.TrimSpace(msg) == "" {
		return diagnosticsRow{Severity: "info", Message: marshalJSON(obj), Raw: true}
	}
	line, lineOK, lineSeen := jsonIntField(obj, "line")
	column, colOK, colSeen := jsonIntField(obj, "column", "col")
	if (lineSeen && !lineOK) || (colSeen && !colOK) {
		// A present-but-unusable position would silently become line/column 0
		// and read as a real finding: demote to a raw row instead.
		return diagnosticsRow{Severity: "info", Message: marshalJSON(obj), Raw: true}
	}
	severity := "info"
	if raw, ok := jsonStringField(obj, "severity", "level", "type"); ok {
		severity = normalizeSeverityToken(raw)
	}
	file, _ := jsonStringField(obj, "file", "path", "filename")
	return diagnosticsRow{
		Severity: severity,
		Message:  msg,
		File:     relativizeDiagnosticsPath(file, workspaceRoot),
		Line:     line,
		Column:   column,
	}
}

// jsonField returns the value of the first case-insensitive key match in obj.
// Duplicate case-variant keys resolve deterministically to the smallest key
// (audit finding P4): Go map iteration order must never decide a row.
func jsonField(obj map[string]any, key string) (any, bool) {
	best := ""
	var val any
	found := false
	for k, v := range obj {
		if strings.EqualFold(k, key) && (!found || k < best) {
			best, val, found = k, v, true
		}
	}
	return val, found
}

// jsonStringField returns the first usable string value among the aliases. A
// present-but-wrong-typed or empty value is skipped so a usable
// lower-priority alias still wins (audit finding P3).
func jsonStringField(obj map[string]any, aliases ...string) (string, bool) {
	for _, alias := range aliases {
		if v, ok := jsonField(obj, alias); ok {
			if s, ok := v.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// jsonIntField returns the first usable position number among the aliases. ok
// reports that a usable value was found; seen reports that at least one alias
// was present. A present-but-unusable position (wrong type, or a number that
// is negative, fractional, non-finite, or too large for int) is skipped so a
// usable lower-priority alias still wins (audit finding P3); when no alias
// yields a usable value, seen stays true and ok false, which is the caller's
// signal to demote the element to a raw row (audit finding P2).
func jsonIntField(obj map[string]any, aliases ...string) (val int, ok, seen bool) {
	for _, alias := range aliases {
		if v, present := jsonField(obj, alias); present {
			seen = true
			if f, isNum := v.(float64); isNum && usablePositionNumber(f) {
				return int(f), true, true
			}
		}
	}
	return 0, false, seen
}

// maxInt is the largest value int can hold on this platform: 2^63-1 on
// 64-bit int targets, 2^31-1 on 32-bit targets.
const maxInt = int(^uint(0) >> 1)

// usablePositionNumber reports whether a JSON number is a position the parser
// can trust: finite, non-negative, integral (no silent float->int truncation),
// and exactly representable as int (no overflow). Anything else - 1.5, -3, or
// 1e20 - would corrupt line/column into a value the producer never emitted
// (int(1e20) is implementation-defined overflow garbage), so the caller
// demotes the element to a raw row.
func usablePositionNumber(f float64) bool {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return false
	}
	if f != math.Trunc(f) {
		return false
	}
	// Reject the int boundary before converting. On targets whose float->int
	// conversion saturates (arm64, arm), int(2^63) is MaxInt64 and
	// float64(MaxInt64) rounds back to 2^63, so the round-trip check alone
	// passes for f == 2^63 (resp. 2^31 on 32-bit) and fabricates a position
	// the producer never emitted (finding C2-1). float64(maxInt)+1 is the
	// first float64 that overflows int, so this comparison demotes identically
	// on every platform.
	if f >= float64(maxInt)+1 {
		return false
	}
	// f is non-negative, integral, and below the int boundary. The round-trip
	// check is the final guard against silent truncation for any value the
	// platform converts non-exactly.
	return float64(int(f)) == f
}

// marshalJSON renders v compactly for a raw row echo.
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
