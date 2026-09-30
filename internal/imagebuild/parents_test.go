package imagebuild_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// logicalLines joins backslash continuations and drops blank and comment
// lines, one Dockerfile instruction per entry.
func logicalLines(dockerfile string) []string {
	var out []string
	cur := ""
	for _, line := range strings.Split(dockerfile, "\n") {
		t := strings.TrimSpace(line)
		if cur == "" && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			cur += strings.TrimSuffix(t, "\\") + " "
			continue
		}
		out = append(out, cur+t)
		cur = ""
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// fromParents parses a Dockerfile and returns, in order and without
// duplicates, every image reference — a FROM, a COPY --from=<ref> or a
// RUN --mount=...,from=<ref> — that names neither an earlier stage nor a
// local sandbox image (claude-sandbox*). A reference holding "$" fails: the
// launcher could not know which image a build-arg names.
func fromParents(dockerfile string) []string {
	stages := map[string]bool{}
	seen := map[string]bool{}
	var out []string
	add := func(ref, ins string) {
		Expect(ref).NotTo(ContainSubstring("$"), "unresolvable image reference in: "+ins)
		if ref == "" || stages[strings.ToLower(ref)] || strings.HasPrefix(ref, "claude-sandbox") || seen[ref] {
			return
		}
		// A numeric --from is a stage index, never an image.
		if strings.Trim(ref, "0123456789") == "" {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, ins := range logicalLines(dockerfile) {
		f := strings.Fields(ins)
		if len(f) == 0 {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "FROM":
			args := f[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:] // --platform=...
			}
			Expect(args).NotTo(BeEmpty(), ins)
			add(args[0], ins)
			// A stage name is usable from the NEXT instruction on.
			if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
				stages[strings.ToLower(args[2])] = true
			}
		case "COPY", "ADD":
			for _, a := range f[1:] {
				if !strings.HasPrefix(a, "--") {
					break
				}
				if ref, ok := strings.CutPrefix(a, "--from="); ok {
					add(ref, ins)
				}
			}
		case "RUN":
			for _, a := range f[1:] {
				if !strings.HasPrefix(a, "--") {
					break
				}
				spec, ok := strings.CutPrefix(a, "--mount=")
				if !ok {
					continue
				}
				for _, kv := range strings.Split(spec, ",") {
					if ref, ok := strings.CutPrefix(kv, "from="); ok {
						add(ref, ins)
					}
				}
			}
		}
	}
	return out
}

// pullLiteral finds "--pull" (any --pull* flag) inside a Go string literal;
// comments may name the flag to explain why it is not used.
var pullLiteral = regexp.MustCompile("[\"`][^\"`\\n]*--pull\\b")

var _ = Describe("external parents (CS-IMG-055)", func() {
	It("CS-IMG-055: ExternalParents is exactly the registry image references of the base, tools and CLI Dockerfiles", func() {
		Expect(imagebuild.ExternalParents).To(HaveLen(3))
		for _, name := range []string{imagebuild.BaseDockerfile, imagebuild.ToolsDockerfile, imagebuild.CLIDockerfile} {
			Expect(imagebuild.ExternalParents).To(HaveKey(name))
			Expect(imagebuild.ExternalParents[name]).To(Equal(fromParents(repoFile(name))), name)
		}
	})

	It("CS-IMG-055: the parser skips earlier stages and local sandbox images and reads COPY --from and RUN --mount from=", func() {
		df := "FROM golang:1 AS b\n" +
			"FROM --platform=linux/amd64 b\n" +
			"FROM claude-sandbox\n" +
			"COPY --link --from=b /x /x\n" +
			"COPY --from=claude-sandbox-tools /o /o\n" +
			"COPY --from=0 /y /y\n" +
			"COPY --chown=1:1 --from=alpine:3 /z /z\n" +
			"RUN --mount=type=cache,id=x,target=/c \\\n" +
			"    --mount=type=bind,from=busybox:1,source=/bin,target=/bb true\n" +
			"FROM debian:x\n" +
			"FROM golang:1\n"
		Expect(fromParents(df)).To(Equal([]string{"golang:1", "alpine:3", "busybox:1", "debian:x"}))
	})

	It("CS-IMG-055: the parser fails on an image reference holding $", func() {
		for _, df := range []string{
			"FROM debian:${TAG}\n",
			"FROM debian:x\nCOPY --from=$IMG /a /a\n",
			"FROM debian:x\nRUN --mount=type=bind,from=${IMG},target=/m true\n",
		} {
			failures := InterceptGomegaFailures(func() { fromParents(df) })
			Expect(failures).NotTo(BeEmpty(), df)
		}
	})

	It("CS-IMG-055: no launcher source passes BuildKit's --pull to a build", func() {
		var scanned int
		for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				scanned++
				Expect(pullLiteral.FindString(string(raw))).To(BeEmpty(), path)
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(scanned).To(BeNumerically(">", 10))
		Expect(pullLiteral.MatchString(`args := []string{"build", "--pull"}`)).To(BeTrue())
		Expect(pullLiteral.MatchString(`"--pull=always"`)).To(BeTrue())
		Expect(pullLiteral.MatchString(`// BuildKit's own --pull is never used`)).To(BeFalse())
	})
})
