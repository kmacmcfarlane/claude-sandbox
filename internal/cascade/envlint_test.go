package cascade_test

// Spec: spec/config-cascade.feature (CS-CASC-013..020) — env file linting.
//
// CS-CASC-020 (every cascade level linted at launch, warnings on stderr,
// launch proceeds) is launcher wiring — covered by the launch/CLI suite.

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

// writeEnv writes content to <tmp>/env and returns its path.
func writeEnv(tmp, content string) string {
	f := filepath.Join(tmp, "env")
	Expect(os.WriteFile(f, []byte(content), 0o644)).To(Succeed())
	return f
}

// text renders all warnings as one string, as the launcher prints them.
func text(ws []cascade.EnvWarning) string {
	var b strings.Builder
	for _, w := range ws {
		for _, l := range w.Lines() {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

var _ = Describe("env file linting", func() {
	var tmp string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
	})

	It("CS-CASC-013: reports values wrapped in matching quotes", func() {
		f := writeEnv(tmp, "DOUBLE=\"secret\"\nSINGLE='secret'\n")

		ws, err := cascade.LintEnvFile(f)
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(2))

		Expect(ws[0].Kind).To(Equal(cascade.EnvWarningQuoted))
		Expect(ws[0].File).To(Equal(f))
		Expect(ws[0].Line).To(Equal(1))
		Expect(ws[0].Key).To(Equal("DOUBLE"))
		Expect(ws[1].Line).To(Equal(2))
		Expect(ws[1].Key).To(Equal("SINGLE"))

		out := text(ws)
		// Each warning locates itself and explains the docker semantics.
		Expect(out).To(ContainSubstring(f + ":1: value for DOUBLE is wrapped in \" quotes."))
		Expect(out).To(ContainSubstring(f + ":2: value for SINGLE is wrapped in ' quotes."))
		Expect(out).To(ContainSubstring("docker --env-file does not strip quotes"))
		Expect(out).To(ContainSubstring("the value (a secret will fail auth while still looking set). Remove them."))
	})

	DescribeTable("CS-CASC-014: leaves values that are not quote-wrapped alone",
		func(value string) {
			ws, err := cascade.LintEnvFile(writeEnv(tmp, "KEY="+value+"\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ws).To(BeEmpty())
		},
		Entry("unquoted", "plain"),
		Entry("opening quote only", `"unbalanced`),
		Entry("closing quote only", "unbalanced'"),
		Entry("a lone double quote cannot be a matching pair", `"`),
		Entry("a lone single quote cannot be a matching pair", "'"),
		Entry("quotes not at both ends", `say "hi"`),
		Entry("ends do not match each other", `"mixed'`),
		Entry("empty value", ""),
	)

	It("CS-CASC-015: reports an empty quoted value", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "KEY=\"\"\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Kind).To(Equal(cascade.EnvWarningQuoted))
	})

	DescribeTable("CS-CASC-016: CRLF line endings produce no carriage-return warning",
		func(content string) {
			// docker drops one trailing '\r' per line, so CRLF is harmless.
			ws, err := cascade.LintEnvFile(writeEnv(tmp, content))
			Expect(err).NotTo(HaveOccurred())
			Expect(ws).To(BeEmpty())
			Expect(text(ws)).NotTo(ContainSubstring("carriage return"))
		},
		Entry("one CRLF assignment", "KEY=value\r\n"),
		Entry("a whole CRLF file", "# comment\r\n\r\nA=1\r\nB=2\r\n"),
		Entry("a trailing '\\r' with no final newline", "KEY=value\r"),
		Entry("a bare CRLF key", "KEY\r\n"),
		// Only ONE '\r' goes: docker passes "\"x\"\r" on, which is not
		// quote-wrapped, so no quote warning either.
		Entry("'\\r\\r': the second '\\r' stays on the value", "KEY=\"x\"\r\r\n"),
	)

	It("CS-CASC-016: the reader drops exactly one trailing '\\r' per line, as docker does", func() {
		// Docker 29.8.0: CR=x\r\n is the one-byte value x; RR=y\r\r\n keeps
		// one '\r'; a bare KEY\r is key KEY.
		as, err := cascade.ReadEnvAssignments(writeEnv(tmp, "CR=x\r\nRR=y\r\r\nB\r\nBB\r\r\nLAST=z\r"))
		Expect(err).NotTo(HaveOccurred())
		Expect(as).To(HaveLen(5))
		type kv struct {
			Key, Value string
			HasValue   bool
		}
		var got []kv
		for _, a := range as {
			got = append(got, kv{a.Key, a.Value, a.HasValue})
		}
		Expect(got).To(Equal([]kv{
			{"CR", "x", true},
			{"RR", "y\r", true},
			{"B", "", false},
			{"BB\r", "", false},
			{"LAST", "z", true},
		}))
	})

	It("CS-CASC-017: a quoted value in a CRLF file still gets the quote warning", func() {
		// docker strips the '\r' but not the quotes.
		f := writeEnv(tmp, "A=1\r\nKEY=\"secret\"\r\n")
		ws, err := cascade.LintEnvFile(f)
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Kind).To(Equal(cascade.EnvWarningQuoted))
		Expect(ws[0].Key).To(Equal("KEY"))
		Expect(ws[0].Line).To(Equal(2))
		Expect(ws[0].Quote).To(Equal(byte('"')))
		Expect(text(ws)).To(ContainSubstring(f + ":2: value for KEY is wrapped in \" quotes."))
	})

	DescribeTable("CS-CASC-018: skips non-assignment lines",
		func(line string) {
			ws, err := cascade.LintEnvFile(writeEnv(tmp, line+"\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ws).To(BeEmpty())
		},
		Entry("comment", `# KEY="x"`),
		Entry("blank line", ""),
		Entry("not an assignment", "NOEQUALS"),
	)

	It("CS-CASC-019: counts every line, including skipped ones", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "# comment\n\nKEY=\"quoted\"\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Line).To(Equal(3))
	})

	It("CS-CASC-019: reports the correct line for a file with no trailing newline", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "A=1\nKEY=\"quoted\""))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Line).To(Equal(2))
	})

	It("CS-CASC-013: takes the key from the first '=' and keeps the rest as the value", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "KEY=\"a=b\"\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Key).To(Equal("KEY"))
	})

	It("CS-CASC-013: reports a missing file as an error rather than warnings", func() {
		ws, err := cascade.LintEnvFile(filepath.Join(tmp, "absent"))
		Expect(err).To(HaveOccurred())
		Expect(ws).To(BeEmpty())
	})

	It("CS-CASC-026: the shared reader trims leading whitespace, so an indented quoted value is reported under its key", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "  KEY=\"x\"\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Key).To(Equal("KEY"))
		Expect(ws[0].Line).To(Equal(1))
	})

	It("CS-CASC-026: an indented comment is skipped", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "  # KEY=\"x\"\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(BeEmpty())
	})

	It("CS-CASC-026: a UTF-8 BOM on line 1 is not part of the key", func() {
		ws, err := cascade.LintEnvFile(writeEnv(tmp, "\xEF\xBB\xBFKEY=\"x\"\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ws).To(HaveLen(1))
		Expect(ws[0].Key).To(Equal("KEY"))
	})
})

