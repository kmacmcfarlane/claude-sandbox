package main

// Spec: spec/launch.feature CS-LNCH-185..191, CS-LNCH-193 and
// spec/sessions.feature CS-SESS-097 — the launcher network end to end through
// MainWithEnv. newCLIFixture stays key-off (no docker network call at all);
// these tests turn the key on per path. The network stubs are registered
// before any broad pattern and begin "docker network inspect" / "docker
// network create": none of the fixture's broad patterns ("docker inspect",
// "docker ps", "docker create") is a substring of a "docker network …" line.

import (
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

const (
	netInspectPat = "docker network inspect --format {{.Driver}}"
	netBridgePat  = "docker network inspect --format {{json .Options}} bridge"
	netCreatePat  = "docker network create"
	netOurs       = "bridge\x1ffalse\x1f1\n"
	netForeign    = "bridge\x1ffalse\x1f<no value>\n"
	netNotFound   = "Error response from daemon: network claude-sandbox not found"
)

// netStderr answers with stderr and exit 1.
func netStderr(msg string) func(execx.Cmd) (string, error) {
	return func(c execx.Cmd) (string, error) {
		if c.Stderr != nil {
			c.Stderr.Write([]byte(msg + "\n"))
		}
		return "", execx.Fail(1)
	}
}

// networkOn turns the key on for f and scripts the network as ours.
func networkOn(f *cliFixture) {
	f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
	f.fake.On(netInspectPat, netOurs, nil)
}

func netLines(f *cliFixture) []string {
	var out []string
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, "docker network ") {
			out = append(out, l)
		}
	}
	return out
}

func netInspects(f *cliFixture) int {
	n := 0
	for _, l := range netLines(f) {
		if strings.HasPrefix(l, netInspectPat) {
			n++
		}
	}
	return n
}

// expectOnNetwork asserts the last create joined the network right after
// --init, and the launch printed the banner once.
func expectOnNetwork(f *cliFixture, what string) {
	args := f.launched().Args
	i := slices.Index(args, "--init")
	Expect(i).To(BeNumerically(">", 0), what)
	Expect(args[i+1:i+3]).To(Equal([]string{"--network", "claude-sandbox"}), what)
	Expect(args).NotTo(ContainElement("--dns"), what)
	Expect(strings.Count(f.out.String()+f.errw.String(), "Network: claude-sandbox - ")).To(Equal(1), what)
}

func expectDefaultBridge(f *cliFixture) {
	Expect(f.launched().Args).NotTo(ContainElement("--network"))
	Expect(f.out.String() + f.errw.String()).NotTo(ContainSubstring("Network: claude-sandbox"))
}

