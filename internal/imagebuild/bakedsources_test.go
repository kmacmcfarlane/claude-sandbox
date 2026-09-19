package imagebuild_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// copySource is one source path of a COPY/ADD that reads from the build
// context, normalized to repo-relative form without "./" or a trailing slash.
type copySource struct {
	path       string
	chmod      bool // the instruction carries --chmod
	finalStage bool // the instruction is in the last FROM stage
}

// contextSources parses a Dockerfile and returns the source paths of every
// COPY/ADD that reads from the build context (not --from a stage or image).
func contextSources(dockerfile string) []string {
	var out []string
	for _, c := range contextCopies(dockerfile) {
		out = append(out, c.path)
	}
	return out
}

// contextCopies is contextSources with each source's --chmod and stage.
func contextCopies(dockerfile string) []copySource {
	// Join backslash continuations into one logical instruction per line.
	var logical []string
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
		logical = append(logical, cur+t)
		cur = ""
	}
	lastFrom := -1
	for i, ins := range logical {
		if f := strings.Fields(ins); len(f) > 0 && strings.ToUpper(f[0]) == "FROM" {
			lastFrom = i
		}
	}
	var srcs []copySource
	for i, ins := range logical {
		f := strings.Fields(ins)
		if len(f) == 0 {
			continue
		}
		op := strings.ToUpper(f[0])
		if op != "COPY" && op != "ADD" {
			continue
		}
		var args []string
		fromStage, chmod := false, false
		for _, a := range f[1:] {
			if strings.HasPrefix(a, "--") {
				if strings.HasPrefix(a, "--from=") {
					fromStage = true
				}
				if strings.HasPrefix(a, "--chmod=") {
					chmod = true
				}
				continue
			}
			args = append(args, a)
		}
		if fromStage {
			continue
		}
		// The exec (JSON) form and heredocs would hide sources from this
		// parser; fail loudly so the test is extended rather than bypassed.
		Expect(ins).NotTo(MatchRegexp(`^\S+(\s+--\S+)*\s+[\[<]`), "unparsed COPY/ADD form: %s", ins)
		Expect(len(args)).To(BeNumerically(">=", 2), "COPY/ADD without a destination: %s", ins)
		for _, s := range args[:len(args)-1] {
			s = strings.TrimPrefix(s, "./")
			s = strings.TrimSuffix(s, "/")
			srcs = append(srcs, copySource{path: s, chmod: chmod, finalStage: i > lastFrom})
		}
	}
	return srcs
}

var _ = Describe("baked sources", func() {
	It("CS-IMG-037: every source the base Dockerfile COPYs from the build context is a baked source", func() {
		srcs := contextSources(repoFile("Dockerfile"))
		Expect(srcs).To(ContainElements("scaffold", "scaffold-ralph", "container-context.md", "mcp-servers.json"),
			"the parser found the Dockerfile's COPY lines")
		for _, s := range srcs {
			Expect(s).NotTo(BeElementOf("", "."), "a whole-context COPY cannot be tracked as a baked source")
			covered := false
			for _, b := range imagebuild.BakedSources {
				if s == b || strings.HasPrefix(s, b+"/") {
					covered = true
					break
				}
			}
			Expect(covered).To(BeTrue(), "Dockerfile COPY source %q is not in imagebuild.BakedSources (CS-IMG-004)", s)
		}
	})

	It("CS-IMG-037: the parser skips multi-stage COPYs and joins continuations", func() {
		df := "FROM x AS b\n# COPY commented/ out/\nCOPY --link --chmod=755 a.sh \\\n  b/ /dst/\nCOPY --from=b /out/bin /bin\nADD ./c.txt /c\n"
		Expect(contextSources(df)).To(Equal([]string{"a.sh", "b", "c.txt"}))
	})

	It("CS-IMG-039: the mode-keeping sources are exactly the final stage's COPYs without --chmod", func() {
		var keep []string
		for _, c := range contextCopies(repoFile("Dockerfile")) {
			if c.finalStage && !c.chmod {
				keep = append(keep, c.path)
			}
		}
		Expect(keep).To(ContainElement("logstream"), "the parser found the final stage's COPY lines")
		Expect(imagebuild.ModeBakedSources).To(ConsistOf(keep),
			"a baked source's mode reaches the image exactly when the final stage COPYs it without --chmod; "+
				"update imagebuild.ModeBakedSources to match the Dockerfile")
	})

	It("CS-IMG-039: the parser tracks --chmod and the final stage", func() {
		df := "FROM x AS b\nCOPY a.go ./\nFROM y\nCOPY --link --chmod=755 e.sh /e\nCOPY --link l/ /l/\n"
		Expect(contextCopies(df)).To(Equal([]copySource{
			{path: "a.go"}, {path: "e.sh", chmod: true, finalStage: true}, {path: "l", finalStage: true},
		}))
	})
})
