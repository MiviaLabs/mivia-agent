package cliautomations

import "fmt"

// runListCommand implements `mivia automations list [--workspace dir]
// [--config path]`.
func runListCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("automations list: unexpected arguments %v", rest)
	}
	svc, _, cleanup, err := buildService(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer cleanup()
	printAutomationList(svc.Automations())
	return nil
}
