package launch_test

// Spec: spec/launch.feature CS-LNCH-185..191, CS-LNCH-193 (the launcher
// network). EnsureNetwork and InspectNetwork run over execx.Fake: the network
// stubs use patterns beginning "docker network inspect" / "docker network
// create", with the network's name or "bridge" where the two inspects differ.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

const (
	netInspect    = "docker network inspect --format {{.Driver}}"
	bridgeInspect = "docker network inspect --format {{json .Options}} bridge"
	netCreate     = "docker network create"
	oursLine      = "bridge\x1ffalse\x1f1\n"
	hooperBridge  = `{"com.docker.network.bridge.default_bridge":"true","com.docker.network.bridge.enable_icc":"true","com.docker.network.bridge.enable_ip_masquerade":"true","com.docker.network.bridge.host_binding_ipv4":"0.0.0.0","com.docker.network.bridge.name":"docker0","com.docker.network.driver.mtu":"1500"}`
)

// stderrStub answers with stderr and a failing exit.
func stderrStub(msg string) func(execx.Cmd) (string, error) {
	return func(c execx.Cmd) (string, error) {
		if c.Stderr != nil {
			c.Stderr.Write([]byte(msg + "\n"))
		}
		return "", execx.Fail(1)
	}
}

func networkLines(f *execx.Fake) []string {
	var out []string
	for _, l := range f.CommandLines() {
		if strings.HasPrefix(l, "docker network ") {
			out = append(out, l)
		}
	}
	return out
}

func createLine(f *execx.Fake) string {
	for _, l := range f.CommandLines() {
		if strings.HasPrefix(l, netCreate) {
			return l
		}
	}
	return ""
}

