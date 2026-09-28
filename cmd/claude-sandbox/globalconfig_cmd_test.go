package main

// Spec: spec/global-config.feature CS-GCFG-041..055 — the global-config
// subcommand's wiring: routing, exit codes, the sandbox refusal and the
// Env.StateDir seam. The behaviour itself is tested in internal/globalcfg.
// execx.Fake throughout; HOME and the state root are the fixture's scratch
// directories.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
)

var _ = Describe("global-config subcommand (CS-GCFG-041..055)", func() {
	var f *cliFixture
	var link, target string
	BeforeEach(func() {
		f = newCLIFixture()
		link = filepath.Join(f.home, ".claude.json")
		target = filepath.Join(f.home, ".claude", ".claude.json")
		Expect(os.MkdirAll(filepath.Dir(target), 0o700)).To(Succeed())
		f.fake.On("claude --version", globalcfg.VerifiedClaudeVersion+"\n", nil)
	})

	It("CS-GCFG-041, CS-GCFG-052: migrate then revert round-trip through the CLI, keeping copies under Env.StateDir", func() {
		writeFile(link, `{"projects":{}}`)
		Expect(f.run("global-config", "migrate")).To(Equal(0), f.errw.String())
		Expect(globalcfg.Classify(f.home, "").Mode).To(Equal(globalcfg.ModeLinked))
		Expect(f.out.String()).To(ContainSubstring("Migrated: " + link))
		store := &globalcfg.Store{Dir: filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(link))}
		Expect(store.List(globalcfg.PreMigratePrefix)).To(HaveLen(1))

		Expect(f.run("global-config", "revert")).To(Equal(0), f.errw.String())
		Expect(globalcfg.Classify(f.home, "").Mode).To(Equal(globalcfg.ModeLegacy))
		Expect(store.List(globalcfg.RevertedPrefix)).To(HaveLen(1))
	})

	It("CS-GCFG-042: a refusal exits 1 with one Error line and changes nothing", func() {
		writeFile(link, `{"projects":`)
		Expect(f.run("global-config", "migrate")).To(Equal(1))
		Expect(f.errw.String()).To(HavePrefix("Error: global-config migrate: " + link + " does not parse"))
		Expect(readFileString(link)).To(Equal(`{"projects":`))
	})

	It("CS-GCFG-044: inside a sandbox every command refuses with exit 1, before the state dir is resolved", func() {
		writeFile(link, `{}`)
		f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
		f.env.StateDir = "" // resolving it under go test would panic
		for _, c := range []string{"migrate", "revert", "accept"} {
			f.errw.Reset()
			Expect(f.run("global-config", c)).To(Equal(1), c)
			Expect(f.errw.String()).To(ContainSubstring("Error: global-config " + c + ": refused inside a sandbox"))
		}
		Expect(f.fake.CommandLines()).To(BeEmpty())
	})

	It("CS-GCFG-045, CS-GCFG-047: --force is a flag of migrate and revert", func() {
		writeFile(link, `{}`)
		f.fake.On("docker ps", "cs-a\x1f"+link+"\n", nil)
		Expect(f.run("global-config", "migrate")).To(Equal(1))
		Expect(f.errw.String()).To(ContainSubstring("cs-a"))
		f.errw.Reset()
		Expect(f.run("global-config", "migrate", "--force")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("WARNING: --force"))
	})

	It("CS-GCFG-054: accept writes a snapshot under Env.StateDir", func() {
		writeFile(link, `{"projects":{"a":{}}}`)
		Expect(f.run("global-config", "accept")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("projects: 1"))
		store := &globalcfg.Store{Dir: filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(link))}
		Expect(store.List(globalcfg.SnapshotPrefix)).To(HaveLen(1))
	})

	It("CS-GCFG-041: global-config with no command, an unknown one, or an extra argument is a usage error (exit 2)", func() {
		Expect(f.run("global-config")).To(Equal(2))
		Expect(f.run("global-config", "frob")).To(Equal(2))
		Expect(f.run("global-config", "migrate", "extra")).To(Equal(2))
		Expect(f.run("global-config", "migrate", "--nope")).To(Equal(2))
		Expect(f.fake.CommandLines()).To(BeEmpty(), "never falls through to a launch")
	})

	It("CS-GCFG-041: the launcher help lists the commands", func() {
		Expect(f.run("--help")).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("global-config migrate|revert [--force]"))
		Expect(f.out.String()).To(ContainSubstring("global-config accept"))
	})

	It("CS-GCFG-055: a legacy launch prints no unmigrated reminder", func() {
		writeFile(link, `{}`)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.out.String() + f.errw.String()).NotTo(ContainSubstring("global-config"))
	})
})

func readFileString(p string) string {
	b, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}
