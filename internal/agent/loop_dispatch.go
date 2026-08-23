package agent

import (
	"context"
	"errors"
	"fmt"
)

// runOnce is the flag-dispatched driver behind (*Loop).Run. opts.Backend
// picks the inner loop. The legacy branch is the unchanged pre-flag body,
// renamed for clarity; the sdk branch is a stub that fails closed until
// the completer wrapper, options adapter, and steer bridge land.
func (l *Loop) runOnce(ctx context.Context, userText string, opts Options) (string, error) {
	switch opts.Backend {
	case "", "legacy":
		return l.runOnceLegacy(ctx, userText, opts)
	case "sdk":
		return "", errSDKBackendUnwirened
	default:
		return "", fmt.Errorf("agent: unknown Backend %q (want %q or %q)", opts.Backend, "legacy", "sdk")
	}
}

// errSDKBackendUnwirened is the sentinel runOnce emits from the "sdk"
// branch. It is distinct from the SDK's own ErrNoCompleter /
// ErrMaxIterations so a test can assert "the flag reached the dispatcher"
// without conflating it with construction-time failures.
var errSDKBackendUnwirened = errors.New("agent: SDK backend not yet wired (B.2 #8 part 2 commits 2-4)")
