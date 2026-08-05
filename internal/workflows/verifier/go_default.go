package verifier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// GoDefaultName is the first host-owned verifier profile.
const GoDefaultName = "go-default"

// CheckFunc runs fixed host checks for go-default. Tests may inject a fake.
type CheckFunc func(ctx context.Context, workDir string) ([]Check, error)

// GoDefault is the host-owned go-default verifier profile.
// It never accepts TOML-supplied command strings.
type GoDefault struct {
	checks CheckFunc
}

// NewGoDefault creates the go-default profile. When checks is nil, the fixed
// host implementation runs.
func NewGoDefault(checks CheckFunc) *GoDefault {
	if checks == nil {
		checks = fixedGoDefaultChecks
	}
	return &GoDefault{checks: checks}
}

func (g *GoDefault) Name() string { return GoDefaultName }

// Verify runs fixed host checks and returns schema-valid verification evidence.
func (g *GoDefault) Verify(ctx context.Context, req Request) (Result, error) {
	if g == nil || g.checks == nil {
		return Result{}, fmt.Errorf("go-default verifier is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	workDir := req.WorkDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Result{}, fmt.Errorf("resolve work dir: %w", err)
		}
		workDir = wd
	}
	checks, err := g.checks(ctx, workDir)
	if err != nil {
		return Result{}, err
	}
	if len(checks) == 0 {
		return Result{}, fmt.Errorf("go-default produced no checks")
	}
	status := "passed"
	for _, c := range checks {
		if c.Status == "failed" {
			status = "failed"
			break
		}
	}
	return Result{Status: status, Checks: checks}, nil
}

// fixedGoDefaultChecks is host-owned logic: presence of a Go module root and
// that the work directory is a real directory. No shell and no user argv.
func fixedGoDefaultChecks(ctx context.Context, workDir string) ([]Check, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return []Check{{Name: "workspace-dir", Status: "failed"}}, nil
	}
	if !info.IsDir() {
		return []Check{{Name: "workspace-dir", Status: "failed"}}, nil
	}
	checks := []Check{{Name: "workspace-dir", Status: "passed"}}
	modPath := filepath.Join(workDir, "go.mod")
	if st, err := os.Stat(modPath); err != nil || st.IsDir() {
		checks = append(checks, Check{Name: "go-module", Status: "failed"})
	} else {
		checks = append(checks, Check{Name: "go-module", Status: "passed"})
	}
	return checks, nil
}