var _ = Describe("the launcher network (CS-LNCH-185..191, CS-LNCH-193)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	Describe("CS-LNCH-193: every create path", func() {
		It("CS-LNCH-193: interactive", func() {
			networkOn(f)
			Expect(f.run()).To(Equal(0), f.errw.String())
			expectOnNetwork(f, "interactive")
			Expect(f.out.String()).To(ContainSubstring(launch.NetworkBanner + "\n"))
		})

		It("CS-LNCH-193: ralph", func() {
			networkOn(f)
			Expect(f.run("--ralph")).To(Equal(0), f.errw.String())
			expectOnNetwork(f, "ralph")
		})

		It("CS-LNCH-193: --branch, the [b] fork and the tier-1 [n]", func() {
			for _, c := range []struct {
				args []string
				tty  []string
			}{{args: []string{"--branch"}}, {tty: []string{"b"}}, {tty: []string{"n"}}} {
				g := newCLIFixture()
				networkOn(g)
				g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
				g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
				g.env.Prompter = &prompt.Scripted{IsTTY: c.tty != nil, Answers: c.tty}
				Expect(g.run(c.args...)).To(Equal(0), "%v %v: %s", c.args, c.tty, g.errw.String())
				expectOnNetwork(g, strings.Join(append(c.args, c.tty...), " "))
			}
		})

		It("CS-LNCH-193: --detach", func() {
			saved := detachedSettle
			detachedSettle = 20 * time.Millisecond
			DeferCleanup(func() { detachedSettle = saved })
			networkOn(f)
			f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run("--detach")).To(Equal(0), f.errw.String())
			expectOnNetwork(f, "--detach")
		})

		It("CS-LNCH-193: headless prints the line on stderr only; stdout stays empty", func() {
			networkOn(f)
			Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
			args := f.launched().Args
			Expect(args[:6]).To(Equal([]string{"create", "-i", "--rm", "--init", "--network", "claude-sandbox"}))
			Expect(f.out.String()).To(BeEmpty())
			Expect(strings.Count(f.errw.String(), "Network: claude-sandbox - ")).To(Equal(1))
		})

		It("CS-LNCH-193: a nested launch makes the same calls", func() {
			networkOn(f)
			f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
			f.envmap["TMPDIR"] = f.home
			Expect(f.run()).To(Equal(0), f.errw.String())
			expectOnNetwork(f, "nested")
			Expect(netInspects(f)).To(Equal(1))
		})
	})

	It("CS-LNCH-185: the ensure step runs under the lock, before discovery and the create", func() {
		networkOn(f)
		Expect(f.run()).To(Equal(0), f.errw.String())
		held := f.lock.held()
		Expect(held[0]).To(HavePrefix(netInspectPat), "first under the lock")
		Expect(held[1]).To(HavePrefix("docker ps -a "))
		Expect(held[len(held)-1]).To(HavePrefix("docker create -it --rm --init --network claude-sandbox "))
		Expect(netLines(f)).To(HaveLen(1), "ours: one inspect, no options read")
	})

	It("CS-LNCH-185: a missing network is created under the lock with the default bridge's non-default options", func() {
		f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
		f.fake.OnFunc(netInspectPat, netStderr(netNotFound))
		f.fake.On(netBridgePat, `{"com.docker.network.bridge.enable_icc":"false","com.docker.network.driver.mtu":"1500"}`, nil)
		f.fake.On(netCreatePat, "abc\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		held := f.lock.held()
		Expect(held[:3]).To(Equal([]string{
			held[0],
			netBridgePat,
			"docker network create --driver bridge --label claude-sandbox.network=1 -o com.docker.network.bridge.enable_icc=false claude-sandbox",
		}))
		expectOnNetwork(f, "created")
	})

	It("CS-LNCH-185, CS-LNCH-193: a noun re-pick reuses the ensure step and prints the line once", func() {
		networkOn(f)
		attempts := conflictOn(f, 1)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(*attempts).To(Equal(2))
		Expect(netInspects(f)).To(Equal(1))
		expectOnNetwork(f, "re-pick")
	})

	Describe("CS-LNCH-188: opt-in", func() {
		It("CS-LNCH-188: the unset key makes no docker network call and leaves the argv alone", func() {
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(netLines(f)).To(BeEmpty())
			expectDefaultBridge(f)
		})

		It("CS-LNCH-188: sandboxNetwork: true turns it on; CLAUDE_SANDBOX_NETWORK=0 turns it off for one launch", func() {
			writeFile(f.proj+"/.claude-sandbox/config.yaml", "sandboxNetwork: true\n")
			f.fake.On(netInspectPat, netOurs, nil)
			Expect(f.run()).To(Equal(0), f.errw.String())
			expectOnNetwork(f, "config true")

			g := newCLIFixture()
			writeFile(g.proj+"/.claude-sandbox/config.yaml", "sandboxNetwork: true\n")
			g.envmap["CLAUDE_SANDBOX_NETWORK"] = "0"
			g.fake.On(netInspectPat, netOurs, nil)
			Expect(g.run()).To(Equal(0), g.errw.String())
			Expect(netLines(g)).To(BeEmpty())
			expectDefaultBridge(g)
		})
	})

	// keyOffHash is the confighash label of a key-off launch in f's tree.
	keyOffHash := func(g *cliFixture) string {
		h := newCLIFixture()
		h.home, h.proj = g.home, g.proj
		h.envmap["HOME"], h.envmap["PROJECT_DIR"] = g.home, g.proj
		Expect(h.run()).To(Equal(0), h.errw.String())
		return labelsOf(h.launched().Args)["claude-sandbox.confighash"]
	}

	It("CS-LNCH-186: a foreign network warns once and launches exactly as with the key off", func() {
		f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
		f.fake.On(netInspectPat, netForeign, nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("WARNING: a Docker network named claude-sandbox exists but was not created by claude-sandbox (no claude-sandbox.network=1 label); launching on the default bridge."))
		Expect(strings.Count(f.errw.String(), "WARNING: a Docker network")).To(Equal(1))
		expectDefaultBridge(f)
		Expect(netLines(f)).To(HaveLen(1), "nothing created")
		Expect(labelsOf(f.launched().Args)["claude-sandbox.confighash"]).To(Equal(keyOffHash(f)))
	})

	It("CS-LNCH-187: a docker failure warns once and launches on the default bridge", func() {
		f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
		f.fake.OnFunc(netInspectPat, netStderr("Cannot connect to the Docker daemon"))
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("WARNING: could not inspect the claude-sandbox network (Cannot connect to the Docker daemon); launching on the default bridge.\n"))
		expectDefaultBridge(f)
	})

	It("CS-LNCH-187: headless stand-down keeps stdout empty; the warning is on stderr", func() {
		f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
		f.fake.On(netInspectPat, netForeign, nil)
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(BeEmpty())
		Expect(f.errw.String()).To(ContainSubstring("WARNING: a Docker network named claude-sandbox"))
		expectDefaultBridge(f)
	})

	Describe("CS-LNCH-190: a vanished network", func() {
		vanishOn := func(n int) *int {
			calls := 0
			f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				calls++
				if n < 0 || calls <= n {
					c.Stderr.Write([]byte("Error response from daemon: network claude-sandbox not found\n"))
					return "", execx.Fail(125)
				}
				return "abc123\n", nil
			})
			return &calls
		}

		It("CS-LNCH-190: re-ensures once and retries", func() {
			networkOn(f)
			calls := vanishOn(1)
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(*calls).To(Equal(2))
			Expect(netInspects(f)).To(Equal(2))
			expectOnNetwork(f, "retried")
		})

		It("CS-LNCH-190: a second miss exits 2 as any create error", func() {
			networkOn(f)
			calls := vanishOn(-1)
			Expect(f.run()).To(Equal(2))
			Expect(*calls).To(Equal(2))
			Expect(netInspects(f)).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("network claude-sandbox not found"))
		})

		It("CS-LNCH-190: the retry does not count against the name-conflict attempts", func() {
			networkOn(f)
			calls := 0
			f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				calls++
				switch calls {
				case 1:
					c.Stderr.Write([]byte(netNotFound + "\n"))
					return "", execx.Fail(125)
				case 2, 3:
					c.Stderr.Write([]byte(`docker: Error response from daemon: Conflict. The container name "/x" is already in use by container "abc".`))
					return "", execx.Fail(125)
				}
				return "abc123\n", nil
			})
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(calls).To(Equal(4))
		})
	})
})

