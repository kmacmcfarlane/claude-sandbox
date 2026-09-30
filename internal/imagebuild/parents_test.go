package imagebuild_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// fromParents parses a Dockerfile and returns, in order and without
// duplicates, every FROM image that names neither an earlier stage nor a
// local sandbox image (claude-sandbox*).
func fromParents(dockerfile string) []string {
	stages := map[string]bool{}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(dockerfile, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 || strings.ToUpper(f[0]) != "FROM" {
			continue
		}
		args := f[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			args = args[1:] // --platform=...
		}
		Expect(args).NotTo(BeEmpty(), line)
		image := args[0]
		if !stages[strings.ToLower(image)] && !strings.HasPrefix(image, "claude-sandbox") && !seen[image] {
			seen[image] = true
			out = append(out, image)
		}
		// A stage name is usable from the NEXT FROM on.
		if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
			stages[strings.ToLower(args[2])] = true
		}
	}
	return out
}

var _ = Describe("external parents (CS-IMG-055)", func() {
	It("CS-IMG-055: ExternalParents is exactly the registry FROMs of the base, tools and CLI Dockerfiles", func() {
		Expect(imagebuild.ExternalParents).To(HaveLen(3))
		for _, name := range []string{imagebuild.BaseDockerfile, imagebuild.ToolsDockerfile, imagebuild.CLIDockerfile} {
			Expect(imagebuild.ExternalParents).To(HaveKey(name))
			Expect(imagebuild.ExternalParents[name]).To(Equal(fromParents(repoFile(name))), name)
		}
	})

	It("CS-IMG-055: the parser skips earlier stages and local sandbox images", func() {
		Expect(fromParents("FROM golang:1 AS b\nFROM --platform=linux/amd64 b\nFROM claude-sandbox\nFROM debian:x\nFROM golang:1\n")).
			To(Equal([]string{"golang:1", "debian:x"}))
	})

	It("CS-IMG-055: no launcher source passes BuildKit's --pull to a build", func() {
		for _, dir := range []string{".", filepath.Join("..", "..", "cmd", "claude-sandbox")} {
			entries, err := os.ReadDir(dir)
			Expect(err).NotTo(HaveOccurred())
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(raw)).NotTo(ContainSubstring(`"--pull"`), e.Name())
			}
		}
	})
})
