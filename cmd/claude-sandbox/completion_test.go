package main

// Spec: spec/completion.feature (CS-COMP) — shell completion.
//
// Most scenarios drive the hidden __complete command end-to-end through
// MainWithEnv, which is exactly what a shell does on every keystroke: it is
// the only way to cover the routing, the DisableFlagParsing interplay, and
// cobra's own subcommand/flag completion in one pass.
//
// CS-COMP-022 and CS-COMP-023 are @manual: they are behavior of the
// bin/claude-sandbox bash shim (serve completions from the existing binary,
// never build) and are covered by the manual smoke checklist.

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// completionResult is the parsed __complete protocol: one completion per line
// ("name\tdescription"), terminated by a ":<directive>" line.
type completionResult struct {
	names     []string
	descs     map[string]string
	directive cobra.ShellCompDirective
}

func (r completionResult) has(name string) bool { return slices.Contains(r.names, name) }

func (f *cliFixture) complete(args ...string) completionResult {
	code := MainWithEnv(append([]string{cobra.ShellCompRequestCmd}, args...), f.env)
	Expect(code).To(Equal(0), "stderr:\n%s", f.errw.String())
	Expect(f.fake.Execed).To(BeNil(), "completion must never hand off to docker")
	return parseCompletion(f.out.String())
}

func parseCompletion(out string) completionResult {
	r := completionResult{descs: map[string]string{}, directive: -1}
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, ":"); ok {
			d, err := strconv.Atoi(rest)
			Expect(err).NotTo(HaveOccurred())
			r.directive = cobra.ShellCompDirective(d)
			continue
		}
		name, desc, _ := strings.Cut(line, "\t")
		r.names = append(r.names, name)
		r.descs[name] = desc
	}
	Expect(r.directive).NotTo(Equal(cobra.ShellCompDirective(-1)), "no directive line in:\n%s", out)
	return r
}

// visibleLaunchFlags are the launcher flags completion is expected to offer.
func visibleLaunchFlags() []string {
	var out []string
	for _, s := range launchFlagSpecs {
		if !s.alias() {
			out = append(out, s.Name)
		}
	}
	return out
}

