package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// passwordMinBytesHint tracks go-mivia's identity.PasswordMinBytes (12).
// This is a fast local check only -- the server remains authoritative and
// will reject anything this misses (e.g. if the server-side bound changes).
const passwordMinBytesHint = 12

// runRegister starts account creation against the go-mivia auth endpoints.
// Registration never returns a session; the account is unusable until the
// emailed verification code is submitted via `mivia verify`.
func runRegister(args []string) error {
	return runRegisterWithIO(args, os.Stdout, os.Stderr, os.Stdin)
}

func runRegisterWithIO(args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	opts, err := parseRegisterArgs(args)
	if err != nil {
		return err
	}

	password, err := resolvePasswordInput(opts.passwordStdin, stdout, stdin)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if len(password) < passwordMinBytesHint {
		return fmt.Errorf("register: password must be at least %d bytes", passwordMinBytesHint)
	}

	serverURL := opts.serverURL
	if serverURL == "" {
		serverURL = miviaauth.ServerURLFromEnv()
	}

	client, err := miviaauth.NewClient(serverURL)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	if err := client.Register(context.Background(), opts.email, password, opts.organizationName); err != nil {
		return registerRequestError(err, opts.email)
	}

	fmt.Fprintf(stdout, "Registration started for %s. Check your email for a verification code, then run `mivia verify <code>`.\n", opts.email)
	return nil
}

// registerRequestError classifies a failed Register call into a tailored
// message where go-mivia's status code gives the caller a clear next step;
// anything else is wrapped generically.
func registerRequestError(err error, email string) error {
	var statusErr *miviaauth.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case 409:
			return fmt.Errorf("register: %s is already registered (run `mivia login` if it's yours, or `mivia verify <code>` if you're still finishing verification)", email)
		case 429:
			return fmt.Errorf("register: rate limited, wait a few minutes and try again")
		}
	}
	return fmt.Errorf("register: %w", err)
}

// registerOptions holds the parsed `mivia register` flags.
type registerOptions struct {
	email            string
	organizationName string
	serverURL        string
	passwordStdin    bool
}

// parseRegisterArgs parses register flags with flagValue, mirroring
// parseLoginArgs's conventions. The organization name is trimmed and
// required, but its upper-bound length is left to the server (go-mivia's
// 100-rune limit is not duplicated here) so that bound can change server-side
// without this client silently drifting out of sync.
func parseRegisterArgs(args []string) (registerOptions, error) {
	var opts registerOptions
	var err error

	opts.email, args, _, err = flagValue(args, "--email")
	if err != nil {
		return opts, fmt.Errorf("register: %w", err)
	}
	opts.email = strings.TrimSpace(opts.email)

	opts.organizationName, args, _, err = flagValue(args, "--organization-name")
	if err != nil {
		return opts, fmt.Errorf("register: %w", err)
	}
	opts.organizationName = strings.TrimSpace(opts.organizationName)

	var serverURLFound bool
	opts.serverURL, args, serverURLFound, err = flagValue(args, "--server-url")
	if err != nil {
		return opts, fmt.Errorf("register: %w", err)
	}
	if serverURLFound && opts.serverURL == "" {
		return opts, fmt.Errorf("register: --server-url must not be empty; omit the flag to use %s", miviaauth.ServerURLFromEnv())
	}

	args, opts.passwordStdin, err = rejectPlainPasswordFlag(args)
	if err != nil {
		return opts, fmt.Errorf("register: %w", err)
	}

	if arg, ok := firstUnknownFlag(args); ok {
		return opts, fmt.Errorf("register: unknown flag %q", arg)
	}
	if len(args) != 0 {
		return opts, fmt.Errorf("register: unexpected argument %q", args[0])
	}

	if opts.email == "" {
		return opts, fmt.Errorf("register: --email is required")
	}
	if opts.organizationName == "" {
		return opts, fmt.Errorf("register: --organization-name is required")
	}
	return opts, nil
}
