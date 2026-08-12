package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// runLogin authenticates against the go-mivia auth endpoints and persists
// the resulting session locally.
func runLogin(args []string) error {
	return runLoginWithIO(args, os.Stdout, os.Stderr, os.Stdin)
}

func runLoginWithIO(args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	opts, err := parseLoginArgs(args)
	if err != nil {
		return err
	}

	password, err := resolvePasswordInput(opts.passwordStdin, stdout, stdin)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	serverURL := opts.serverURL
	if serverURL == "" {
		serverURL = miviaauth.ServerURLFromEnv()
	}

	client, err := miviaauth.NewClient(serverURL)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	service := miviaauth.NewService(client, config.UserAuthPath())

	if err := service.Login(context.Background(), opts.email, password); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	fmt.Fprintf(stdout, "Logged in as %s.\n", opts.email)
	return nil
}

// loginOptions holds the parsed `mivia login` flags.
type loginOptions struct {
	email         string
	serverURL     string
	passwordStdin bool
}

// parseLoginArgs parses login flags with flagValue, matching the convention
// used by chat/workflow commands elsewhere in this package. --password-stdin
// is a boolean flag, handled with the manual filter-loop convention used for
// --force in workflow_run.go. A bare --password (or --password=...) is
// rejected outright: accepting a plaintext password on the command line
// would expose it in shell history and process listings.
func parseLoginArgs(args []string) (loginOptions, error) {
	var opts loginOptions
	var err error

	opts.email, args, _, err = flagValue(args, "--email")
	if err != nil {
		return opts, fmt.Errorf("login: %w", err)
	}
	opts.email = strings.TrimSpace(opts.email)

	var serverURLFound bool
	opts.serverURL, args, serverURLFound, err = flagValue(args, "--server-url")
	if err != nil {
		return opts, fmt.Errorf("login: %w", err)
	}
	if serverURLFound && opts.serverURL == "" {
		return opts, fmt.Errorf("login: --server-url must not be empty; omit the flag to use %s", miviaauth.ServerURLFromEnv())
	}

	args, opts.passwordStdin, err = rejectPlainPasswordFlag(args)
	if err != nil {
		return opts, fmt.Errorf("login: %w", err)
	}

	if arg, ok := firstUnknownFlag(args); ok {
		return opts, fmt.Errorf("login: unknown flag %q", arg)
	}
	if len(args) != 0 {
		return opts, fmt.Errorf("login: unexpected argument %q", args[0])
	}

	if opts.email == "" {
		return opts, fmt.Errorf("login: --email is required")
	}
	return opts, nil
}

// firstUnknownFlag reports the first remaining token that looks like a
// flag, so it can be rejected with an "unknown flag" message rather than
// falling through to the generic "unexpected argument" message.
func firstUnknownFlag(args []string) (string, bool) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return arg, true
		}
	}
	return "", false
}
