package cascade_test

// Spec: spec/config-cascade.feature (CS-CASC-021..025) — the env override
// notice names keys a more-local env file shadows, never their values.

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

var _ = Describe("env override notice", func() {
	var tmp string

	// level writes <tmp><rel>/.claude-sandbox/env and returns its path.
	level := func(rel, content string) string {
		dir := filepath.Join(tmp, rel, ".claude-sandbox")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		f := filepath.Join(dir, "env")
		Expect(os.WriteFile(f, []byte(content), 0o644)).To(Succeed())
		return f
	}
	print := func(files ...string) string {
		var b bytes.Buffer
		cascade.PrintEnvOverrides(&b, files)
		return b.String()
	}

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
	})

	It("CS-CASC-021: names a key defined upstream and in the project env on one line", func() {
		ws := level("/ws", "GITLAB_TOKEN=new\n")
		p := level("/ws/p", "GITLAB_TOKEN=old\n")
		Expect(print(ws, p)).To(Equal("Env override: GITLAB_TOKEN in " + p + " overrides " + ws + "\n"))
	})

	It("CS-CASC-022: lists several overridden keys on one line in the winner's order", func() {
		ws := level("/ws", "FOO=1\nBAR=2\nONLY_UP=3\n")
		p := level("/ws/p", "BAR=4\nFOO=5\nONLY_P=6\n")
		Expect(print(ws, p)).To(Equal("Env override: BAR, FOO in " + p + " overrides " + ws + "\n"))
	})

	It("CS-CASC-022: a key assigned twice in one file is not an override", func() {
		ws := level("/ws", "OTHER=1\n")
		p := level("/ws/p", "FOO=1\nFOO=2\n")
		Expect(print(ws, p)).To(BeEmpty())
	})

	It("CS-CASC-023: attributes keys to the most-local file and lists overridden files nearest-first", func() {
		ws := level("/ws", "A=1\nB=1\n")
		p := level("/ws/p", "A=2\nC=2\n")
		q := level("/ws/p/q", "A=3\nC=3\n")
		Expect(print(ws, p, q)).To(Equal("Env override: A, C in " + q + " overrides " + p + ", " + ws + "\n"))
	})

	It("CS-CASC-023: prints one line per winning file, most-local first", func() {
		ws := level("/ws", "A=1\nB=1\n")
		p := level("/ws/p", "B=2\n")
		q := level("/ws/p/q", "A=3\n")
		Expect(print(ws, p, q)).To(Equal(
			"Env override: A in " + q + " overrides " + ws + "\n" +
				"Env override: B in " + p + " overrides " + ws + "\n"))
	})

	It("CS-CASC-024: prints nothing when no key is defined in two files", func() {
		ws := level("/ws", "FOO=1\n")
		p := level("/ws/p", "BAR=2\n# FOO=3\nFOO\n")
		Expect(print(ws, p)).To(BeEmpty())
	})

	It("CS-CASC-024: skips unreadable files", func() {
		p := level("/ws/p", "FOO=1\n")
		Expect(print(filepath.Join(tmp, "missing", "env"), p)).To(BeEmpty())
	})

	It("CS-CASC-025: never prints a value", func() {
		ws := level("/ws", "SECRET=upstream-val\n")
		p := level("/ws/p", "SECRET=local-val\n")
		out := print(ws, p)
		Expect(out).To(ContainSubstring("SECRET"))
		Expect(out).NotTo(ContainSubstring("upstream-val"))
		Expect(out).NotTo(ContainSubstring("local-val"))
	})
})
