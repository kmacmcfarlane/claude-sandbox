package main

// Spec: spec/launch.feature CS-LNCH-231, CS-LNCH-236, CS-LNCH-238 end to end
// through MainWithEnv. The fixture's home is a scratch directory.

import (
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("AWS sign-in folders through the CLI (CS-LNCH-231, 236, 238)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-231: a bad tokenCache key exits 2 naming its file, before any image work", func() {
		cfgFile := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(cfgFile, "hostAccess:\n  aws:\n    tokenCache: shared\n")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring(`hostAccess.aws.tokenCache: "shared" in ` + cfgFile))
		Expect(createdAny(f)).To(BeFalse())
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker build"))
		}
	})

	It("CS-LNCH-231: a bad CLAUDE_SANDBOX_AWS_TOKEN_CACHE exits 2 naming the variable", func() {
		f.envmap[launch.AWSTokenCacheEnv] = "everywhere"
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring(`CLAUDE_SANDBOX_AWS_TOKEN_CACHE="everywhere"`))
		Expect(createdAny(f)).To(BeFalse())
	})

	It("CS-LNCH-236: every create passes the three evidence variables, empty with aws off", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		e := createFlag(f, "-e")
		Expect(e).To(ContainElements("CLAUDE_SANDBOX_AWS_HOME=", "CLAUDE_SANDBOX_AWS_CACHE=", "CLAUDE_SANDBOX_AWS_CACHE_DIR="))
	})

	It("CS-LNCH-238: headless prints the AWS: line on stderr only", func() {
		f.envmap["CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED"] = "1"
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(BeEmpty())
		Expect(strings.Count(f.errw.String(), "AWS: sign-ins here are shared")).To(Equal(1))
		Expect(createFlag(f, "-e")).To(ContainElement("CLAUDE_SANDBOX_AWS_CACHE=sandbox"))
	})
})
