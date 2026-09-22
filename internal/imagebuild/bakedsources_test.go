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
	It("CS-IMG-037: every source Dockerfile.tools COPYs from the build context is a baked source", func() {
		srcs := contextSources(repoFile("Dockerfile.tools"))
		Expect(srcs).To(ContainElements("scaffold", "scaffold-ralph", "container-context.md", "mcp-servers.json"),
			"the parser found Dockerfile.tools' COPY lines")
		for _, s := range srcs {
			Expect(s).NotTo(BeElementOf("", "."), "a whole-context COPY cannot be tracked as a baked source")
			covered := false
			for _, b := range imagebuild.BakedSources {
				if s == b || strings.HasPrefix(s, b+"/") {
					covered = true
					break
				}
			}
			Expect(covered).To(BeTrue(), "Dockerfile.tools COPY source %q is not in imagebuild.BakedSources (CS-IMG-004)", s)
		}
	})

	It("CS-IMG-048: the base Dockerfile COPYs nothing from the build context and carries no version stamp", func() {
		df := repoFile("Dockerfile")
		Expect(contextSources(df)).To(BeEmpty(),
			"a baked source in the base would rebuild it, and every child, on each commit; put it in Dockerfile.tools")
		Expect(df).NotTo(ContainSubstring("CLAUDE_SANDBOX_VERSION"))
		Expect(df).NotTo(MatchRegexp(`(?m)^FROM\s+golang`), "the Go build belongs to Dockerfile.tools")
		// The files arrive with the cap; the base only names them.
		Expect(df).To(MatchRegexp(`(?m)^ENTRYPOINT \["/opt/claude-sandbox/bin/entrypoint\.sh"\]\s*$`))
		Expect(df).To(MatchRegexp(`(?m)^ENV PATH="/opt/claude-sandbox/bin:\$PATH"\s*$`))
	})

	It("CS-IMG-048: the scaffold child Dockerfile says the sandbox binary is not there at build time", func() {
		ex := repoFile("scaffold/Dockerfile.example")
		Expect(ex).To(ContainSubstring("claude-sandbox-tools"))
		Expect(ex).To(MatchRegexp(`(?s)RUN step that invokes.*claude-sandbox`))
	})

	It("CS-IMG-049: Dockerfile.tools lays out the files the cap copies", func() {
		df := repoFile("Dockerfile.tools")
		for _, re := range []string{
			`(?m)^COPY --link --chmod=755 entrypoint\.sh /opt/claude-sandbox/bin/entrypoint\.sh\s*$`,
			`(?m)^COPY --link --chmod=755 --from=builder /out/claude-sandbox /opt/claude-sandbox/bin/claude-sandbox\s*$`,
			`(?m)^RUN ln -s /opt/claude-sandbox/bin/claude-sandbox /opt/claude-sandbox/bin/ralph\s*$`,
			`(?m)^COPY --link logstream/ /opt/claude-sandbox/logstream/\s*$`,
			`(?m)^COPY --link PROMPT_RALPH\.md /opt/claude-sandbox/PROMPT_RALPH\.md\s*$`,
			`(?m)^COPY --link --from=mcp /src/dist/index\.mjs /opt/claude-sandbox/mcp/discord-notify/dist/index\.mjs\s*$`,
			`(?m)^ARG CLAUDE_SANDBOX_VERSION=unknown\s*$`,
			`(?m)^RUN echo "\$CLAUDE_SANDBOX_VERSION" > /opt/claude-sandbox/version\s*$`,
			`(?m)^LABEL org\.opencontainers\.image\.revision=\$CLAUDE_SANDBOX_VERSION\s*$`,
		} {
			Expect(df).To(MatchRegexp(re))
		}
		// The MCP bundle is built in its own node stage from the COPYed source.
		Expect(df).To(MatchRegexp(`(?m)^FROM node:22\S* AS mcp\s*$`))
		Expect(df).To(MatchRegexp(`(?m)^COPY mcp/discord-notify/ \./\s*$`))
		// mcp-servers.json runs exactly the bundle the tools image ships.
		Expect(repoFile("mcp-servers.json")).To(ContainSubstring(`"/opt/claude-sandbox/mcp/discord-notify/dist/index.mjs"`))
	})

	It("CS-IMG-037: the parser skips multi-stage COPYs and joins continuations", func() {
		df := "FROM x AS b\n# COPY commented/ out/\nCOPY --link --chmod=755 a.sh \\\n  b/ /dst/\nCOPY --from=b /out/bin /bin\nADD ./c.txt /c\n"
		Expect(contextSources(df)).To(Equal([]string{"a.sh", "b", "c.txt"}))
	})

	It("CS-IMG-039: the mode-keeping sources are exactly the final stage's COPYs without --chmod", func() {
		var keep []string
		for _, c := range contextCopies(repoFile("Dockerfile.tools")) {
			if c.finalStage && !c.chmod {
				keep = append(keep, c.path)
			}
		}
		Expect(keep).To(ContainElement("logstream"), "the parser found the final stage's COPY lines")
		Expect(imagebuild.ModeBakedSources).To(ConsistOf(keep),
			"a baked source's mode reaches the image exactly when the final stage COPYs it without --chmod; "+
				"update imagebuild.ModeBakedSources to match Dockerfile.tools")
	})

	It("CS-IMG-039: the parser tracks --chmod and the final stage", func() {
		df := "FROM x AS b\nCOPY a.go ./\nFROM y\nCOPY --link --chmod=755 e.sh /e\nCOPY --link l/ /l/\n"
		Expect(contextCopies(df)).To(Equal([]copySource{
			{path: "a.go"}, {path: "e.sh", chmod: true, finalStage: true}, {path: "l", finalStage: true},
		}))
	})

	It("CS-IMG-040: each baked source is exactly a COPY source path, the level BuildKit follows a symlink at", func() {
		seen := map[string]bool{}
		var srcs []string
		for _, s := range contextSources(repoFile("Dockerfile.tools")) {
			if !seen[s] {
				seen[s] = true
				srcs = append(srcs, s)
			}
		}
		Expect(imagebuild.BakedSources).To(ConsistOf(srcs))
	})
})
