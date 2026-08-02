package tools

import (
	"errors"
	"fmt"
	"io"
)

// The two Tavily-backed tools (`search`'s provider path and `extract`) return
// remote content. The dispatcher DESTROYS - never truncates - any tool result
// over its output backstop, and that backstop is derived from the result
// budgets tools declare. So an undeclared, unbounded remote read is not merely
// a memory hazard: a large honest result is replaced wholesale by
// {"error":"output budget exceeded","status":"failed"}.
//
// The fix is a bound, NOT a truncation. Content that is fetched reaches the
// model whole. One number - [tools] max_tavily_response_bytes - is enforced in
// two places and declared once:
//
//  1. the wire read, so the response body can never exceed it;
//  2. the COMPOSED result, because composition is not guaranteed to shrink the
//     body it came from. `search` emits a 4-byte bullet per result against a
//     3-byte empty JSON object, amplifying by up to 5/3, and its header
//     formats the model-supplied query with %q, which expands a byte to four.
//     Neither term is the fixed-size framing that the dispatcher's slack
//     allowance covers.
//
// Enforcing (2) as well as (1) makes ResultBudgetBytes() exactly true rather
// than true-modulo-framing, so the derived ceiling can never bind below an
// honest result. Both checks fail loudly, naming the bound and the key that
// raises it. Neither ever returns a partial document: a body cut mid-JSON does
// not decode, and a result cut mid-composition is a silent lie.

// defaultTavilyResponseBytes mirrors config.DefaultToolsConfig's value. It is
// duplicated rather than imported because package tools does not depend on
// package config; TestTavilyResponseBudgetDefaultMatchesConfig pins them
// together.
const defaultTavilyResponseBytes = 4 << 20

// errWebResponseBudget marks a refusal caused by the response bound. `search`
// falls back to free engines when Tavily fails, which would turn this refusal
// into a silent substitution of different results; callers use errors.Is to
// keep it loud.
var errWebResponseBudget = errors.New("web response budget exceeded")

// resolveWebResponseBudget defends against a tool constructed without a
// budget (direct struct literals in tests, older callers).
func resolveWebResponseBudget(limit int) int {
	if limit <= 0 {
		return defaultTavilyResponseBytes
	}
	return limit
}

// readWebResponse reads at most limit bytes of body and refuses anything
// larger. It reads limit+1 so "exceeded" is detected exactly, and it never
// returns the partial read: these bodies are JSON, so a document cut at the
// limit yields a decode error rather than usable content, and reporting the
// bound is the only honest outcome.
func readWebResponse(body io.Reader, limit int, endpoint string) ([]byte, error) {
	limit = resolveWebResponseBudget(limit)
	buf, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("tavily %s read: %w", endpoint, err)
	}
	if len(buf) > limit {
		return nil, fmt.Errorf("tavily %s: response body exceeds the %d-byte bound; raise [tools] max_tavily_response_bytes: %w",
			endpoint, limit, errWebResponseBudget)
	}
	return buf, nil
}

// guardWebResult returns out unchanged when it fits the declared budget and
// refuses otherwise. It does not truncate: the budget this tool declares to
// the dispatcher has to be true, and cutting the result to make it true would
// hand the model a document that claims to be whole.
func guardWebResult(out string, limit int, endpoint string) (string, error) {
	limit = resolveWebResponseBudget(limit)
	if len(out) > limit {
		return "", fmt.Errorf("tavily %s: composed result exceeds the %d-byte bound; raise [tools] max_tavily_response_bytes: %w",
			endpoint, limit, errWebResponseBudget)
	}
	return out, nil
}
