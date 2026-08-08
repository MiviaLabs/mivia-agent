package provider

import (
	"fmt"
	"net/http"
)

func checkNoReplayRedirect(req *http.Request, via []*http.Request) error {
	if providerReplayDisabled(req.Context()) {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}
