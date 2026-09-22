package launch_test

// Spec: spec/launch.feature CS-LNCH-068. The notification hooks are no longer a
// launch-time injection: they ship in the tools image as a Claude Code managed
// settings drop-in, which the cap copies into every run image (CS-IMG-024,
// CS-IMG-048/049). These tests pin the repo artifacts that make that true —
// Dockerfile.tools' COPY, the fragment's shape, and its place in the
// baked-source set — since no docker build runs in unit tests. The cap's own
// COPY line is pinned in internal/imagebuild (CS-IMG-024).
//
// That managed hooks COMBINE with the host's user-scope hooks (rather than
// replacing them) is Claude Code behavior, not ours: documented at
// code.claude.com/docs/en/hooks and verified against Claude Code 2.1.277 in a
// throwaway container (plugin-persistence/01_implementation.md). It is not
// re-verified here because unit tests run no claude and no docker.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// repoFile reads a file at the repository root (the directory holding go.mod).
func repoFile(name string) []byte {
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		Expect(parent).NotTo(Equal(dir), "go.mod not found above the test directory")
		dir = parent
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	Expect(err).NotTo(HaveOccurred())
	return raw
}

var _ = Describe("notification hooks as managed settings", func() {
	It("CS-LNCH-068: Dockerfile.tools copies notification-hooks.json into a traversable managed-settings drop-in dir, mode 0644", func() {
		df := string(repoFile("Dockerfile.tools"))
		copyLine := regexp.MustCompile(`(?m)^COPY\b[^\n]*--chmod=644[^\n]*\snotification-hooks\.json\s+` + regexp.QuoteMeta(imagebuild.ManagedSettingsFile) + `\s*$`)
		Expect(copyLine.MatchString(df)).To(BeTrue(),
			"Dockerfile.tools must COPY --chmod=644 notification-hooks.json /etc/claude-code/managed-settings.d/10-claude-sandbox.json")
		// The parent dirs are made traversable in their own step, and the COPY
		// avoids --link, which would apply --chmod=644 to the dirs it creates.
		mk := regexp.MustCompile(`(?m)^RUN install -d -m 0755 /etc/claude-code /etc/claude-code/managed-settings\.d\s*$`)
		loc := mk.FindStringIndex(df)
		Expect(loc).NotTo(BeNil(), "Dockerfile.tools must create /etc/claude-code{,/managed-settings.d} 0755")
		Expect(loc[0]).To(BeNumerically("<", copyLine.FindStringIndex(df)[0]))
		Expect(copyLine.FindString(df)).NotTo(ContainSubstring("--link"))
		// Not managed-settings.json itself: a child image may ship its own.
		Expect(df).NotTo(MatchRegexp(`(?m)^COPY\b.*/etc/claude-code/managed-settings\.json`))
		// And the base no longer carries it: it rides the cap.
		Expect(string(repoFile("Dockerfile"))).NotTo(ContainSubstring("notification-hooks.json"))
	})

	It("CS-LNCH-068: notification-hooks.json is a JSON object whose only key is hooks", func() {
		var m map[string]json.RawMessage
		Expect(json.Unmarshal(repoFile("notification-hooks.json"), &m)).To(Succeed())
		Expect(m).To(HaveLen(1))
		Expect(m).To(HaveKey("hooks"))
		var hooks map[string]any
		Expect(json.Unmarshal(m["hooks"], &hooks)).To(Succeed())
		Expect(hooks).To(HaveKey("Notification"))
	})

	It("CS-LNCH-068: notification-hooks.json is a baked source, so editing it rebuilds the tools image (CS-IMG-004)", func() {
		Expect(imagebuild.BakedSources).To(ContainElement("notification-hooks.json"))
	})
})
