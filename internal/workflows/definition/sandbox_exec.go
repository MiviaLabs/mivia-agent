package definition

import "fmt"

// validateVerifierProgram checks the safety invariants every verifier
// command must satisfy regardless of whether it runs sandboxed or directly:
// a "go" program must have pinned module inputs admitted before execution,
// and any other program must be a bare executable name, never a shell
// string. Shared by runSandboxedCommand and runDirectCommand so the two
// paths cannot drift apart on what they consider safe to run.
func validateVerifierProgram(goMode bool, baseline *GoModuleBaseline, program string) error {
	if goMode && (baseline == nil || len(baseline.GoMod) == 0) {
		return fmt.Errorf("workflow verifier module baseline is missing")
	}
	if !goMode && !IsBareProgramName(program) {
		return fmt.Errorf("verifier rejects non-bare program %q", program)
	}
	return nil
}

// resolveSandboxExecutable resolves the executable that runs inside the
// sandbox and, when the pinned Go module inputs are available, the
// toolchain used to provision the module cache on the host.
func resolveSandboxExecutable(goMode bool, baseline *GoModuleBaseline, program string) (exePath, toolchainPath, goRoot string, err error) {
	if goMode {
		toolchainPath, goRoot, err = verifierGoToolchain()
		if err != nil {
			return "", "", "", err
		}
		return toolchainPath, toolchainPath, goRoot, nil
	}
	exePath, err = trustedSystemExecutable(program)
	if err != nil {
		return "", "", "", err
	}
	if baseline != nil {
		toolchainPath, goRoot, err = verifierGoToolchain()
		if err != nil {
			return "", "", "", err
		}
	}
	return exePath, toolchainPath, goRoot, nil
}
