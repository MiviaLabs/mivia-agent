package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// zaiErrorEnvelope covers every shape z.ai uses to report a failure. The
// documented schema is a flat {"code":<int>,"message":<string>}, but the service
// also returns the code as a string and nests both fields under "error" in the
// OpenAI style. Error stays a json.RawMessage rather than a struct so a body
// where "error" is a bare string still decodes instead of failing wholesale.
type zaiErrorEnvelope struct {
	Choices json.RawMessage `json:"choices"`
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Error   json.RawMessage `json:"error"`
}

// zaiCodeMeanings explains z.ai's published API error codes.
//
// The text is ours, not the provider's. z.ai echoes request content back in its
// own message field — prompt text, and the API key on some auth failures — and
// rule 10 forbids putting either in an error, so the message is never forwarded.
// The numeric code carries no request content, which makes it the only thing
// worth surfacing: without it a caller sees a bare HTTP status and cannot tell an
// expired plan from an unknown model.
//
// Codes 1316-1321 are published as a single grouped row covering spend caps and
// balance limits, so they share one explanation.
var zaiCodeMeanings = map[int]string{
	1000: "authentication failed",
	1001: "no authentication header received",
	1003: "authentication token expired",
	1005: "two-factor authentication required",
	1113: "insufficient balance or no resource package — a GLM Coding Plan key must use the coding base_url (https://api.z.ai/api/coding/paas/v4)",
	1200: "provider-side API call error",
	1210: "invalid API parameter",
	1211: "unknown model",
	1212: "model does not support this call method",
	1213: "required parameter missing",
	1214: "parameter is invalid",
	1215: "conflicting parameters set together",
	1220: "permission denied for API access",
	1221: "API has been taken offline",
	1222: "API does not exist",
	1230: "provider-side API call process error",
	1234: "provider-side network error",
	1261: "prompt too long",
	1301: "unsafe or sensitive content detected",
	1302: "rate limit reached",
	1305: "service temporarily overloaded",
	1308: "usage limit reached for the current period",
	1309: "GLM Coding Plan package expired",
	1310: "weekly or monthly limit exhausted",
	1311: "subscription does not include access to this model",
	1313: "usage does not comply with the fair usage policy",
	1314: "enterprise package expired",
	1315: "API key is limited to enterprise coding scenarios",
	1316: "usage limit or spend cap reached",
	1317: "usage limit or spend cap reached",
	1318: "usage limit or spend cap reached",
	1319: "usage limit or spend cap reached",
	1320: "usage limit or spend cap reached",
	1321: "usage limit or spend cap reached",
}

// zaiTransient429Codes are the HTTP 429 codes a backoff can actually clear.
// Every other known 429 is a quota or plan state that holds for the rest of the
// billing period, so retrying only spends more requests and delays the error.
var zaiTransient429Codes = map[int]bool{1302: true, 1305: true}

// zaiErrorParser converts a z.ai response into an error, or nil when the
// response carries no failure. It reports the numeric code and a static
// explanation, never the provider's own message.
func zaiErrorParser(statusCode int, body []byte) error {
	var envelope zaiErrorEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		if statusCode != http.StatusOK {
			return fmt.Errorf("zai: provider error (HTTP %d)", statusCode)
		}
		return nil
	}
	// A 200 carrying choices is a completion, including every streamed chunk.
	if statusCode == http.StatusOK && len(envelope.Choices) != 0 {
		return nil
	}
	// The parser runs on every SSE chunk, so a code alone is not enough to
	// declare failure: there must also be a failing status or an error field.
	if statusCode == http.StatusOK && envelope.Message == "" && len(envelope.Error) == 0 {
		return nil
	}
	code, ok := zaiErrorCode(envelope)
	if !ok {
		return fmt.Errorf("zai: provider error (HTTP %d)", statusCode)
	}
	if meaning := zaiCodeMeanings[code]; meaning != "" {
		return fmt.Errorf("zai: provider error (HTTP %d, code %d: %s)", statusCode, code, meaning)
	}
	return fmt.Errorf("zai: provider error (HTTP %d, code %d)", statusCode, code)
}

// zaiNonRetryable reports whether an HTTP 429 is a permanent quota or plan
// state rather than a rate limit a backoff can clear. An unrecognised body
// keeps the shared status-code policy, so nothing that used to be retried
// silently stops being retried.
func zaiNonRetryable(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	var envelope zaiErrorEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	code, ok := zaiErrorCode(envelope)
	if !ok || zaiTransient429Codes[code] {
		return false
	}
	return zaiCodeMeanings[code] != ""
}

// zaiErrorCode extracts the numeric code from whichever field carries it.
func zaiErrorCode(envelope zaiErrorEnvelope) (int, bool) {
	if code, ok := decodeZAICode(envelope.Code); ok {
		return code, true
	}
	if len(envelope.Error) == 0 {
		return 0, false
	}
	var nested struct {
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal(envelope.Error, &nested) != nil {
		return 0, false
	}
	return decodeZAICode(nested.Code)
}

// decodeZAICode reads a code encoded as either a JSON number or a JSON string.
func decodeZAICode(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number, true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, false
	}
	return code, true
}