var _ = Describe("drift and the launcher network (CS-LNCH-189, CS-SESS-097)", func() {
	var (
		f            *cliFixture
		hash, inputs string
	)

	// launchKeyOff launches a container with the key off and keeps its
	// labels, then gives the fixture a fresh fake for the attach.
	BeforeEach(func() {
		f = newCLIFixture()
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		l := labelsOf(f.launched().Args)
		hash, inputs = l["claude-sandbox.confighash"], l["claude-sandbox.inputs"]
		Expect(hash).NotTo(BeEmpty())
		f.fake = &execx.Fake{}
		f.env.Runner = f.fake
		f.lock.fake = f.fake
		f.lock.acquiredAt, f.lock.releasedAt = nil, nil
		f.out.Reset()
		f.errw.Reset()
	})
	running := func() {
		f.fake.On("docker ps", psRowFull("cs-otter", "Up 1 hour", f.proj, "otter", "", hash, inputs)+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
	}
	tty := func(answers ...string) { f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: answers} }

	It("CS-SESS-097: key off: equal hashes, no drift, no docker network call", func() {
		running()
		Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("different configuration"))
		Expect(netLines(f)).To(BeEmpty())
	})

	It("CS-SESS-097, CS-LNCH-189: key on and the network ours: drift names <network> beside the shadow CLAUDE.md, one inspect", func() {
		networkOn(f)
		running()
		tty("c")
		Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
		e := f.errw.String()
		Expect(e).To(ContainSubstring("Session 'otter' was started with different configuration:"))
		Expect(e).To(ContainSubstring("  added    network     <network>\n"))
		Expect(e).To(ContainSubstring("  changed  shadow      CLAUDE.md\n"), "an opted-in tree also shows the context paragraph")
		Expect(netLines(f)).To(HaveLen(1))
	})

	It("CS-SESS-097: key on and the network missing: the drift stands", func() {
		f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
		f.fake.OnFunc(netInspectPat, netStderr(netNotFound))
		running()
		tty("c")
		Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("<network>"))
		Expect(netLines(f)).To(HaveLen(1), "the drift check creates nothing")
	})

	It("CS-SESS-097: a join with the key on reports the same drift", func() {
		networkOn(f)
		running()
		tty("c")
		Expect(f.run("--join=otter")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("added    network     <network>"))
	})

	DescribeTable("CS-SESS-097, CS-LNCH-189: a foreign network or an inspect error is no drift; no terminal does not exit 3",
		func(stub func()) {
			f.envmap["CLAUDE_SANDBOX_NETWORK"] = "1"
			stub()
			running()
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring("different configuration"))
			Expect(f.sessionLine()).To(HavePrefix("docker attach "))
			Expect(netLines(f)).To(HaveLen(1))
		},
		Entry("foreign", func() { f.fake.On(netInspectPat, netForeign, nil) }),
		Entry("inspect error", func() { f.fake.OnFunc(netInspectPat, netStderr("Cannot connect to the Docker daemon")) }),
	)

	It("CS-LNCH-189: --allow-config-drift makes no network call", func() {
		networkOn(f)
		running()
		Expect(f.run("--attach=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
		Expect(netLines(f)).To(BeEmpty())
	})

	It("CS-LNCH-189: wouldBeFingerprint builds with the network and makes no docker network call", func() {
		networkOn(f)
		fl, err := scanLaunchArgs(nil)
		Expect(err).NotTo(HaveOccurred())
		configFiles, err := paths.CollectUp(f.proj, paths.Config)
		Expect(err).NotTo(HaveOccurred())
		cfg, err := cascade.Load(configFiles)
		Expect(err).NotTo(HaveOccurred())
		on, onInputs := wouldBeFingerprint(f.env, f.proj, fl, cfg, nil, nil, nil, nil, nil)
		Expect(onInputs).To(ContainElement(launch.InputDigest{Path: "<network>", Digest: onInputs[len(onInputs)-1].Digest, Kind: launch.KindNetwork}))
		Expect(netLines(f)).To(BeEmpty())
		off, _ := wouldBeFingerprintNet(f.env, f.proj, fl, cfg, nil, nil, nil, nil, nil, false)
		Expect(off).To(Equal(hash), "network off = the key-off launch")
		Expect(on).NotTo(Equal(hash))
	})
})
