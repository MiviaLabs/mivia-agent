package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

const (
	// GoTestName identifies the fixed Go test verifier profile.
	GoTestName = "go-test"
	// GoVerifyName identifies the fixed Go quality verifier profile.
	GoVerifyName = "go-verify"
	// GoFinalName identifies the fixed final Go verifier profile.
	GoFinalName = "go-final"
)

type commandSpec struct {
	check   string
	program string
	args    []string
}

type commandRunner func(context.Context, string, string, ...string) error

type goProfile struct {
	name     string
	commands []commandSpec
	run      commandRunner
	policy   secretpath.Policy
}

func defaultGoProfiles(policy secretpath.Policy) []Profile {
	return []Profile{
		newGoProfile(GoTestName, []commandSpec{{check: "go-test", program: "go", args: []string{"test", "./..."}}}, nil, policy),
		newGoProfile(GoVerifyName, []commandSpec{
			{check: "go-vet", program: "go", args: []string{"vet", "./..."}},
			{check: "go-build", program: "go", args: []string{"build", "./cmd/mivia"}},
		}, nil, policy),
		newGoProfile(GoFinalName, []commandSpec{
			{check: "go-test-race", program: "go", args: []string{"test", "-race", "./..."}},
		}, nil, policy),
	}
}

func newGoProfile(name string, commands []commandSpec, run commandRunner, policy ...secretpath.Policy) *goProfile {
	p := goProfile{name: name, commands: commands, run: run}
	if len(policy) > 0 {
		p.policy = policy[0]
	}
	return &p
}

func (p *goProfile) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *goProfile) Verify(ctx context.Context, req Request) (Result, error) {
	if p == nil || len(p.commands) == 0 {
		return Result{}, fmt.Errorf("go verifier is not configured")
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
			runErr = runSandboxedCommand(ctx, workDir, req.ModuleBaseline, p.policy, command.program, command.args...)
		}
		if runErr != nil {
			check.Status = "failed"
			check.Class, check.Detail = failureEvidence(runErr)
			status = "failed"
		}
		checks = append(checks, check)
	}
	return Result{Status: status, Checks: checks}, nil
}

func failureEvidence(err error) (string, string) {
	var failure *commandFailure
	if errors.As(err, &failure) {
		return failure.class, failure.detail
	}
	return "source", "host verifier command failed"
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

func runFixedCommand(ctx context.Context, workDir, program string, args ...string) error {
	baseline, err := CaptureGoModuleBaseline(workDir)
	if err != nil {
		return err
	}
	return runSandboxedCommand(ctx, workDir, baseline, secretpath.Policy{}, program, args...)
}
