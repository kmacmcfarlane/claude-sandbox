package cascade_test

// Spec: spec/config-cascade.feature (CS-CASC-021..029) — the env override
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
	// host is the injected launcher environment (hermetic: never os env).
	var host map[string]string
	lookup := func(k string) (string, bool) { v, ok := host[k]; return v, ok }
	print := func(files ...string) string {
		var b bytes.Buffer
		cascade.PrintEnvOverrides(&b, files, lookup)
		return b.String()
	}

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		host = map[string]string{}
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

	It("CS-CASC-023: attributes keys to the most-local file with exact per-key provenance", func() {
		ws := level("/ws", "A=1\nB=1\n")
		p := level("/ws/p", "A=2\nC=2\n")
		q := level("/ws/p/q", "A=3\nC=3\n")
		Expect(print(ws, p, q)).To(Equal(
			"Env override: A in " + q + " overrides " + p + ", " + ws +
				"; C in " + q + " overrides " + p + "\n"))
	})

	It("CS-CASC-023: keys overriding the same files share a segment", func() {
		ws := level("/ws", "A=1\nC=1\n")
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
		Expect(print(ws, p)).To(BeEmpty()) // host does not set FOO
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

	DescribeTable("CS-CASC-026: reads indented and BOM-prefixed keys as docker does",
		func(line string) {
			ws := level("/ws", "GITLAB_TOKEN=new\n")
			p := level("/ws/p", line+"\n")
			Expect(print(ws, p)).To(Equal("Env override: GITLAB_TOKEN in " + p + " overrides " + ws + "\n"))
		},
		Entry("leading blanks", "  GITLAB_TOKEN=stale"),
		Entry("leading tab", "\tGITLAB_TOKEN=stale"),
		Entry("UTF-8 BOM on line 1", "\xEF\xBB\xBFGITLAB_TOKEN=stale"),
		Entry("CRLF assignment: the '\\r' stays out of the key", "GITLAB_TOKEN=stale\r"),
	)

	It("CS-CASC-026: an indented comment is still a comment", func() {
		ws := level("/ws", "GITLAB_TOKEN=new\n")
		p := level("/ws/p", "  # GITLAB_TOKEN=stale\n")
		Expect(print(ws, p)).To(BeEmpty())
	})

	DescribeTable("CS-CASC-027: a bare key overrides when the launcher's environment sets it",
		func(content string) {
			host["GITLAB_TOKEN"] = "host-secret"
			ws := level("/ws", "GITLAB_TOKEN=new\n")
			p := level("/ws/p", content)
			out := print(ws, p)
			Expect(out).To(Equal("Env override: GITLAB_TOKEN in " + p + " overrides " + ws + "\n"))
			Expect(out).NotTo(ContainSubstring("host-secret"))
		},
		Entry("LF line ending", "GITLAB_TOKEN\n"),
		Entry("CRLF line ending: the '\\r' is not in the key", "GITLAB_TOKEN\r\n"),
		Entry("trailing '\\r' with no final newline", "GITLAB_TOKEN\r"),
	)

	It("CS-CASC-027: whole CRLF files name each key without a '\\r'", func() {
		host["CRB"] = "hostcrb"
		ws := level("/ws", "CRA=up\r\nCRB=up\r\n")
		p := level("/ws/p", "CRA=loc\r\nCRB\r\n")
		out := print(ws, p)
		Expect(out).To(Equal("Env override: CRA, CRB in " + p + " overrides " + ws + "\n"))
		Expect(out).NotTo(ContainSubstring("\r"))
	})

	It("CS-CASC-028: a CRLF bare key the launcher's environment lacks is not a definition", func() {
		ws := level("/ws", "GITLAB_TOKEN=new\r\n")
		p := level("/ws/p", "GITLAB_TOKEN\r\n")
		Expect(print(ws, p)).To(BeEmpty())
	})

	It("CS-CASC-028: a bare key ending in '\\r\\r' keeps one '\\r' and does not resolve", func() {
		// docker drops only ONE trailing '\r': the key is "GITLAB_TOKEN\r",
		// which the launcher's environment does not set (Docker 29.8.0).
		host["GITLAB_TOKEN"] = "host-secret"
		ws := level("/ws", "GITLAB_TOKEN=new\r\n")
		p := level("/ws/p", "GITLAB_TOKEN\r\r\n")
		Expect(print(ws, p)).To(BeEmpty())
	})

	It("CS-CASC-027: set-but-empty in the launcher's environment counts as set", func() {
		host["GITLAB_TOKEN"] = ""
		ws := level("/ws", "GITLAB_TOKEN=new\n")
		p := level("/ws/p", "GITLAB_TOKEN\n")
		Expect(print(ws, p)).To(ContainSubstring("GITLAB_TOKEN in " + p))
	})

	It("CS-CASC-028: a bare key is not a definition when the launcher's environment lacks it", func() {
		ws := level("/ws", "GITLAB_TOKEN=new\n")
		p := level("/ws/p", "GITLAB_TOKEN\n")
		Expect(print(ws, p)).To(BeEmpty())
	})

	It("CS-CASC-028: a nil lookup never resolves a bare key", func() {
		ws := level("/ws", "GITLAB_TOKEN=new\n")
		p := level("/ws/p", "GITLAB_TOKEN\n")
		var b bytes.Buffer
		cascade.PrintEnvOverrides(&b, []string{ws, p}, nil)
		Expect(b.String()).To(BeEmpty())
	})

	It("CS-CASC-029: never names keys docker rejects", func() {
		ws := level("/ws", "=x\nBAD KEY=1\n")
		p := level("/ws/p", "=y\nBAD KEY=2\n")
		Expect(print(ws, p)).To(BeEmpty())
	})
})
