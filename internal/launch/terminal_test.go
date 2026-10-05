package launch_test

// Spec: spec/launch.feature CS-LNCH-178..181 and spec/sessions.feature
// CS-SESS-090/092 — the terminal identity (TERMINAL_EMULATOR) a launch passes
// into the container, the claude-sandbox.terminal label, and what a join does.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

const jedi = "JetBrains-JediTerm"

// envFile is one snapshotted env file.
func envFile(path, content string) cascade.EnvFile {
	return cascade.EnvFile{Path: path, Content: []byte(content)}
}

// lookupIn mirrors os.LookupEnv over a map.
func lookupIn(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

var _ = Describe("terminal identity resolver (CS-LNCH-178..181, CS-SESS-090/092)", func() {
	resolve := func(env map[string]string, files ...cascade.EnvFile) launch.TerminalResult {
		return launch.ResolveTerminal(lookupIn(env), files, lookupIn(env))
	}

	// The table of 03_review-fixes-3.md § M1 (rows 1-6), plus the carried
	// review low: a join under a winning assignment sets the walk's value.
	DescribeTable("CS-LNCH-178, CS-LNCH-179, CS-LNCH-181, CS-SESS-090, CS-SESS-092: create, label and join keyed on the walk's last effective line",
		func(env map[string]string, files []cascade.EnvFile, create []string, label string, joinSet, joinUnset []string, pinned string) {
			r := resolve(env, files...)
			Expect(r.CreateEnv(false)).To(Equal(create))
			Expect(r.Label(false)).To(Equal(label))
			set, unset := r.JoinEnv()
			Expect(set).To(Equal(joinSet))
			Expect(unset).To(Equal(joinUnset))
			Expect(r.PinnedBy()).To(Equal(pinned))
		},
		Entry("f1 =X, f2 bare, env Y: the bare line wins (rule 3)",
			map[string]string{"TERMINAL_EMULATOR": "Y"},
			[]cascade.EnvFile{envFile("/f1", "TERMINAL_EMULATOR=X\n"), envFile("/f2", "TERMINAL_EMULATOR\n")},
			[]string{"TERMINAL_EMULATOR=Y"}, "TERMINAL_EMULATOR=Y", []string{"TERMINAL_EMULATOR=Y"}, []string(nil), ""),
		Entry("f1 =X, f2 bare, env unset: the assignment stands (rule 2)",
			map[string]string{},
			[]cascade.EnvFile{envFile("/f1", "TERMINAL_EMULATOR=X\n"), envFile("/f2", "TERMINAL_EMULATOR\n")},
			[]string(nil), "TERMINAL_EMULATOR=X", []string{"TERMINAL_EMULATOR=X"}, []string(nil), "/f1"),
		Entry("f1 bare, f2 =X, env Y: the later assignment wins",
			map[string]string{"TERMINAL_EMULATOR": "Y"},
			[]cascade.EnvFile{envFile("/f1", "TERMINAL_EMULATOR\n"), envFile("/f2", "TERMINAL_EMULATOR=X\n")},
			[]string(nil), "TERMINAL_EMULATOR=X", []string{"TERMINAL_EMULATOR=X"}, []string(nil), "/f2"),
		Entry("f1 =X, f2 =Z: the last assignment wins",
			map[string]string{"TERMINAL_EMULATOR": "Y"},
			[]cascade.EnvFile{envFile("/f1", "TERMINAL_EMULATOR=X\n"), envFile("/f2", "TERMINAL_EMULATOR=Z\n")},
			[]string(nil), "TERMINAL_EMULATOR=Z", []string{"TERMINAL_EMULATOR=Z"}, []string(nil), "/f2"),
		Entry("f1 = (empty): an explicit file choice, read as unset",
			map[string]string{"TERMINAL_EMULATOR": "Y"},
			[]cascade.EnvFile{envFile("/f1", "TERMINAL_EMULATOR=\n")},
			[]string(nil), "none", []string(nil), []string{"TERMINAL_EMULATOR"}, "/f1"),
		Entry("no line, env Y",
			map[string]string{"TERMINAL_EMULATOR": "Y"}, nil,
			[]string{"TERMINAL_EMULATOR=Y"}, "TERMINAL_EMULATOR=Y", []string{"TERMINAL_EMULATOR=Y"}, []string(nil), ""),
		Entry("no line, env unset",
			map[string]string{}, nil,
			[]string(nil), "none", []string(nil), []string{"TERMINAL_EMULATOR"}, ""),
		Entry("no line, env set but empty: empty is unset",
			map[string]string{"TERMINAL_EMULATOR": ""}, nil,
			[]string(nil), "none", []string(nil), []string{"TERMINAL_EMULATOR"}, ""),
	)

	It("CS-LNCH-179: a bare line with S unset but the client holding a value passes an empty -e (the S-source seam)", func() {
		// S from elsewhere than the docker client's environment (OQ-B's seam):
		// S unset while the bare line would pass the client's stale value.
		r := launch.ResolveTerminal(func(string) (string, bool) { return "", false },
			[]cascade.EnvFile{envFile("/f1", "TERMINAL_EMULATOR\n")},
			lookupIn(map[string]string{"TERMINAL_EMULATOR": jedi}))
		Expect(r.CreateEnv(false)).To(Equal([]string{"TERMINAL_EMULATOR="}))
		Expect(r.Label(false)).To(Equal("none"))
	})

	It("CS-LNCH-180: headless and ralph (passive) pass nothing; their label is the env-file walk, never S", func() {
		r := resolve(map[string]string{"TERMINAL_EMULATOR": "Y"}, envFile("/f1", "TERMINAL_EMULATOR\n"))
		Expect(r.CreateEnv(true)).To(BeEmpty())
		Expect(r.Label(true)).To(Equal("TERMINAL_EMULATOR=Y"), "a bare line passes the launcher's value")
		r = resolve(map[string]string{"TERMINAL_EMULATOR": "Y"})
		Expect(r.CreateEnv(true)).To(BeEmpty())
		Expect(r.Label(true)).To(Equal("none"), "no env-file line: the container has none")
	})

	It("CS-LNCH-181: the label value is cleaned and capped", func() {
		r := resolve(map[string]string{"TERMINAL_EMULATOR": "a=b;c\x1fd\x07e" + strings.Repeat("x", 100)})
		l := r.Label(false)
		Expect(l).To(HavePrefix("TERMINAL_EMULATOR=abc d e"))
		v := strings.TrimPrefix(l, "TERMINAL_EMULATOR=")
		Expect([]rune(v)).To(HaveLen(64))
		Expect(strings.ContainsAny(v, "=;\x1f\x07")).To(BeFalse())
		// The container gets the raw value: only the label is cleaned.
		Expect(r.CreateEnv(false)[0]).To(HavePrefix("TERMINAL_EMULATOR=a=b;c"))
	})

	It("CS-LNCH-178: the list is exactly TERMINAL_EMULATOR: never TERM, TMUX or TMUX_PANE", func() {
		Expect(launch.TerminalEnv).To(Equal([]string{"TERMINAL_EMULATOR"}))
	})
})

var _ = Describe("terminal identity in Build (CS-LNCH-178..181)", func() {
	var (
		home, proj, shadow string
		env                map[string]string
		in                 launch.Inputs
	)
	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home, proj, shadow = filepath.Join(base, "home"), filepath.Join(base, "proj"), filepath.Join(base, "shadow")
		mkdir(home)
		mkdir(proj)
		mkdir(shadow)
		env = map[string]string{}
		in = launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(k string) string { return env[k] },
			LookupEnv: lookupIn(env),
			ImageName: "claude-sandbox-df-proj-abc123", ImageID: "sha256:aaaa",
			Cfg: &cascade.Config{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
		}
	})
	build := func() *launch.Plan {
		dir, err := os.MkdirTemp(shadow, "run")
		Expect(err).NotTo(HaveOccurred())
		cp := in
		cp.TempDir = dir
		p, err := launch.Build(cp)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	labelOf := func(p *launch.Plan) string {
		for _, l := range p.Labels {
			if v, ok := strings.CutPrefix(l, launch.LabelTerminal+"="); ok {
				return v
			}
		}
		Fail("no terminal label")
		return ""
	}

	It("CS-LNCH-178, CS-LNCH-181: an interactive launch passes -e TERMINAL_EMULATOR=<value> and labels it", func() {
		env["TERMINAL_EMULATOR"] = jedi
		p := build()
		Expect(p.EnvFlags).To(ContainElement("TERMINAL_EMULATOR=" + jedi))
		Expect(labelOf(p)).To(Equal("TERMINAL_EMULATOR=" + jedi))
		Expect(p.Terminal).To(Equal("TERMINAL_EMULATOR=" + jedi))
		Expect(strings.Join(p.CreateArgs(proj), " ")).To(ContainSubstring("--label claude-sandbox.terminal=TERMINAL_EMULATOR=" + jedi))
	})

	It("CS-LNCH-178, CS-LNCH-181: unset passes nothing and labels none", func() {
		p := build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("TERMINAL_EMULATOR"))
			Expect(e).NotTo(HavePrefix("TERM="))
			Expect(e).NotTo(HavePrefix("TMUX"))
		}
		Expect(labelOf(p)).To(Equal("none"))
	})

	It("CS-LNCH-179: an assigning env-file line wins: no -e, the label is the file's value", func() {
		env["TERMINAL_EMULATOR"] = "xterm.js"
		envPath := filepath.Join(proj, ".claude-sandbox", "env")
		in.Env = []cascade.EnvFile{envFile(envPath, "TERMINAL_EMULATOR="+jedi+"\n")}
		p := build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("TERMINAL_EMULATOR"))
		}
		Expect(labelOf(p)).To(Equal("TERMINAL_EMULATOR=" + jedi))
	})

	It("CS-LNCH-180: headless and ralph pass no -e; the label is the walk", func() {
		env["TERMINAL_EMULATOR"] = "Y"
		in.Headless = true
		in.Env = []cascade.EnvFile{envFile("/e", "TERMINAL_EMULATOR\n")}
		p := build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("TERMINAL_EMULATOR"))
		}
		Expect(labelOf(p)).To(Equal("TERMINAL_EMULATOR=Y"))

		in.Headless, in.RalphMode, in.Env = false, true, nil
		p = build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("TERMINAL_EMULATOR"))
		}
		Expect(labelOf(p)).To(Equal("none"))
	})

	It("CS-LNCH-181: two launches differing only in the terminal identity hash equal", func() {
		env["TERMINAL_EMULATOR"] = jedi
		a := build()
		delete(env, "TERMINAL_EMULATOR")
		b := build()
		Expect(a.ConfigHash).To(Equal(b.ConfigHash))
		Expect(a.Terminal).NotTo(Equal(b.Terminal))
	})
})
