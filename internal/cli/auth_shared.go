package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// resolvePasswordInput resolves a password in priority order: passwordStdin
// (read from stdin up to the first newline or EOF) then, if stdin is a real
// terminal, an interactive term.ReadPassword prompt (echoing "Password: " to
// stdout first, matching setup.go); otherwise it returns a clear error asking
// for --password-stdin. Network calls must never be attempted before this
// resolves. Shared by login.go and register.go, which need identical
// password-handling rules.
//
// The interactive term.ReadPassword branch requires a real TTY on stdin and
// is not exercised by the unit test suite, matching the same accepted gap in
// setup.go's identical pattern.
func resolvePasswordInput(passwordStdin bool, stdout io.Writer, stdin io.Reader) ([]byte, error) {
	if passwordStdin {
		return readPasswordFromStdin(stdin)
	}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stdout, "Password: ")
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stdout)
		if err != nil {
			return nil, fmt.Errorf("read the password: %w", err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("no password source; pass --password-stdin")
}

// readPasswordFromStdin reads the entire password up to the first newline or
// EOF, trimming a single trailing newline.
func readPasswordFromStdin(stdin io.Reader) ([]byte, error) {
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read the password from stdin: %w", err)
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return []byte(line), nil
}

// rejectPlainPasswordFlag filters --password-stdin (a boolean flag, handled
// with the manual filter-loop convention used for --force in
// workflow_run.go) out of args and reports whether it was present. A bare
// --password (or --password=...) is rejected outright: accepting a plaintext
// password on the command line would expose it in shell history and process
// listings. Shared by login.go's and register.go's argument parsers.
func rejectPlainPasswordFlag(args []string) ([]string, bool, error) {
	var passwordStdin bool
	filtered := args[:0]
	for _, arg := range args {
		switch {
		case arg == "--password-stdin":
			passwordStdin = true
		case arg == "--password" || strings.HasPrefix(arg, "--password="):
			return nil, false, fmt.Errorf("--password is not supported (it would leak into shell history and process listings); use --password-stdin instead")
		default:
			filtered = append(filtered, arg)
		}
	}
	return filtered, passwordStdin, nil
}
