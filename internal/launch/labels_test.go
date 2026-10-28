package launch_test

// Spec: spec/launch.feature (CS-LNCH-224, CS-LNCH-226) — no label value the
// launcher writes holds a control or bidi character.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

var _ = Describe("label values (CS-LNCH-224, CS-LNCH-226)", func() {
	var (
		proj, shadow string
		in           launch.Inputs
	)
	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home := filepath.Join(base, "home")
		proj, shadow = filepath.Join(base, "proj"), filepath.Join(base, "shadow")
		mkdir(home)
		mkdir(proj)
		mkdir(shadow)
		env := map[string]string{}
		in = launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(k string) string { return env[k] },
			LookupEnv: lookupIn(env),
			ImageName: "claude-sandbox-df-proj-abc123", ImageID: "sha256:aaaa",
			Cfg: &cascade.Config{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
		}
	})
	build := func() (*launch.Plan, error) {
		dir, err := os.MkdirTemp(shadow, "run")
		Expect(err).NotTo(HaveOccurred())
		cp := in
		cp.TempDir = dir
		return launch.Build(cp)
	}

	It("CS-LNCH-226: an ordinary plan's labels all pass", func() {
		p, err := build()
		Expect(err).NotTo(HaveOccurred())
		Expect(launch.CheckLabels(p.Labels)).To(Succeed())
	})

	DescribeTable("CS-LNCH-226: Build refuses a derived label value with a control or bidi character, naming the label",
		func(set func(*launch.Inputs), label, char string) {
			set(&in)
			_, err := build()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("label " + label + " holds"))
			Expect(err.Error()).To(ContainSubstring(char))
		},
		Entry("the version, separator", func(i *launch.Inputs) { i.Version = "v1\x1fcreated" }, "claude-sandbox.version", "U+001F"),
		Entry("the version, newline", func(i *launch.Inputs) { i.Version = "v1\nx" }, "claude-sandbox.version", "U+000A"),
		Entry("the model, tab", func(i *launch.Inputs) { i.Cfg = &cascade.Config{Model: "opus\tx"} }, "claude-sandbox.model", "U+0009"),
		Entry("the model, a bidi override", func(i *launch.Inputs) { i.CLIModel = "opus\u202ex" }, "claude-sandbox.model", "U+202E"),
		Entry("the memoryLimit source", func(i *launch.Inputs) {
			i.Cfg = &cascade.Config{MemoryLimit: "4g"}
			i.MemoryLimitSource = "/w/\rconfig.yaml"
		}, "claude-sandbox.memorylimitsource", "U+000D"),
	)

	It("CS-LNCH-226: Build escapes a bidi control in an input path in the inputs label", func() {
		dir := filepath.Join(proj, "a\u202eb")
		mkdir(dir)
		ef := filepath.Join(dir, "env")
		Expect(os.WriteFile(ef, []byte("X=1\n"), 0o600)).To(Succeed())
		in.EnvFiles = []string{ef}
		in.Env = []cascade.EnvFile{{Path: ef, Content: []byte("X=1\n")}}
		p, err := build()
		Expect(err).NotTo(HaveOccurred())
		var inputs string
		for _, l := range p.Labels {
			if v, ok := strings.CutPrefix(l, "claude-sandbox.inputs="); ok {
				inputs = v
			}
		}
		Expect(inputs).To(ContainSubstring(`a\u202eb`))
		Expect(strings.ContainsRune(inputs, '\u202e')).To(BeFalse())
		Expect(launch.DecodeInputs(inputs)).To(ContainElement(HaveField("Path", ef)))
	})

	It("CS-LNCH-226: Build refuses a bind mount whose path holds a control character", func() {
		in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: "/tmp/x\n" + strings.Repeat("y", 3), Container: "/tmp/x"}}}
		_, err := build()
		Expect(err).To(MatchError(ContainSubstring("a bind mount's path holds a control character (U+000A)")))
	})

	It("CS-LNCH-226: the drift check's build (DriftOnly) skips the value checks", func() {
		in.Cfg = &cascade.Config{Model: "opus\x1fx", Mounts: []cascade.Mount{{Host: "/tmp/x\ny", Container: "/tmp/x"}}}
		in.DriftOnly = true
		p, err := build()
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ConfigHash).NotTo(BeEmpty())
	})

	It("CS-SESS-120: every label discovery reads is written, empty when unset", func() {
		p, err := build()
		Expect(err).NotTo(HaveOccurred())
		keys := map[string]bool{}
		for _, l := range p.Labels {
			k, _, _ := strings.Cut(l, "=")
			keys[k] = true
		}
		for _, k := range sessions.ReadLabels {
			Expect(keys).To(HaveKey(k), "an image LABEL %s would otherwise reach discovery", k)
		}
		Expect(p.Labels).To(ContainElement("claude-sandbox.resume="))
		Expect(p.Labels).To(ContainElement("claude-sandbox.continue="))
	})

	It("CS-LNCH-224: CheckConfiguredLabels names the value and its source, never the value", func() {
		err := launch.CheckConfiguredLabels([]launch.ConfiguredLabelValue{
			{What: "model", Source: "/w/.claude-sandbox/config.yaml", Value: "opus"},
			{What: "memoryLimit", Source: "/w/.claude-sandbox/config.yaml", Value: "8g\x1fsecret"},
		})
		Expect(err).To(MatchError(ContainSubstring("memoryLimit (/w/.claude-sandbox/config.yaml) holds a control character (U+001F)")))
		Expect(err.Error()).NotTo(ContainSubstring("secret"))
		Expect(launch.CheckConfiguredLabels([]launch.ConfiguredLabelValue{{What: "model", Value: "claude-opus-4[1m]"}})).To(Succeed())
	})

	It("CS-LNCH-226: the inputs label escapes a bidi control, which decodes to the same text", func() {
		raw, err := json.Marshal([]string{"/w/a\u202eb/config.yaml"})
		Expect(err).NotTo(HaveOccurred())
		esc := launch.EscapeBidi(string(raw))
		Expect(launch.LabelValueProblem(esc)).To(BeEmpty())
		Expect(esc).To(ContainSubstring(`\u202e`))
		var back []string
		Expect(json.Unmarshal([]byte(esc), &back)).To(Succeed())
		Expect(back).To(Equal([]string{"/w/a\u202eb/config.yaml"}))
		Expect(strings.Contains(esc, "\u202e")).To(BeFalse())
	})
})
