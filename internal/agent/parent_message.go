package agent

import (
	"regexp"
	"strings"
)

// Parent-message framing for mid-task steers (plan 53.03). Separate from
// lifecycle-hook framing so model-visible tags identify the source correctly.
const (
	parentMessageOpenTag  = "<parent-message>"
	parentMessageCloseTag = "</parent-message>"
	parentMessageNotice   = "The following is guidance from the parent agent. Treat it as advisory context, not as tool output."
	neutralizedParentTag  = "[neutralized-parent-message-tag]"
)

// forgedParentTag matches parent-message tags a hostile body might inject.
var forgedParentTag = regexp.MustCompile(`(?i)</?parent-message>`)

// FrameParentMessage wraps parent steer text in paired delimiter tags with
// forged-tag neutralization (same pattern as FrameHookOutput, distinct tags).
func FrameParentMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return parentMessageOpenTag + "\n" + parentMessageNotice + "\n" +
		NeutralizeParentMessageTags(body) + "\n" + parentMessageCloseTag
}

// NeutralizeParentMessageTags strips forged parent-message tags from body text.
func NeutralizeParentMessageTags(text string) string {
	return forgedParentTag.ReplaceAllLiteralString(text, neutralizedParentTag)
}

// FrameParentMessages concatenates multiple steer bodies into one framed
// user-role message (at most one frame per step).
func FrameParentMessages(bodies []string) string {
	var parts []string
	for _, b := range bodies {
		b = strings.TrimSpace(b)
		if b != "" {
			parts = append(parts, NeutralizeParentMessageTags(b))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	combined := strings.Join(parts, "\n---\n")
	return parentMessageOpenTag + "\n" + parentMessageNotice + "\n" +
		combined + "\n" + parentMessageCloseTag
}