var _ = Describe("the launcher network", func() {
	var (
		f    *execx.Fake
		warn *bytes.Buffer
	)
	BeforeEach(func() {
		f = &execx.Fake{}
		warn = &bytes.Buffer{}
	})

	Describe("EnsureNetwork", func() {
		It("CS-LNCH-185: an existing network of ours is used with one inspect and no options read", func() {
			f.On(netInspect, oursLine, nil)
			Expect(launch.EnsureNetwork(f, warn)).To(Equal("claude-sandbox"))
			Expect(networkLines(f)).To(HaveLen(1))
			Expect(networkLines(f)[0]).To(HaveSuffix(" claude-sandbox"))
			Expect(networkLines(f)[0]).To(ContainSubstring(`{{index .Labels "claude-sandbox.network"}}`))
			Expect(warn.String()).To(BeEmpty())
		})

		It("CS-LNCH-185: a stub fixture of only the network stubs records exactly the expected docker network lines", func() {
			f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
			f.On(bridgeInspect, hooperBridge, nil)
			f.On(netCreate, "abc\n", nil)
			Expect(launch.EnsureNetwork(f, warn)).To(Equal("claude-sandbox"))
			lines := networkLines(f)
			Expect(lines).To(HaveLen(3))
			Expect(lines[0]).To(HavePrefix(netInspect))
			Expect(lines[1]).To(Equal(bridgeInspect))
			// Hooper's docker0: every value at Docker's default, so no -o.
			Expect(lines[2]).To(Equal("docker network create --driver bridge --label claude-sandbox.network=1 claude-sandbox"))
			Expect(warn.String()).To(BeEmpty())
		})

		It("CS-LNCH-185: copies only the four keys, only where they differ from Docker's defaults, in a fixed order", func() {
			f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
			f.On(bridgeInspect, `{"com.docker.network.driver.mtu":"1400","com.docker.network.bridge.enable_icc":"false","com.docker.network.bridge.name":"docker0","com.docker.network.bridge.default_bridge":"true","com.docker.network.bridge.enable_ip_masquerade":"true"}`, nil)
			f.On(netCreate, "", nil)
			Expect(launch.EnsureNetwork(f, warn)).To(Equal("claude-sandbox"))
			Expect(createLine(f)).To(Equal("docker network create --driver bridge --label claude-sandbox.network=1 " +
				"-o com.docker.network.bridge.enable_icc=false -o com.docker.network.driver.mtu=1400 claude-sandbox"))
		})

		DescribeTable("CS-LNCH-185: one non-default key",
			func(opts, want string) {
				f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
				f.On(bridgeInspect, opts, nil)
				f.On(netCreate, "", nil)
				Expect(launch.EnsureNetwork(f, warn)).To(Equal("claude-sandbox"))
				Expect(createLine(f)).To(Equal("docker network create --driver bridge --label claude-sandbox.network=1 " + want + "claude-sandbox"))
			},
			Entry("masquerade off", `{"com.docker.network.bridge.enable_ip_masquerade":"false"}`, "-o com.docker.network.bridge.enable_ip_masquerade=false "),
			Entry("loopback binding", `{"com.docker.network.bridge.host_binding_ipv4":"127.0.0.1"}`, "-o com.docker.network.bridge.host_binding_ipv4=127.0.0.1 "),
			Entry("mtu 0 = the default", `{"com.docker.network.driver.mtu":"0"}`, ""),
			Entry("keys absent", `{}`, ""),
			Entry("null options", `null`, ""),
		)

		It("CS-LNCH-185: a default bridge that is not found creates with no -o", func() {
			f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
			f.OnFunc(bridgeInspect, stderrStub("Error response from daemon: network bridge not found"))
			f.On(netCreate, "", nil)
			Expect(launch.EnsureNetwork(f, warn)).To(Equal("claude-sandbox"))
			Expect(createLine(f)).To(Equal("docker network create --driver bridge --label claude-sandbox.network=1 claude-sandbox"))
			Expect(createLine(f)).NotTo(ContainSubstring("--subnet"))
			Expect(createLine(f)).NotTo(ContainSubstring("--internal"))
			Expect(createLine(f)).NotTo(ContainSubstring("--ipv6"))
			Expect(createLine(f)).NotTo(ContainSubstring("--dns"))
		})

		It("CS-LNCH-185: 'already exists' re-inspects once and never creates again", func() {
			n := 0
			f.OnFunc(netInspect, func(c execx.Cmd) (string, error) {
				n++
				if n == 1 {
					c.Stderr.Write([]byte("Error response from daemon: network claude-sandbox not found\n"))
					return "", execx.Fail(1)
				}
				return oursLine, nil
			})
			f.On(bridgeInspect, hooperBridge, nil)
			f.OnFunc(netCreate, stderrStub("Error response from daemon: network with name claude-sandbox already exists"))
			Expect(launch.EnsureNetwork(f, warn)).To(Equal("claude-sandbox"))
			creates := 0
			for _, l := range networkLines(f) {
				if strings.HasPrefix(l, netCreate) {
					creates++
				}
			}
			Expect(creates).To(Equal(1))
			Expect(n).To(Equal(2))
			Expect(warn.String()).To(BeEmpty())
		})

		It("CS-LNCH-186: 'already exists' then a foreign network warns", func() {
			n := 0
			f.OnFunc(netInspect, func(c execx.Cmd) (string, error) {
				n++
				if n == 1 {
					c.Stderr.Write([]byte("Error response from daemon: network claude-sandbox not found\n"))
					return "", execx.Fail(1)
				}
				return "bridge\x1ffalse\x1f<no value>\n", nil
			})
			f.On(bridgeInspect, hooperBridge, nil)
			f.OnFunc(netCreate, stderrStub("Error response from daemon: network with name claude-sandbox already exists"))
			Expect(launch.EnsureNetwork(f, warn)).To(BeEmpty())
			Expect(warn.String()).To(ContainSubstring("not created by claude-sandbox (no claude-sandbox.network=1 label)"))
		})

		DescribeTable("CS-LNCH-186: a foreign network is left alone with one warning",
			func(line, reason string) {
				f.On(netInspect, line, nil)
				Expect(launch.EnsureNetwork(f, warn)).To(BeEmpty())
				Expect(networkLines(f)).To(HaveLen(1))
				Expect(warn.String()).To(Equal("WARNING: a Docker network named claude-sandbox exists but was not created by claude-sandbox (" +
					reason + "); launching on the default bridge. Remove it (docker network rm claude-sandbox) or recreate it with --label claude-sandbox.network=1.\n"))
			},
			Entry("another driver", "overlay\x1ffalse\x1f1\n", "driver overlay"),
			Entry("internal", "bridge\x1ftrue\x1f1\n", "internal"),
			Entry("no label", "bridge\x1ffalse\x1f<no value>\n", "no claude-sandbox.network=1 label"),
			Entry("another label value", "bridge\x1ffalse\x1fyes\n", "no claude-sandbox.network=1 label"),
		)

		DescribeTable("CS-LNCH-187: any other failure stands down with one warning and creates nothing",
			func(setup func(), want string) {
				setup()
				Expect(launch.EnsureNetwork(f, warn)).To(BeEmpty())
				Expect(strings.Count(warn.String(), "WARNING")).To(Equal(1))
				Expect(warn.String()).To(ContainSubstring(want))
				Expect(warn.String()).To(HaveSuffix("; launching on the default bridge.\n"))
			},
			Entry("inspect error", func() {
				f.OnFunc(netInspect, stderrStub("Cannot connect to the Docker daemon at unix:///var/run/docker.sock."))
			}, "could not inspect the claude-sandbox network (Cannot connect to the Docker daemon"),
			Entry("empty inspect output", func() {
				f.On(netInspect, "", nil)
			}, "could not inspect the claude-sandbox network (unexpected output"),
			Entry("unparsable internal field", func() {
				f.On(netInspect, "bridge\x1fmaybe\x1f1\n", nil)
			}, "could not inspect the claude-sandbox network (unexpected output"),
			Entry("default bridge inspect error", func() {
				f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
				f.OnFunc(bridgeInspect, stderrStub("permission denied"))
			}, "could not read the default bridge's options (permission denied)"),
			Entry("default bridge bad JSON", func() {
				f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
				f.On(bridgeInspect, "{not json", nil)
			}, "could not read the default bridge's options (unexpected output"),
			Entry("an unparsable icc value", func() {
				f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
				f.On(bridgeInspect, `{"com.docker.network.bridge.enable_icc":"maybe"}`, nil)
			}, `could not read the default bridge's options (com.docker.network.bridge.enable_icc="maybe" does not parse)`),
			Entry("create error", func() {
				f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
				f.On(bridgeInspect, hooperBridge, nil)
				f.OnFunc(netCreate, stderrStub("Error response from daemon: could not find an available, non-overlapping IPv4 address pool"))
			}, "could not create the claude-sandbox network (Error response from daemon: could not find an available"),
		)

		It("CS-LNCH-187: an unreadable default bridge never reaches docker network create", func() {
			f.OnFunc(netInspect, stderrStub("Error response from daemon: network claude-sandbox not found"))
			f.On(bridgeInspect, `{"com.docker.network.driver.mtu":"big"}`, nil)
			Expect(launch.EnsureNetwork(f, warn)).To(BeEmpty())
			Expect(createLine(f)).To(BeEmpty())
		})
	})

	Describe("NetworkWanted", func() {
		yes, no := true, false
		DescribeTable("CS-LNCH-188: opt-in tri-state, default off",
			func(envVal string, key *bool, want bool) {
				getenv := func(k string) string {
					if k == "CLAUDE_SANDBOX_NETWORK" {
						return envVal
					}
					return ""
				}
				Expect(launch.NetworkWanted(getenv, &cascade.Config{SandboxNetwork: key})).To(Equal(want))
			},
			Entry("unset, unset", "", nil, false),
			Entry("unset, key true", "", &yes, true),
			Entry("env 1, key false", "1", &no, true),
			Entry("env yes, key unset", "yes", nil, true),
			Entry("env true, key unset", "true", nil, true),
			Entry("env 0 over key true", "0", &yes, false),
			Entry("env false over key true", "false", &yes, false),
			Entry("env no over key true", "no", &yes, false),
		)
		It("CS-LNCH-188: a nil config is off", func() {
			Expect(launch.NetworkWanted(func(string) string { return "" }, nil)).To(BeFalse())
		})
	})

	Describe("CreateError", func() {
		It("CS-LNCH-190: names a vanished network", func() {
			e := &launch.CreateError{Stderr: "Error response from daemon: network claude-sandbox not found\n"}
			Expect(e.NetworkMissing()).To(BeTrue())
			Expect((&launch.CreateError{Stderr: "Error response from daemon: network other not found"}).NetworkMissing()).To(BeFalse())
			Expect((&launch.CreateError{Stderr: "Conflict. The container name"}).NetworkMissing()).To(BeFalse())
		})
	})

	Describe("Build", func() {
		var (
			home, proj string
			in         launch.Inputs
			out        *bytes.Buffer
		)
		BeforeEach(func() {
			base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			home = filepath.Join(base, "home")
			proj = filepath.Join(base, "proj")
			mkdir(home)
			mkdir(proj)
			out = &bytes.Buffer{}
			in = launch.Inputs{
				ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
				Getenv:    func(string) string { return "" },
				TempDir:   filepath.Join(base, "shadow"),
				ImageName: "claude-sandbox-proj",
				Out:       out, Err: &bytes.Buffer{},
			}
			mkdir(in.TempDir)
		})
		build := func(network string) *launch.Plan {
			in.Network = network
			in.TempDir = filepath.Join(filepath.Dir(in.TempDir), "shadow-"+network+"x")
			mkdir(in.TempDir)
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		shadow := func() string {
			raw, err := os.ReadFile(filepath.Join(in.TempDir, "CLAUDE.md"))
			Expect(err).NotTo(HaveOccurred())
			return string(raw)
		}

		It("CS-LNCH-193: --network follows --init only when the network applies, with one banner line", func() {
			p := build("claude-sandbox")
			args := p.CreateArgs(proj)
			Expect(args[:6]).To(Equal([]string{"create", "-it", "--rm", "--init", "--network", "claude-sandbox"}))
			Expect(strings.Count(out.String(), "Network: claude-sandbox")).To(Equal(1))
			Expect(out.String()).To(ContainSubstring(launch.NetworkBanner + "\n"))
			Expect(args).NotTo(ContainElement("--dns"))

			out.Reset()
			off := build("")
			Expect(off.CreateArgs(proj)[:5]).To(Equal([]string{"create", "-it", "--rm", "--init", "-v"}))
			Expect(off.CreateArgs(proj)).NotTo(ContainElement("--network"))
			Expect(out.String()).NotTo(ContainSubstring("Network:"))
		})

		It("CS-LNCH-193: headless gets --network after --init too", func() {
			in.Headless = true
			args := build("claude-sandbox").CreateArgs(proj)
			Expect(args[:6]).To(Equal([]string{"create", "-i", "--rm", "--init", "--network", "claude-sandbox"}))
		})

		It("CS-LNCH-191: the context paragraph is appended only when the network applies", func() {
			touch(filepath.Join(home, ".claude", "CLAUDE.md"), "# host memory\n")
			offPlan := build("")
			Expect(shadow()).To(Equal("# host memory\n\n" + string(assets.ContainerContext)))
			offDigest := shadowDigest(offPlan)

			onPlan := build("claude-sandbox")
			Expect(shadow()).To(Equal("# host memory\n\n" + string(assets.ContainerContext) + "\n" + launch.NetworkContext))
			Expect(shadow()).To(HaveSuffix(launch.NetworkContext))
			Expect(shadowDigest(onPlan)).NotTo(Equal(offDigest))
			Expect(string(assets.ContainerContext)).NotTo(ContainSubstring("claude-sandbox network"))
		})

		It("CS-LNCH-189: the applied network is hashed as network=… plus the <network> input; off adds neither", func() {
			off := build("")
			for _, d := range off.ConfigInputs {
				Expect(d.Kind).NotTo(Equal(launch.KindNetwork))
			}
			on := build("claude-sandbox")
			Expect(on.ConfigHash).NotTo(Equal(off.ConfigHash))
			changes := launch.Drift(off.ConfigInputs, on.ConfigInputs)
			Expect(changes).To(ContainElement(launch.DriftChange{Path: "<network>", How: "added", Kind: launch.KindNetwork}))
			// An opted-in tree's report also shows the context paragraph's
			// shadow change: <network> is never the only line.
			Expect(changes).To(ContainElement(launch.DriftChange{Path: "CLAUDE.md", How: "changed", Kind: launch.KindShadow}))

			// Off hashes as a launch without the feature: two key-off builds agree.
			Expect(build("").ConfigHash).To(Equal(off.ConfigHash))
		})

		It("CS-LNCH-189: the hashed text has a network line only when the network applied", func() {
			var text string
			DeferCleanup(launch.CaptureFingerprintText(func(s string) { text = s }))
			build("")
			Expect(text).To(ContainSubstring("oomScoreAdj="), "the hook saw the hashed text")
			Expect(text).NotTo(ContainSubstring("network"), "key off: no network line, so the hash is a pre-feature launch's")
			build("claude-sandbox")
			Expect(text).To(ContainSubstring("\nnetwork=claude-sandbox\n"))
		})
	})
})

func shadowDigest(p *launch.Plan) string {
	for _, d := range p.ConfigInputs {
		if d.Path == "CLAUDE.md" && d.Kind == launch.KindShadow {
			return d.Digest
		}
	}
	return ""
}
