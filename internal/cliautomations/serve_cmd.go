package cliautomations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// serveSignalContext builds the context runServeCommand's scheduler loop
// runs under, cancelled by SIGINT/SIGTERM. A package var so a test can
// substitute a context it controls directly instead of sending the test
// process a real OS signal (unsafe: the default disposition of an
// unhandled SIGTERM is to terminate the whole test binary if the
// notify-context handler has not yet installed).
var serveSignalContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runServeCommand implements `mivia automations serve [--workspace
// dir] [--config path]`: runs the automation scheduler until
// interrupted (SIGINT/SIGTERM), which a user wires into OS
// cron/launchd/systemd if they want firing while the TUI is closed.
func runServeCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("automations serve: unexpected arguments %v", rest)
	}

	svc, _, cleanup, err := buildService(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, stop := serveSignalContext()
	defer stop()

	err = svc.Serve(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
