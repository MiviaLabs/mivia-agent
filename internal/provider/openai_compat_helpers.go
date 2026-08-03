package provider

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// deadlineLabel names the deadline a timed-out provider read refers to: the
// per-request timeout when one was armed on the request, or the transport
// backstop (DefaultHTTPTimeout) when the request carried none. It is appended
// to read-error messages so an operator can tell which deadline fired (finding
// F2) without breaking errors.Is(err, context.DeadlineExceeded).
func deadlineLabel(timeout time.Duration) string {
	if timeout <= 0 {
		return "transport"
	}
	return timeout.String()
}

// httpError maps a non-OK provider response to a typed error, draining the
// remaining response body so the TCP connection can be reused by the HTTP
// transport. The caller will close via defer after this returns; without
// draining, Go's transport opens a new connection.
func (c *OpenAICompat) httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if c.errorParser != nil {
		if err := c.errorParser(resp.StatusCode, body); err != nil {
			return err
		}
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 64*1024)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: auth failed (HTTP %d) - check API key", c.name, resp.StatusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limited (HTTP 429)", c.name)
	default:
		return fmt.Errorf("%s: HTTP %d", c.name, resp.StatusCode)
	}
}
