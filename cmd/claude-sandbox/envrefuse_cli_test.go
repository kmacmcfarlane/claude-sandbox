package main

// Spec: spec/launch.feature CS-LNCH-129..131 — the launcher refuses a launch
// whose cascade env files define a loader or shell-startup variable, before it
// builds an image or creates a container. Fail closed: env files are
// session-writable and reach the root entrypoint's own bash before it can drop
// the variable (CS-IMG-067), so a planted LD_PRELOAD would load a .so as root.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// envFileArgs returns each --env-file path of a create argv, in order.
func envFileArgs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--env-file" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// envFileContents reads each --env-file copy of a create argv (CS-LNCH-132:
// docker gets verbatim copies from the shadow directory, never the originals).
func envFileContents(args []string) []string {
	var out []string
	for _, p := range envFileArgs(args) {
		out = append(out, readFile(p))
	}
	return out
}

var _ = Describe("env files reach docker as the bytes the launcher checked (CS-LNCH-132)", func() {
	var f *cliFixture

	BeforeEach(func() { f = newCLIFixture() })

	// plantAfterCheck rewrites the env file the first time an image inspect
	// runs — after the refusal check, before docker create.
	plantAfterCheck := func(g *cliFixture, envFile string) {
		g.fake.OnFunc("image inspect claude-sandbox:run", func(execx.Cmd) (string, error) {
			writeFile(envFile, "LD_PRELOAD=/p/evil.so\n")
			return "", nil
		})
	}

	// expectSnapshot asserts every --env-file of the create is a 0600 copy in
	// the shadow directory holding the checked bytes, and none the original.
	expectSnapshot := func(g *cliFixture, envFile string, want ...string) {
		args := g.launched().Args
		paths := envFileArgs(args)
		Expect(paths).To(HaveLen(len(want)))
		Expect(envFileContents(args)).To(Equal(want))
		for _, p := range paths {
			Expect(p).To(HavePrefix(g.tmp+"/"), "copy lives in the shadow directory")
			Expect(p).NotTo(Equal(envFile))
			st, err := os.Stat(p)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		}
		Expect(args).NotTo(ContainElement(envFile))
	}

	DescribeTable("a file rewritten after the check does not reach docker",
		func(launchArgs ...string) {
			// --detach inspects the container after its settle (CS-LNCH-119);
			// keep that short and report it running.
			saved := detachedSettle
			detachedSettle = 20 * time.Millisecond
			DeferCleanup(func() { detachedSettle = saved })
			f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
			envFile := filepath.Join(f.proj, ".claude-sandbox", "env")
			writeFile(envFile, "TOKEN=a\n")
			plantAfterCheck(f, envFile)
			Expect(f.run(launchArgs...)).To(Equal(0), f.errw.String())
			expectSnapshot(f, envFile, "TOKEN=a\n")
			// The planted line was written, and would have been what a re-read passes.
			Expect(readFile(envFile)).To(Equal("LD_PRELOAD=/p/evil.so\n"))
		},
		Entry("interactive"),
		Entry("ralph", "--ralph"),
		Entry("headless", "headless", "--"),
		Entry("detached", "--detach"),
	)

	It("copies are in cascade order and messages name the original paths", func() {
		parent := filepath.Dir(f.proj)
		up := filepath.Join(parent, ".claude-sandbox", "env")
		local := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(up, "TOKEN=up\n")
		writeFile(local, "TOKEN=local\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(envFileContents(f.launched().Args)).To(Equal([]string{"TOKEN=up\n", "TOKEN=local\n"}))
		// The cascade report and override notice keep the real paths.
		Expect(f.out.String()).To(ContainSubstring(parent + "/.claude-sandbox/"))
		Expect(f.out.String()).To(ContainSubstring("TOKEN in " + local + " overrides " + up))
	})

	It("an env file that cannot be read fails the launch before any image work", func() {
		envFile := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(envFile, "TOKEN=a\n")
		Expect(os.Chmod(envFile, 0o000)).To(Succeed())
		DeferCleanup(os.Chmod, envFile, os.FileMode(0o644))
		if _, err := os.ReadFile(envFile); err == nil {
			Skip("running as a user that can read a 0000 file")
		}
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("reading env file"))
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker build"))
			Expect(l).NotTo(HavePrefix("docker create"))
		}
	})
})

