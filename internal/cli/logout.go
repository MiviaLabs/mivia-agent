package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// runLogout revokes the stored session server-side on a best-effort basis
// and deletes the local token file.
func runLogout(args []string) error {
	return runLogoutWithIO(args, os.Stdout, os.Stderr)
}

func runLogoutWithIO(args []string, stdout, stderr io.Writer) error {
	opts, err := parseLogoutArgs(args)
	if err != nil {
		return err
	}

	serverURL := opts.serverURL
	if serverURL == "" {
		serverURL = miviaauth.ServerURLFromEnv()
	}

	client, err := miviaauth.NewClient(serverURL)
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	service := miviaauth.NewService(client, config.UserAuthPath())

	outcome, err := service.Logout(context.Background())
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}

	fmt.Fprintln(stdout, "Logged out.")
	if !outcome.ServerRevoked && outcome.RevokeErr != nil {
		// Local logout succeeded, which is what the user asked for, but the
		// server-side session was not ended -- and its refresh token stays
		// valid for up to 30 days. Saying nothing would leave them believing
		// the session is gone everywhere.
		fmt.Fprintf(stderr, "Warning: the local session was removed, but the server was not reached to end it: %v\n", outcome.RevokeErr)
		fmt.Fprintln(stderr, "The session stays valid on the server until it expires. Run `mivia logout` again when you are back online, or end it from the web app.")
	}
	return nil
}

// logoutOptions holds the parsed `mivia logout` flags.
type logoutOptions struct {
	serverURL string
}

// parseLogoutArgs parses logout flags with flagValue, matching login.go and
// the rest of this package's flag-parsing convention.
func parseLogoutArgs(args []string) (logoutOptions, error) {
	var opts logoutOptions
	var err error

	var serverURLFound bool
	opts.serverURL, args, serverURLFound, err = flagValue(args, "--server-url")
	if err != nil {
		return opts, fmt.Errorf("logout: %w", err)
	}
	if serverURLFound && opts.serverURL == "" {
		return opts, fmt.Errorf("logout: --server-url must not be empty; omit the flag to use %s", miviaauth.ServerURLFromEnv())
	}

	if len(args) != 0 {
		if arg, ok := firstUnknownFlag(args); ok {
			return opts, fmt.Errorf("logout: unknown flag %q", arg)
		}
		return opts, fmt.Errorf("logout: unexpected argument %q", args[0])
	}
	return opts, nil
}
