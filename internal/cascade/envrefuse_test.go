package cascade_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

var _ = Describe("refused env keys (CS-CASC-042..045)", func() {
	// write creates an env file under a fresh temp dir and returns its path.
	write := func(content string) string {
		dir := GinkgoT().TempDir()
		p := filepath.Join(dir, "env")
		Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
		return p
	}

	DescribeTable("CS-CASC-042: the refused keys are those glibc and bash act on first",
		func(key string, refused bool) {
			Expect(cascade.IsRefusedEnvKey(key)).To(Equal(refused))
		},
		// refused
		Entry("LD_PRELOAD", "LD_PRELOAD", true),
		Entry("LD_AUDIT", "LD_AUDIT", true),
		Entry("LD_LIBRARY_PATH", "LD_LIBRARY_PATH", true),
		Entry("LD_DEBUG_OUTPUT", "LD_DEBUG_OUTPUT", true),
		Entry("LD_PROFILE", "LD_PROFILE", true),
		Entry("any future LD_ name", "LD_SOMETHING", true),
		Entry("GLIBC_TUNABLES", "GLIBC_TUNABLES", true),
		Entry("GCONV_PATH", "GCONV_PATH", true),
		Entry("LOCPATH", "LOCPATH", true),
		Entry("BASH_ENV", "BASH_ENV", true),
		// not refused
		Entry("ENV", "ENV", false),
		Entry("MALLOC_CHECK_", "MALLOC_CHECK_", false),
		Entry("PATH", "PATH", false),
		Entry("LDFLAGS", "LDFLAGS", false),
		Entry("LD alone", "LD", false),
		Entry("prefix anchored at start", "MY_LD_PRELOAD", false),
		Entry("case-sensitive", "ld_preload", false),
	)

	DescribeTable("CS-CASC-043: detection reads env files as docker does",
		func(line string, refused bool) {
			got := cascade.RefusedEnvKeys(snap(write(line)))
			if refused {
				Expect(got).To(HaveLen(1))
				Expect(got[0].Key).To(Equal("LD_PRELOAD"))
			} else {
				Expect(got).To(BeEmpty())
			}
		},
		Entry("plain", "LD_PRELOAD=/p/e.so\n", true),
		Entry("indented with spaces", "  LD_PRELOAD=/p/e.so\n", true),
		Entry("indented with a tab", "\tLD_PRELOAD=/p/e.so\n", true),
		Entry("UTF-8 BOM on the first line", "\xEF\xBB\xBFLD_PRELOAD=/p/e.so\n", true),
		Entry("CRLF ending", "LD_PRELOAD=/p/e.so\r\n", true),
		Entry("empty value still sets the key", "LD_PRELOAD=\n", true),
		Entry("a comment is not a finding", "# LD_PRELOAD=/p/e.so\n", false),
		Entry("blank before '=' is a key docker rejects", "LD_PRELOAD =/p/e.so\n", false),
		Entry("different case", "ld_preload=/p/e.so\n", false),
	)

	It("CS-CASC-044: a bare refused key is refused whatever the environment holds", func() {
		// A bare key is refused with no lookup at all — RefusedEnvKeys does not
		// consult the launcher's environment, unlike EnvFilesDefine.
		got := cascade.RefusedEnvKeys(snap(write("LD_PRELOAD\n")))
		Expect(got).To(HaveLen(1))
		Expect(got[0].Key).To(Equal("LD_PRELOAD"))
	})

	It("CS-CASC-045: every refused line in every file is reported by file, line and key", func() {
		up := write("TOKEN=t\nLD_AUDIT=/x.so\n")
		local := write("# c\nBASH_ENV=/p/rc\nLD_PRELOAD=/p/e.so\n")

		got := cascade.RefusedEnvKeys(snap(up, local))
		Expect(got).To(Equal([]cascade.EnvRefusal{
			{File: up, Line: 2, Key: "LD_AUDIT"},
			{File: local, Line: 2, Key: "BASH_ENV"},
			{File: local, Line: 3, Key: "LD_PRELOAD"},
		}))
	})

	It("CS-CASC-045: an unreadable file cannot be snapshotted, so the launch fails on it before any check", func() {
		_, err := cascade.ReadEnvFiles([]string{filepath.Join(GinkgoT().TempDir(), "does-not-exist")})
		Expect(err).To(HaveOccurred())
	})
})