// Spec: spec/launch.feature (CS-LNCH-108) — EnvFilesDefine is the launcher's
// "would docker set this key" check, on the shared reader.
var _ = Describe("EnvFilesDefine", func() {
	var tmp string
	host := map[string]string{}
	lookup := func(k string) (string, bool) { v, ok := host[k]; return v, ok }

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		for k := range host {
			delete(host, k)
		}
	})

	DescribeTable("CS-LNCH-108: reports a key docker would set from the files",
		func(content string, hostSet, want bool) {
			if hostSet {
				host["K"] = ""
			}
			f := writeEnv(tmp, content)
			Expect(cascade.EnvFilesDefine([]string{f}, "K", lookup)).To(Equal(want))
		},
		Entry("plain assignment", "K=v\n", false, true),
		Entry("empty value", "K=\n", false, true),
		Entry("indented", " \tK=v\n", false, true),
		Entry("BOM on the first line", "\xEF\xBB\xBFK=v\n", false, true),
		Entry("CRLF", "A=1\r\nK=v\r\n", false, true),
		Entry("bare key, host sets it (even to \"\")", "K\n", true, true),
		Entry("bare CRLF key, host sets it", "K\r\n", true, true),
		Entry("bare key, host lacks it", "K\n", false, false),
		Entry("bare key ending in \\r\\r", "K\r\r\n", true, false),
		Entry("comment", "  # K=v\n", false, false),
		Entry("blank before '='", "K =v\n", false, false),
		Entry("another key only", "KK=v\nk=v\n", false, false),
	)

	It("CS-LNCH-108: a nil lookup never resolves a bare key", func() {
		f := writeEnv(tmp, "K\n")
		Expect(cascade.EnvFilesDefine([]string{f}, "K", nil)).To(BeFalse())
	})

	It("CS-LNCH-108: checks every file and skips unreadable ones", func() {
		f := writeEnv(tmp, "K=v\n")
		missing := filepath.Join(tmp, "missing")
		Expect(cascade.EnvFilesDefine([]string{missing, f}, "K", nil)).To(BeTrue())
	})

	It("CS-LNCH-108: a key docker rejects never matches, even on its own line", func() {
		f := writeEnv(tmp, "=v\nA B=v\n")
		Expect(cascade.EnvFilesDefine([]string{f}, "", nil)).To(BeFalse())
		Expect(cascade.EnvFilesDefine([]string{f}, "A B", nil)).To(BeFalse())
	})
})
