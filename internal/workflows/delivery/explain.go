package delivery

import "fmt"

// FormatOutcome renders the human-readable CLI summary of one delivery
// attempt. A successful or no_diff result is described from r; a refusal is
// permanent; any other error is a transient attempt failure that can be
// retried.
func FormatOutcome(r Result, err error) string {
	switch r.Status {
	case "succeeded":
		return fmt.Sprintf("PR created: %s (mode=%s)", r.URL, r.Mode)
	case "no_diff":
		return "no diff to publish; run completed without a PR"
	}
	if err != nil {
		if IsRefusal(err) {
			return "delivery refused: " + err.Error()
		}
		return "delivery attempt failed: " + err.Error() + "; retry with: mivia workflow deliver <runid> --allow-publish"
	}
	return "delivery finished with unknown status"
}
