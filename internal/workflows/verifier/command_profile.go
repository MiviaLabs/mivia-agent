package verifier

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

// bareProgramNameRegex matches a bare executable name that the generic command
// verifier resolves from the trusted system directories (/usr/bin, /bin)
// inside the sandbox. No slashes, no dot-only names, no shell metacharacters:
// args are always passed as argv verbatim, never through a shell.
var bareProgramNameRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// IsBareProgramName reports whether program is a safe bare executable name.
// The generic command verifier and the workflow definition validator share
// this rule so a TOML-declared command can never name a path or a shell.
func IsBareProgramName(program string) bool {
	return program != "." && program != ".." && bareProgramNameRegex.MatchString(program)
}

// CommandProfile runs one sandboxed system command declared by an
// evidence_gate step. The program is a bare executable name resolved from the
// trusted system directories; args are argv passed verbatim (never a shell
// string). The sandbox isolates every run: a copied worktree without secrets,
// no network, no host home, an empty environment, and a fixed PATH. A
// workflow file can therefore declare its project's own final gate without
// widening the host's trust surface.
type CommandProfile struct {
	check   string
	program string
	args    []string
	run     commandRunner
	policy  secretpath.Policy
}

// NewCommandProfile creates a generic command verifier profile. policy is
// optional; when present it excludes matching secret-like files from the
// sandboxed worktree copy.
func NewCommandProfile(check, program string, args []string, policy ...secretpath.Policy) (Profile, error) {
	if strings.TrimSpace(check) == "" {
		return nil, fmt.Errorf("command verifier check name is empty")
	}
	if !IsBareProgramName(program) {
		return nil, fmt.Errorf("command verifier program %q must be a bare executable name", program)
	}
	p := CommandProfile{check: check, program: program, args: append([]string(nil), args...)}
	if len(policy) > 0 {
		p.policy = policy[0]
	}
	return &p, nil
}

// Name identifies the profile in diagnostics and duplicate registration.
func (p *CommandProfile) Name() string {
	if p == nil {
		return ""
	}
	return "command:" + p.program
}

// Verify runs the declared command in the sandbox and returns
// schema-valid verification evidence (verification-v1).
func (p *CommandProfile) Verify(ctx context.Context, req Request) (Result, error) {
	if p == nil || strings.TrimSpace(p.check) == "" || p.program == "" {
		return Result{}, fmt.Errorf("command verifier is not configured")
	}
	workDir, err := verifierWorkDir(req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	check := Check{Name: p.check, Status: "passed"}
	var runErr error
	if p.run != nil {
		runErr = p.run(ctx, workDir, p.program, p.args...)
	} else {
		runErr = runSandboxedCommand(ctx, workDir, req.ModuleBaseline, p.policy, p.program, p.args...)
	}
	if runErr != nil {
		check.Status = "failed"
		check.Class, check.Detail = failureEvidence(runErr)
		return Result{Status: "failed", Checks: []Check{check}}, nil
	}
	return Result{Status: "passed", Checks: []Check{check}}, nil
}
