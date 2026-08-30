package cli

import (
	"context"

	"fmt"
	"io"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// runWhoami reports who the stored session belongs to, refreshing it first if
// it is near or past expiry.
func runWhoami(args []string) error {
	return runWhoamiWithIO(args, os.Stdout, os.Stderr)
}

func runWhoamiWithIO(args []string, stdout, stderr io.Writer) error {
	opts, err := parseWhoamiArgs(args)
	if err != nil {
		return err
	}

	serverURL := opts.serverURL
	if serverURL == "" {
		serverURL = miviaauth.ServerURLFromEnv()
	}

	client, err := miviaauth.NewClient(serverURL)
	if err != nil {
		return fmt.Errorf("whoami: %w", err)
	}
	service := miviaauth.NewService(client, config.UserAuthPath())

	result, err := service.Whoami(context.Background())
	if err != nil {
		// The session-lifecycle sentinels each carry their own next step --
		// ErrReauthRequired says to run `mivia login`, ErrRefreshBusy says to
		// retry, ErrSessionLost says the session is gone. Wrapping preserves
		// both the text and errors.Is; restating them here would be a second
		// copy to keep in sync.
		return fmt.Errorf("whoami: %w", err)
	}

	printIdentity(stdout, result)
	return nil
}

// printIdentity writes the identity block. Display name is omitted rather
// than shown blank when the account has none, which the schema allows.
func printIdentity(stdout io.Writer, result miviaauth.WhoamiResult) {
	id := result.Identity
	fmt.Fprintf(stdout, "Email:         %s\n", id.Email)
	if id.DisplayName != "" {
		fmt.Fprintf(stdout, "Display name:  %s\n", id.DisplayName)
	}
	fmt.Fprintf(stdout, "Organization:  %s\n", id.OrganizationID)
	fmt.Fprintf(stdout, "Role:          %s\n", id.Role)
	fmt.Fprintf(stdout, "Token expires: %s (%s)\n",
		result.ExpiresAt.Format(time.RFC3339), humanizeUntil(time.Until(result.ExpiresAt)))
}

// humanizeUntil renders a duration the way a person reads a countdown. A
// non-positive value is reported honestly rather than as a negative number:
// the access token can legitimately be expired at this point, because the
// refresh that would replace it is driven by the refresh token, not by this
// clock.
func humanizeUntil(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	d = d.Round(time.Minute)
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("in %dh %dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("in %dm", int(d.Minutes()))
}

// whoamiOptions holds the parsed `mivia whoami` flags.
type whoamiOptions struct {
	serverURL string
}

// parseWhoamiArgs parses whoami flags with flagValue, matching logout.go and
// the rest of this package's flag-parsing convention.
func parseWhoamiArgs(args []string) (whoamiOptions, error) {
	var opts whoamiOptions
	var err error

	var serverURLFound bool
	opts.serverURL, args, serverURLFound, err = flagValue(args, "--server-url")
	if err != nil {
		return opts, fmt.Errorf("whoami: %w", err)
	}
	if serverURLFound && opts.serverURL == "" {
		return opts, fmt.Errorf("whoami: --server-url must not be empty; omit the flag to use %s", miviaauth.ServerURLFromEnv())
	}

	if len(args) != 0 {
		if arg, ok := firstUnknownFlag(args); ok {
			return opts, fmt.Errorf("whoami: unknown flag %q", arg)
		}
		return opts, fmt.Errorf("whoami: unexpected argument %q", args[0])
	}
	return opts, nil
}
