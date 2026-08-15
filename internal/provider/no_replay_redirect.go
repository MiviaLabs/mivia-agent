package provider

import (
	"fmt"
	"net/http"
)

func (c *OpenAICompat) Name() string { return c.name }

// ContextAccounting returns this client's declared context-billing profile
// (see ContextAccountingProfile), set once at construction from
// CompatOptions.ContextAccounting.
func (c *OpenAICompat) ContextAccounting() ContextAccountingProfile { return c.contextAccounting }

func checkNoReplayRedirect(req *http.Request, via []*http.Request) error {
	if providerReplayDisabled(req.Context()) {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}
