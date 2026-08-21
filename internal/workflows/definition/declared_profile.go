package definition

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

// DeclaredCommand is one sandboxed command of a workspace-declared verifier
// profile. Program is a bare executable name; Args are argv verbatim, never a
// shell string.
type DeclaredCommand struct {
	Check   string
	Program string
	Args    []string
}

type commandSpec struct {
	check   string
	program string
	args    []string
}

type commandRunner func(context.Context, string, string, ...string) error

// declaredProfile runs an ordered list of sandboxed commands under one
// profile name. Workspace config declares these profiles; the host owns only
// the execution machinery, never the command catalogue.
type declaredProfile struct {
	name     string
	commands []commandSpec
	run      commandRunner
	policy   secretpath.Policy
}

// NewDeclaredProfile creates a verifier profile from workspace-declared
// commands. Each program must be a bare executable name; each check name must
// be non-empty. policy is optional; when present it excludes matching
// secret-like files from the sandboxed worktree copy.
func NewDeclaredProfile(name string, commands []DeclaredCommand, policy ...secretpath.Policy) (Profile, error) {
	if name == "" {
		return nil, fmt.Errorf("verifier profile name is empty")
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("verifier profile %q declares no commands", name)
	}
	specs := make([]commandSpec, 0, len(commands))
	for i, command := range commands {
		if command.Check == "" {
			return nil, fmt.Errorf("verifier profile %q command %d: check name is empty", name, i)
		}
		if !IsBareProgramName(command.Program) {
			return nil, fmt.Errorf("verifier profile %q command %d: program %q must be a bare executable name", name, i, command.Program)
		}
		specs = append(specs, commandSpec{check: command.Check, program: command.Program, args: append([]string(nil), command.Args...)})
	}
	return newDeclaredProfile(name, specs, nil, policy...), nil
}

func newDeclaredProfile(name string, commands []commandSpec, run commandRunner, policy ...secretpath.Policy) *declaredProfile {
	p := declaredProfile{name: name, commands: commands, run: run}
	if len(policy) > 0 {
		p.policy = policy[0]
	}
	return &p
}

func (p *declaredProfile) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *declaredProfile) Verify(ctx context.Context, req Request) (Result, error) {
	if p == nil || len(p.commands) == 0 {
		return Result{}, fmt.Errorf("verifier is not configured")
	}
	workDir, err := verifierWorkDir(req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	checks := make([]Check, 0, len(p.commands))
	status := "passed"
	for _, command := range p.commands {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		check := Check{Name: command.check, Status: "passed"}
		var runErr error
		if p.run != nil {
			runErr = p.run(ctx, workDir, command.program, command.args...)
		} else {
			runErr = runVerifierCommand(ctx, workDir, req.ModuleBaseline, p.policy, command.program, command.args...)
		}
		if runErr != nil {
			// A caller deadline or cancel is a run timeout, not a host
			// failure: surface the context error so the controller settles
			// the run as timed_out instead of fabricating a host failure.
			if ctxErr := contextErrorFromRun(runErr); ctxErr != nil {
				return Result{Status: status, Checks: checks}, ctxErr
			}
			check.Status = "failed"
			check.Class, check.Detail, check.Failures = failureEvidence(runErr)
			status = "failed"
		}
		checks = append(checks, check)
	}
	return Result{Status: status, Checks: checks}, nil
}

func failureEvidence(err error) (string, string, []string) {
	var failure *commandFailure
	if errors.As(err, &failure) {
		return failure.class, failure.detail, failure.failures
	}
	return "source", "host verifier command failed", nil
}

func runFixedCommand(ctx context.Context, workDir, program string, args ...string) error {
	baseline, err := CaptureGoModuleBaseline(workDir)
	if err != nil {
		return err
	}
	return runVerifierCommand(ctx, workDir, baseline, secretpath.Policy{}, program, args...)
}

func verifierWorkDir(workDir string) (string, error) {
	if workDir != "" {
		return workDir, nil
	}
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}
	return workDir, nil
}
