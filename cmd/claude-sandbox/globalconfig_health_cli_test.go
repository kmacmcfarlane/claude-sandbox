package main

// Spec: spec/global-config.feature CS-GCFG-001 and CS-GCFG-014 — where the
// launcher runs the global-config health check: before every session on a
// host launch, again after the primary session, never inside a sandbox.
// The fixture's HOME and Env.StateDir are scratch directories; execx.Fake
// scripts docker. The check itself is tested in internal/globalcfg.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
)

var _ = Describe("global-config health check wiring (CS-GCFG-001, CS-GCFG-014)", func() {
	const good = `{"projects":{"a":{},"b":{}},"oauthAccount":{"emailAddress":"secret-value"},"hasCompletedOnboarding":true}`
	const damaged = `{"userID":"x"}`
	var f *cliFixture
	var file string
	BeforeEach(func() {
		f = newCLIFixture()
		file = filepath.Join(f.home, ".claude.json")
	})
	snapshots := func() []string {
		s := globalcfg.Store{Dir: filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(file))}
		return s.List(globalcfg.SnapshotPrefix)
	}
	// withBaseline records a snapshot of the good file, as an earlier launch would.
	withBaseline := func() {
		writeFile(file, good)
		globalcfg.CheckHealth(globalcfg.HealthOptions{Home: f.home, StateRoot: f.state})
		Expect(snapshots()).To(HaveLen(1))
	}
	// damageDuring makes the session child damage the file while it "runs".
	damageDuring := func(pattern string, err error) {
		f.fake.OnFunc(pattern, func(execx.Cmd) (string, error) {
			Expect(os.WriteFile(file, []byte(damaged), 0o600)).To(Succeed())
			return "", err
		})
	}
	warnings := func() int { return strings.Count(f.errw.String(), "looks damaged") }

	It("CS-GCFG-001: a new container's launch snapshots before the session; nothing about the container changes", func() {
		writeFile(file, good)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(snapshots()).To(HaveLen(1))
		Expect(f.launchLine()).NotTo(ContainSubstring(f.state))
		Expect(f.errw.String()).NotTo(ContainSubstring("global config"))
	})

	It("CS-GCFG-001, CS-GCFG-014: damage during a new container's session is reported after it ends", func() {
		withBaseline()
		f.fake.On("docker inspect --type container", "exited 2026-09-24T00:00:00Z\n", nil)
		damageDuring("docker start", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(warnings()).To(Equal(1))
		Expect(f.errw.String()).To(ContainSubstring(globalcfg.AcceptCmd))
		Expect(f.errw.String()).NotTo(ContainSubstring("secret-value"))
		Expect(f.out.String()).NotTo(ContainSubstring("looks damaged"))
	})

	It("CS-GCFG-014: damage found before the session is not repeated after it", func() {
		withBaseline()
		writeFile(file, damaged)
		f.fake.On("docker inspect --type container", "exited 2026-09-24T00:00:00Z\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(warnings()).To(Equal(1))
		Expect(os.ReadFile(file)).To(BeEquivalentTo(damaged), "never restored automatically")
	})

	It("CS-GCFG-001: ralph and --branch launches are checked too", func() {
		for _, args := range [][]string{{"--ralph"}, {"--branch"}} {
			g := newCLIFixture()
			f, file = g, filepath.Join(g.home, ".claude.json")
			withBaseline()
			writeFile(file, damaged)
			Expect(g.run(args...)).To(Equal(0), g.errw.String())
			Expect(warnings()).To(Equal(1), strings.Join(args, " "))
		}
	})

	It("CS-GCFG-001: headless: the warning is on stderr, stdout stays claude's", func() {
		withBaseline()
		writeFile(file, damaged)
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		Expect(warnings()).To(Equal(1))
		Expect(f.out.String()).To(BeEmpty())
	})

	It("CS-GCFG-001: a signal the launcher forwarded skips the check after the session", func() {
		withBaseline()
		f.fake.SessionSignal = syscall.SIGTERM
		damageDuring("docker start", execx.Fail(143))
		f.run("headless", "--")
		Expect(warnings()).To(BeZero())
	})

	It("CS-GCFG-001: --detach checks before the start only", func() {
		saved := detachedSettle
		detachedSettle = 20 * time.Millisecond
		DeferCleanup(func() { detachedSettle = saved })
		withBaseline()
		writeFile(file, damaged)
		f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		Expect(warnings()).To(Equal(1))
	})

	It("CS-GCFG-001: an attach is checked before and after; damage during it is reported", func() {
		withBaseline()
		f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		damageDuring("docker attach", nil)
		Expect(f.run("--attach=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
		Expect(warnings()).To(Equal(1))
	})

	It("CS-GCFG-001: a join is checked before, not after (it is not the primary session)", func() {
		withBaseline()
		f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		damageDuring("docker exec", nil)
		Expect(f.run("--join=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
		Expect(warnings()).To(BeZero())

		writeFile(file, damaged)
		f.errw.Reset()
		Expect(f.run("--join=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
		Expect(warnings()).To(Equal(1))
	})

	It("CS-GCFG-001: a launcher inside a sandbox does not run the check", func() {
		writeFile(file, damaged)
		f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
		f.run()
		Expect(filepath.Join(f.state, globalcfg.StoreDirName)).NotTo(BeADirectory())
		Expect(f.errw.String()).NotTo(ContainSubstring("global config " + file))
	})
})
