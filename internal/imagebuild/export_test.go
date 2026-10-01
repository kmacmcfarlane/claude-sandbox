package imagebuild

// Test-only exports for the external imagebuild_test package.

// PreSnapshotChildInputs is childInputs exactly as it was before the child
// Dockerfile was snapshotted (CS-IMG-074): it read the file at spec.Dockerfile.
// A test pins the snapshot form to this value, so the change rebuilds no image.
func PreSnapshotChildInputs(spec ChildSpec, baseID string) string {
	if baseID == "" {
		return ""
	}
	f, sum := newFingerprint("child")
	f.add(spec.Context)
	if !f.addFile(spec.Dockerfile, spec.Dockerfile) {
		return ""
	}
	f.add(baseID)
	return sum()
}

// BaseInputs, ToolsInputs and CLIInputs expose the repo-file fingerprints
// (CS-IMG-075).
func BaseInputs(repoRoot string) string          { return baseInputs(repoRoot) }
func ToolsInputs(repoRoot string) (string, bool) { return toolsInputs(repoRoot) }
func CLIInputs(repoRoot string) string           { return cliInputs(repoRoot) }
