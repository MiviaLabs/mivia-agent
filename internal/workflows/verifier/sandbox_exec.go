package verifier

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
