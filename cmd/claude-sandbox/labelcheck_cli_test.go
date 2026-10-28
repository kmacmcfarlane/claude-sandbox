package main

// Spec: spec/launch.feature CS-LNCH-224/225 and spec/sessions.feature
// CS-SESS-118 — end to end through MainWithEnv: a configured label value with
// a control character is refused before any image work, naming its source; a
// command-line --model with one is refused at parse; a discovery row with an
// extra field is dropped with a warning.

import (
	"path/filepath"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("label values end to end (CS-LNCH-224/225, CS-SESS-118)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	noImageWork := func() {
		Expect(createdAny(f)).To(BeFalse())
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker buildx"), "BuildKit is probed only after validation")
			Expect(l).NotTo(HavePrefix("docker build"))
			Expect(l).NotTo(HavePrefix("docker image inspect"))
		}
	}

	It("CS-LNCH-224: a cascade model holding the unit separator exits 2 naming the key and the file", func() {
		cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(cfg, "model: \"opus\\x1fcreated\"\n")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("Error: model (" + cfg + ") holds a control character (U+001F)"))
		Expect(f.errw.String()).NotTo(ContainSubstring("created"), "the value is never printed")
		noImageWork()
	})

	It("CS-LNCH-224: an upstream cascade memoryLimit with a newline names the upstream file", func() {
		upstream := filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "config.yaml")
		writeFile(upstream, "memoryLimit: \"8g\\nx\"\n")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("Error: memoryLimit (" + upstream + ") holds a control character (U+000A)"))
		noImageWork()
	})

	It("CS-LNCH-224: CLAUDE_CONFIG_DIR with a control character names the environment", func() {
		f.envmap["CLAUDE_CONFIG_DIR"] = f.home + "/.claude\x1fx"
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("Error: CLAUDE_CONFIG_DIR (environment) holds a control character (U+001F)"))
		noImageWork()
	})

	It("CS-LNCH-224: an ordinary cascade model launches and is labelled", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "model: opus\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.launchLine()).To(ContainSubstring("--label claude-sandbox.model=opus "))
	})

	DescribeTable("CS-LNCH-225: a command-line --model with a control or bidi character is refused",
		func(args []string, char string) {
			Expect(f.run(args...)).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("Error: --model holds"))
			Expect(f.errw.String()).To(ContainSubstring(char))
			Expect(f.errw.String()).NotTo(ContainSubstring("created"))
			Expect(f.fake.Calls).To(BeEmpty(), "refused before anything runs")
		},
		Entry("--model <v>, separator", []string{"--model", "opus\x1fcreated"}, "U+001F"),
		Entry("--model=<v>, tab", []string{"--model=opus\tcreated"}, "U+0009"),
		Entry("--model <v>, a bidi override", []string{"--model", "opus\u202ecreated"}, "U+202E"),
	)

	It("CS-SESS-118: a forged row (raw, one field too many) is dropped with one warning; a real one is listed", func() {
		evil := strings.Join([]string{"cs-evil", "Up 1 hour", f.proj, "claude", "otter", "v1", "opus", "x", "", "", "",
			"running", "2026-09-30 12:00:00 +0000 UTC", "8g", "default", "",
			"", "", "", "", "", "none", "none", "", "none", "", "", ""}, psSep)
		good := psRow("cs-good", "Up 2 hours", f.proj, "heron")
		f.fake.On("docker ps", evil+"\n"+good+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("sessions")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("heron"))
		Expect(f.out.String()).NotTo(ContainSubstring("cs-evil"))
		Expect(strings.Count(f.errw.String(), "WARNING: ignoring container cs-evil in the sandbox listing (28 fields, not 27)")).To(Equal(1))
	})

	It("CS-LNCH-224: a cascade mount host holding a newline exits 2 naming the file, before any image work", func() {
		cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(cfg, "mounts:\n  - host: \"/tmp/x\\nforged\"\n    container: /tmp/x\n")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("Error: mounts[0] host (" + cfg + ") holds a control character (U+000A)"))
		Expect(f.errw.String()).NotTo(ContainSubstring("forged"))
		noImageWork()
	})

	It("CS-SESS-120: a new container writes every label discovery reads, an unset one empty", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		labels := map[string]bool{}
		for _, l := range argPairsCLI(f.launched().Args, "--label") {
			k, _, _ := strings.Cut(l, "=")
			labels[k] = true
		}
		for _, k := range sessions.ReadLabels {
			Expect(labels).To(HaveKey(k), "an image LABEL %s would otherwise reach discovery", k)
		}
		// An image defining claude-sandbox.resume cannot make this container
		// look like it resumes a conversation: the create sets the key empty,
		// and docker keeps the create's value over the image's.
		Expect(argPairsCLI(f.launched().Args, "--label")).To(ContainElement("claude-sandbox.resume="))
	})

	It("CS-LNCH-226: an attach to a container whose config value went bad is not refused and shows no false drift", func() {
		cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		// A container carrying this config's hash (an older launcher created
		// it; a launch now would refuse the value): the drift check creates
		// nothing, so it must compute the hash, not fail on the label check.
		writeFile(cfg, "model: \"opus\\x1fcreated\"\n")
		hash := currentHash(f)
		f.fake.On("docker ps", psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "opus", hash, "[]")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("cannot compute the current configuration"))
		Expect(f.errw.String()).NotTo(ContainSubstring("different configuration"))
		Expect(f.errw.String()).NotTo(ContainSubstring("holds a control character"))
	})
})
