package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// runVerify submits an emailed verification code against the go-mivia auth
// endpoints and persists the resulting session locally, if one is issued.
func runVerify(args []string) error {
	return runVerifyWithIO(args, os.Stdout, os.Stderr)
}

func runVerifyWithIO(args []string, stdout, stderr io.Writer) error {
	opts, err := parseVerifyArgs(args)
	if err != nil {
		return err
	}

	serverURL := opts.serverURL
	if serverURL == "" {
		serverURL = miviaauth.ServerURLFromEnv()
	}

	client, err := miviaauth.NewClient(serverURL)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	service := miviaauth.NewService(client, config.UserAuthPath())

	err = service.Verify(context.Background(), opts.code)
	if err == nil {
		fmt.Fprintln(stdout, "Email verified. You are now logged in.")
		return nil
	}
	if errors.Is(err, miviaauth.ErrVerifiedNoSession) {
		fmt.Fprintln(stdout, "Email verified. Run `mivia login` to sign in.")
		return nil
	}

	var statusErr *miviaauth.StatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode == 400 {
		return fmt.Errorf("verify: the code is invalid, expired, or already used. Codes expire 30 minutes after the email is sent and can only be used once. There's currently no way to resend a verification email from the CLI -- register again with a different email, or contact support.")
	}
	return fmt.Errorf("verify: %w", err)
}

// verifyOptions holds the parsed `mivia verify` flags.
type verifyOptions struct {
	code      string
	serverURL string
}

// parseVerifyArgs parses verify flags with flagValue, mirroring
// parseLogoutArgs's conventions. The verification code is a single required
// positional argument, trimmed to defend against clipboard-pasted trailing
// whitespace/newlines. No --token flag is offered: `mivia verify` accepts
// only a bare code, not a full pasted verification link.
func parseVerifyArgs(args []string) (verifyOptions, error) {
	var opts verifyOptions
	var err error

	var serverURLFound bool
	opts.serverURL, args, serverURLFound, err = flagValue(args, "--server-url")
	if err != nil {
		return opts, fmt.Errorf("verify: %w", err)
	}
	if serverURLFound && opts.serverURL == "" {
		return opts, fmt.Errorf("verify: --server-url must not be empty; omit the flag to use %s", miviaauth.ServerURLFromEnv())
	}

	if arg, ok := firstUnknownFlag(args); ok {
		return opts, fmt.Errorf("verify: unknown flag %q", arg)
	}

	if len(args) == 0 {
		return opts, fmt.Errorf("verify: a verification code is required")
	}
	if len(args) > 1 {
		return opts, fmt.Errorf("verify: unexpected argument %q", args[1])
	}
	opts.code = strings.TrimSpace(args[0])
	if opts.code == "" {
		return opts, fmt.Errorf("verify: a verification code is required")
	}
	return opts, nil
}
