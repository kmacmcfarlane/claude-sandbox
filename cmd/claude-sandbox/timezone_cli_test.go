package main

// Spec: spec/launch.feature CS-LNCH-213..217 — the host's time zone end to end
// through MainWithEnv. newCLIFixture pins CLAUDE_SANDBOX_TIMEZONE=none; these
// tests delete or replace the pin.

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// tzEnvFlags returns the TZ entries of the reserving create's -e flags.
func tzEnvFlags(f *cliFixture) []string {
	var out []string
	for _, v := range createFlag(f, "-e") {
		if v == "TZ" || strings.HasPrefix(v, "TZ=") {
			out = append(out, v)
		}
	}
	return out
}

// tzHostLink gives the fixture a host whose /etc/localtime names zone, and
// counts every Readlink.
func tzHostLink(f *cliFixture, zone string) *int {
	n := 0
	f.env.Readlink = func(p string) (string, error) {
		n++
		if p == launch.HostLocaltime {
			return "../usr/share/zoneinfo/" + zone, nil
		}
		return "", os.ErrInvalid
	}
	return &n
}

const imageEnvPat = "{{range .Config.Env}}"

var _ = Describe("the host's time zone (CS-LNCH-213..217)", func() {
	var f *cliFixture
	BeforeEach(func() {
		f = newCLIFixture()
		delete(f.envmap, launch.TimezoneEnv) // the default: host
	})

	It("CS-LNCH-213, CS-LNCH-214: a launch passes the host's zone and labels it", func() {
		tzHostLink(f, "America/Chicago")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(Equal([]string{"TZ=America/Chicago"}))
		Expect(labelsOf(f.launched().Args)[launch.LabelTimezone]).To(Equal("America/Chicago"))
		for _, v := range createFlag(f, "-v") {
			Expect(v).NotTo(ContainSubstring("/etc/localtime"))
			Expect(v).NotTo(ContainSubstring("/etc/timezone"))
		}
	})

	It("CS-LNCH-213: headless and ralph get it too", func() {
		tzHostLink(f, "Europe/Berlin")
		Expect(f.run("headless", "--", "-p", "hi")).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(Equal([]string{"TZ=Europe/Berlin"}))
		Expect(f.out.String()).To(BeEmpty())

		g := newCLIFixture()
		delete(g.envmap, launch.TimezoneEnv)
		tzHostLink(g, "Europe/Berlin")
		Expect(g.run("--ralph")).To(Equal(0), g.errw.String())
		Expect(tzEnvFlags(g)).To(Equal([]string{"TZ=Europe/Berlin"}))
	})

	It("CS-LNCH-214: the launcher's TZ comes first, and a path TZ naming a zone file reads nothing", func() {
		n := tzHostLink(f, "America/Chicago")
		f.envmap["TZ"] = ":/usr/share/zoneinfo/Asia/Tokyo"
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(Equal([]string{"TZ=Asia/Tokyo"}))
		Expect(*n).To(Equal(0))
	})

	It("CS-LNCH-214, CS-LNCH-217: an unknown zone prints one Note, passes nothing and labels unknown", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(BeEmpty())
		Expect(labelsOf(f.launched().Args)[launch.LabelTimezone]).To(Equal(launch.TimezoneLabelUnknown))
		all := f.out.String() + f.errw.String()
		Expect(strings.Count(all, "Note: could not tell the host's time zone (")).To(Equal(1))
		Expect(all).To(ContainSubstring("); the sandbox runs in UTC. Set timezone: <Area/City> in .claude-sandbox/config.yaml.\n"))
	})

	It("CS-LNCH-214: /etc/timezone is read when the link names no zone", func() {
		etc := filepath.Join(f.tmp, "etc-timezone")
		writeFile(etc, "Pacific/Auckland\n")
		f.env.EtcTimezonePath = etc
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(Equal([]string{"TZ=Pacific/Auckland"}))
	})

	It("CS-LNCH-216: the key sets it, CLAUDE_SANDBOX_TIMEZONE beats the key, and none passes nothing", func() {
		tzHostLink(f, "America/Chicago")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "timezone: UTC\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(Equal([]string{"TZ=UTC"}))

		g := newCLIFixture()
		g.envmap[launch.TimezoneEnv] = "Europe/Rome"
		writeFile(filepath.Join(g.proj, ".claude-sandbox", "config.yaml"), "timezone: UTC\n")
		Expect(g.run()).To(Equal(0), g.errw.String())
		Expect(tzEnvFlags(g)).To(Equal([]string{"TZ=Europe/Rome"}))

		h := newCLIFixture() // the pin: none
		Expect(h.run()).To(Equal(0), h.errw.String())
		Expect(tzEnvFlags(h)).To(BeEmpty())
		Expect(labelsOf(h.launched().Args)[launch.LabelTimezone]).To(Equal(launch.TimezoneLabelNone))
	})

	It("CS-LNCH-215: a bad key exits 2 naming its file, before any image work", func() {
		cfgFile := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(cfgFile, "timezone: /usr/share/zoneinfo/UTC\n")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring(`timezone: "/usr/share/zoneinfo/UTC" in ` + cfgFile + ": a path"))
		Expect(createdAny(f)).To(BeFalse())
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker build"))
			Expect(l).NotTo(HavePrefix("docker image inspect"))
		}
	})

	It("CS-LNCH-215: a bad CLAUDE_SANDBOX_TIMEZONE exits 2 naming the variable", func() {
		f.envmap[launch.TimezoneEnv] = "Bad Zone"
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring(`CLAUDE_SANDBOX_TIMEZONE="Bad Zone"`))
		Expect(createdAny(f)).To(BeFalse())
	})

	It("CS-LNCH-216: an env file defining TZ wins silently for the host default, label env-file", func() {
		n := tzHostLink(f, "America/Chicago")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "TZ=Asia/Kolkata\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(BeEmpty())
		Expect(labelsOf(f.launched().Args)[launch.LabelTimezone]).To(Equal(launch.TimezoneLabelEnvFile))
		Expect(f.out.String() + f.errw.String()).NotTo(ContainSubstring("Note: timezone"))
		Expect(*n).To(Equal(0))
	})

	It("CS-LNCH-216: an explicit value an env file outranks prints the Note once", func() {
		envFile := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(envFile, "TZ=Asia/Kolkata\n")
		f.envmap[launch.TimezoneEnv] = "UTC"
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(BeEmpty())
		Expect(strings.Count(f.out.String()+f.errw.String(),
			"Note: timezone: UTC (CLAUDE_SANDBOX_TIMEZONE) is overridden by TZ in "+envFile+"; the sandbox uses that.\n")).To(Equal(1))
	})

	Describe("CS-LNCH-216: the run image's ENV TZ, from the one run-image inspect", func() {
		imageWithTZ := func(g *cliFixture) {
			g.fake.On(imageEnvPat, "sha256:feed\x1fPATH=/usr/bin\nTZ=Europe/Oslo\n\n", nil)
		}
		inspects := func(g *cliFixture) (identity, plain int) {
			for _, l := range g.fake.CommandLines() {
				switch {
				case strings.HasPrefix(l, "docker image inspect -f {{.Id}}\x1f"):
					identity++
				case strings.HasPrefix(l, "docker image inspect -f {{.Id}} ") && strings.HasSuffix(l, ":run"):
					plain++
				}
			}
			return
		}

		It("CS-LNCH-216: the host default stands down silently, label image, nothing read", func() {
			n := tzHostLink(f, "America/Chicago")
			imageWithTZ(f)
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(tzEnvFlags(f)).To(BeEmpty())
			l := labelsOf(f.launched().Args)
			Expect(l[launch.LabelTimezone]).To(Equal(launch.TimezoneLabelImage))
			Expect(*n).To(Equal(0))
			identity, plain := inspects(f)
			Expect(identity).To(Equal(1))
			Expect(plain).To(Equal(0), "the launch's ImageID call became ImageIdentity, not a second inspect")
		})

		It("CS-LNCH-216: an explicit value beats it, with the Note", func() {
			imageWithTZ(f)
			cfgFile := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
			writeFile(cfgFile, "timezone: UTC\n")
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(tzEnvFlags(f)).To(Equal([]string{"TZ=UTC"}))
			Expect(strings.Count(f.out.String()+f.errw.String(),
				"Note: timezone: UTC ("+cfgFile+") overrides TZ set by the image.\n")).To(Equal(1))
		})

		It("CS-LNCH-216: none under an image TZ is silent", func() {
			imageWithTZ(f)
			f.envmap[launch.TimezoneEnv] = "off"
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(tzEnvFlags(f)).To(BeEmpty())
			Expect(f.out.String() + f.errw.String()).NotTo(ContainSubstring("Note: timezone"))
		})
	})

	It("CS-LNCH-217: a launch's hash equals the key-off one; an attach and a join resolve nothing and add no TZ", func() {
		tzHostLink(f, "America/Chicago")
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		withTZ := labelsOf(f.launched().Args)

		f.envmap[launch.TimezoneEnv] = "none"
		f.fake.Calls = nil
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		Expect(tzEnvFlags(f)).To(BeEmpty())
		Expect(labelsOf(f.launched().Args)["claude-sandbox.confighash"]).To(Equal(withTZ["claude-sandbox.confighash"]))

		for _, mode := range []string{"--attach=otter", "--join=otter"} {
			h := f
			h.fake = &execx.Fake{}
			h.env.Runner = h.fake
			h.lock.fake = h.fake
			h.out.Reset()
			h.errw.Reset()
			delete(h.envmap, launch.TimezoneEnv)
			n := tzHostLink(h, "Europe/Berlin")
			h.fake.On("docker ps", psRowFull("cs-otter", "Up 1 hour", h.proj, "otter", "",
				withTZ["claude-sandbox.confighash"], withTZ["claude-sandbox.inputs"])+"\n", nil)
			h.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(h.run(mode)).To(Equal(0), "%s: %s", mode, h.errw.String())
			Expect(h.errw.String()).NotTo(ContainSubstring("different configuration"), mode)
			Expect(*n).To(Equal(0), "%s: the drift check's would-be build reads no host zone", mode)
			Expect(h.sessionLine()).NotTo(ContainSubstring("TZ"), mode)
			Expect(h.out.String()+h.errw.String()).NotTo(ContainSubstring("time zone"), mode)
		}
	})
})