var _ = Describe("shell completion", func() {
	var f *cliFixture

	BeforeEach(func() {
		f = newCLIFixture()
	})

	// ---- routing ----

	It("CS-COMP-001: emits a script for every cobra-supported shell", func() {
		for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
			f = newCLIFixture()
			Expect(f.run("completion", shell)).To(Equal(0), "shell: %s", shell)
			Expect(f.out.String()).To(ContainSubstring(cobra.ShellCompRequestCmd), "shell: %s", shell)
			Expect(f.fake.Execed).To(BeNil(), "shell: %s", shell)
		}
	})

	It("CS-COMP-002: __complete is routed to cobra, never to the launcher", func() {
		Expect(f.run(cobra.ShellCompRequestCmd, "")).To(Equal(0))
		// runLaunch would have printed the cascade and built the base image.
		Expect(f.out.String()).NotTo(ContainSubstring("config cascade"))
		Expect(f.errw.String()).NotTo(ContainSubstring("Building"))
		Expect(f.fake.Execed).To(BeNil())
	})

	It("CS-COMP-003: __completeNoDesc returns completions without descriptions", func() {
		Expect(MainWithEnv([]string{cobra.ShellCompNoDescRequestCmd, "--"}, f.env)).To(Equal(0))
		r := parseCompletion(f.out.String())
		Expect(r.names).To(ContainElement("--ralph"))
		for _, n := range r.names {
			Expect(r.descs[n]).To(BeEmpty(), "completion %s carried a description", n)
		}
	})

	It("CS-COMP-017: the launch usage advertises the completion command", func() {
		Expect(f.run("--help")).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("claude-sandbox completion"))
	})

	// ---- root: subcommands and launcher flags ----

	It("CS-COMP-004: an empty first word completes the subcommand names", func() {
		r := f.complete("")
		for _, name := range []string{"init", "init-ralph", "ralph", "completion"} {
			Expect(r.has(name)).To(BeTrue(), "missing subcommand %q in %v", name, r.names)
		}
	})

	It("CS-COMP-005: a leading dash completes the launcher flags with descriptions", func() {
		r := f.complete("--")
		for _, name := range visibleLaunchFlags() {
			if name == "-h" {
				continue // does not match the "--" prefix
			}
			Expect(r.has(name)).To(BeTrue(), "missing launcher flag %q in %v", name, r.names)
			Expect(r.descs[name]).NotTo(BeEmpty(), "flag %q completed without a description", name)
		}
		Expect(r.directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))
	})

	It("CS-COMP-006: verbose flag aliases are not offered", func() {
		r := f.complete("--")
		for _, s := range launchFlagSpecs {
			if s.alias() {
				Expect(r.has(s.Name)).To(BeFalse(), "alias %q should not be offered", s.Name)
			}
		}
	})

	It("CS-COMP-018: subcommand names stop being offered once an argument is present", func() {
		r := f.complete("--ralph", "")
		for _, name := range []string{"init", "init-ralph", "ralph"} {
			Expect(r.has(name)).To(BeFalse(), "subcommand %q offered after a launcher flag", name)
		}
	})

	// ---- root: flag values ----

	It("CS-COMP-007: --model completes the model aliases", func() {
		r := f.complete("--model", "")
		Expect(r.names).To(ConsistOf("opus", "sonnet", "haiku"))
		Expect(r.directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))
	})

	It("CS-COMP-008: --limit offers nothing and suppresses file completion", func() {
		r := f.complete("--limit", "")
		Expect(r.names).To(BeEmpty())
		Expect(r.directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))
	})

	// ---- root: the passthrough boundary ----

	It("CS-COMP-009: known claude flags are offered alongside the launcher flags", func() {
		r := f.complete("--")
		Expect(r.has("--resume")).To(BeTrue())
		Expect(r.has("--continue")).To(BeTrue())
	})

	DescribeTable("the launcher flags stop being offered past the passthrough boundary",
		func(id string, args []string, wantDirective cobra.ShellCompDirective) {
			r := f.complete(args...)
			for _, name := range visibleLaunchFlags() {
				Expect(r.has(name)).To(BeFalse(), "%s: %q offered past the boundary", id, name)
			}
			Expect(r.directive).To(Equal(wantDirective), id)
		},
		Entry("CS-COMP-010: after a known claude flag", "CS-COMP-010",
			[]string{"--resume", "--"}, cobra.ShellCompDirectiveDefault),
		Entry("CS-COMP-011: after \"--\"", "CS-COMP-011",
			[]string{"--", "--"}, cobra.ShellCompDirectiveDefault),
		Entry("CS-COMP-012: after a positional argument", "CS-COMP-012",
			[]string{"somearg", "--"}, cobra.ShellCompDirectiveDefault),
		Entry("CS-COMP-013: after an unknown flag", "CS-COMP-013",
			[]string{"--frobnicate", "--"}, cobra.ShellCompDirectiveNoFileComp),
	)

	// ---- subcommands ----

	It("CS-COMP-014: init and init-ralph complete their own flags", func() {
		for _, sub := range []string{"init", "init-ralph"} {
			f = newCLIFixture()
			r := f.complete(sub, "--")
			for _, name := range []string{"--track-in-host", "--no-track-in-host", "--gitignore", "--yes"} {
				Expect(r.has(name)).To(BeTrue(), "%s: missing %q in %v", sub, name, r.names)
			}
		}
	})

	It("CS-COMP-015: ralph's numeric flags suppress file completion", func() {
		for _, name := range []string{"--limit", "--watchdog-timeout", "--max-retries", "--quota-pause"} {
			f = newCLIFixture()
			r := f.complete("ralph", name, "")
			Expect(r.names).To(BeEmpty(), "flag: %s", name)
			Expect(r.directive).To(Equal(cobra.ShellCompDirectiveNoFileComp), "flag: %s", name)
		}
	})

	It("CS-COMP-016: ralph's path flags allow file completion", func() {
		for _, name := range []string{"--prompt", "--stop-file", "--runlog-file", "--claude-bin"} {
			f = newCLIFixture()
			r := f.complete("ralph", name, "")
			Expect(r.directive).To(Equal(cobra.ShellCompDirectiveDefault), "flag: %s", name)
		}
	})

	It("CS-COMP-019: ralph --model completes the same model aliases as the launcher", func() {
		r := f.complete("ralph", "--model", "")
		Expect(r.names).To(ConsistOf("opus", "sonnet", "haiku"))
	})

	// ---- drift guards ----

	It("CS-COMP-020: the completion flag table agrees with the launcher parser", func() {
		for _, s := range launchFlagSpecs {
			args := []string{s.Name}
			if s.Value != valueNone {
				args = append(args, "x")
			}
			_, err := scanLaunchArgs(args)
			Expect(err).NotTo(HaveOccurred(), "scanLaunchArgs rejects table flag %q", s.Name)
		}
		for _, name := range scannerFlagNames() {
			_, ok := lookupLaunchFlag(name)
			Expect(ok).To(BeTrue(), "scanLaunchArgs accepts %q but the completion table omits it", name)
		}
	})

	It("CS-COMP-021: the completion flag table agrees with the launch usage", func() {
		for _, name := range visibleLaunchFlags() {
			Expect(launchUsage).To(ContainSubstring(name), "flag %q is completed but undocumented", name)
		}
	})

	Describe("CS-COMP-024: sessions and instance nouns", func() {
		It("offers the sessions subcommand", func() {
			Expect(f.complete("").has("sessions")).To(BeTrue())
		})

		It("completes --attach= and --join= from the live sessions", func() {
			f.fake.On("docker ps", strings.Join([]string{
				strings.Join([]string{"cs-a", "Up 1h", f.proj, "claude", "otter", "v1", "", "", "", ""}, "\x1f"),
				strings.Join([]string{"cs-b", "Up 2h", f.proj, "claude", "heron", "v1", "", "", "", ""}, "\x1f"),
			}, "\n")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)

			r := f.complete("--attach=")
			Expect(r.names).To(ConsistOf("--attach=otter", "--attach=heron"))
			Expect(r.directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))

			f.out.Reset()
			j := f.complete("--join=")
			Expect(j.names).To(ConsistOf("--join=otter", "--join=heron"))
		})

		It("narrows to the typed prefix", func() {
			f.fake.On("docker ps", strings.Join([]string{
				strings.Join([]string{"cs-a", "Up 1h", f.proj, "claude", "otter", "v1", "", "", "", ""}, "\x1f"),
				strings.Join([]string{"cs-b", "Up 2h", f.proj, "claude", "heron", "v1", "", "", "", ""}, "\x1f"),
			}, "\n")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.complete("--attach=o").names).To(ConsistOf("--attach=otter"))
		})

		It("offers nothing rather than failing when docker is unavailable", func() {
			// A TAB press must never surface an error or block, whatever docker does.
			f.fake.On("docker ps", "", execx.Fail(1))
			r := f.complete("--attach=")
			Expect(r.names).To(BeEmpty())
			Expect(r.directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))
		})

		It("excludes ralph containers, which cannot be attached to or joined", func() {
			f.fake.On("docker ps", strings.Join([]string{
				"cs-r", "Up 1h", f.proj, "ralph", "", "v1", "", "", "",
			}, "\x1f")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  ralph\n", nil)
			Expect(f.complete("--attach=").names).To(BeEmpty())
		})
	})
})

// scannerFlagNames extracts the flag names scanLaunchArgs recognizes by reading
// its case labels out of the source. Reflection cannot see into a switch, and a
// hand-maintained second list would be the very drift this guard exists to
// catch.
func scannerFlagNames() []string {
	src, err := os.ReadFile("root.go")
	Expect(err).NotTo(HaveOccurred())
	body := string(src)
	start := strings.Index(body, "func scanLaunchArgs(")
	Expect(start).To(BeNumerically(">", 0))
	body = body[start:]
	end := strings.Index(body, "\n}\n")
	Expect(end).To(BeNumerically(">", 0))
	body = body[:end]

	caseLine := regexp.MustCompile(`(?m)^\s*case (.+):$`)
	quoted := regexp.MustCompile(`"([^"]*)"`)
	var names []string
	for _, m := range caseLine.FindAllStringSubmatch(body, -1) {
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			if name := q[1]; strings.HasPrefix(name, "-") && name != "--" {
				names = append(names, name)
			}
		}
	}
	Expect(names).NotTo(BeEmpty(), "failed to parse scanLaunchArgs case labels")
	return names
}
