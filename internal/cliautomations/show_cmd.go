package cliautomations

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// runShowCommand implements `mivia automations show <id> [--workspace
// dir] [--config path]`.
func runShowCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("automations show: expected exactly one automation id")
	}
	id := rest[0]
	svc, _, cleanup, err := buildService(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	var found *ports.Automation
	for _, a := range svc.Automations() {
		if a.ID == id {
			v := a
			found = &v
			break
		}
	}
	if found == nil {
		return fmt.Errorf("automations show: %q not found", id)
	}
	printAutomationDetail(*found, svc.Runs(id, 20))
	return nil
}