var _ = Describe("refusing loader/shell env keys at launch (CS-LNCH-129..131)", func() {
	var f *cliFixture

	BeforeEach(func() { f = newCLIFixture() })

	projEnv := func(g *cliFixture) string {
		return filepath.Join(g.proj, ".claude-sandbox", "env")
	}

	// createRan reports whether any "docker create" was recorded.
	createRan := func(g *cliFixture) bool {
		for _, c := range g.fake.Calls {
			if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "create" {
				return true
			}
		}
		return false
	}

	// buildRan reports whether any "docker build" was recorded.
	buildRan := func(g *cliFixture) bool {
		for _, l := range g.fake.CommandLines() {
			if strings.HasPrefix(l, "docker build") {
				return true
			}
		}
		return false
	}

	It("CS-LNCH-129: a refused key fails the launch before any docker create or build", func() {
		env := projEnv(f)
		writeFile(env, "LD_PRELOAD=/home/u/evil.so\n")

		Expect(f.run()).To(Equal(2))

		msg := f.errw.String()
		Expect(msg).To(ContainSubstring(env + ":1: LD_PRELOAD"))
		Expect(msg).To(ContainSubstring("root entrypoint"))
		Expect(msg).To(ContainSubstring("shell rc"))
		// The secret value is never echoed.
		Expect(msg).NotTo(ContainSubstring("/home/u/evil.so"))

		Expect(createRan(f)).To(BeFalse(), "must not reach docker create")
		Expect(buildRan(f)).To(BeFalse(), "must not build an image")
		Expect(f.fake.Session).To(BeNil())
	})

	It("CS-LNCH-129: a launch whose env files carry no refused key is unaffected", func() {
		writeFile(projEnv(f), "TOKEN=ok\nDISCORD_WEBHOOK_URL=https://x\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(createRan(f)).To(BeTrue())
	})

	It("CS-LNCH-129: reports every refused line across the cascade", func() {
		parent := filepath.Dir(f.proj)
		up := filepath.Join(parent, ".claude-sandbox", "env")
		local := projEnv(f)
		writeFile(up, "GCONV_PATH=/p/g\n")
		writeFile(local, "BASH_ENV=/p/rc\nLD_AUDIT=/p/a.so\n")

		Expect(f.run()).To(Equal(2))
		msg := f.errw.String()
		Expect(msg).To(ContainSubstring(up + ":1: GCONV_PATH"))
		Expect(msg).To(ContainSubstring(local + ":1: BASH_ENV"))
		Expect(msg).To(ContainSubstring(local + ":2: LD_AUDIT"))
	})

	It("CS-LNCH-130: ralph is refused the same way", func() {
		writeFile(projEnv(f), "BASH_ENV=/tmp/rc\n")
		Expect(f.run("--ralph")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("BASH_ENV"))
		Expect(createRan(f)).To(BeFalse())
	})

	It("CS-LNCH-130: headless is refused with the message on stderr and stdout clean", func() {
		writeFile(projEnv(f), "BASH_ENV=/tmp/rc\n")
		Expect(f.run("headless", "--")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("BASH_ENV"))
		Expect(f.out.String()).NotTo(ContainSubstring("BASH_ENV"))
		Expect(createRan(f)).To(BeFalse())
	})

	It("CS-LNCH-131: attach carries no env file, so a refused key does not block it", func() {
		writeFile(projEnv(f), "LD_AUDIT=/x.so\n")
		f.fake.On("docker ps", psRow("cs-a", "Up 2 hours", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)

		Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
		Expect(f.sessionLine()).To(HaveSuffix("cs-a"))
		Expect(createRan(f)).To(BeFalse()) // no docker create at all
		Expect(strings.Join(f.fake.Session.Args, " ")).NotTo(ContainSubstring("--env-file"))
	})

	It("CS-LNCH-131: join carries no env file, so a refused key does not block it", func() {
		writeFile(projEnv(f), "LD_AUDIT=/x.so\n")
		f.env.Prompter = &prompt.Scripted{IsTTY: true}
		f.fake.On("docker ps", psRow("cs-a", "Up 2 hours", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)

		Expect(f.run("--join=otter")).To(Equal(0), f.errw.String())
		line := f.sessionLine()
		Expect(line).To(ContainSubstring("docker exec"))
		Expect(line).NotTo(ContainSubstring("--env-file"))
	})
})
