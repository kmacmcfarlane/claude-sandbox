package launch_test

// Spec: spec/launch.feature CS-LNCH-213..217 — the container's TZ: the host's
// zone by default, the timezone key and CLAUDE_SANDBOX_TIMEZONE, the env-file
// and image stand-downs, the label, and that it is never hashed.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// tzHost is a fake host for ResolveTimezone: an environment, a link table
// and /etc/timezone's contents, recording every Readlink.
type tzHost struct {
	env     map[string]string
	links   map[string]string
	etc     string
	hasEtc  bool
	readlnk []string
	etcRead int
}

func (h *tzHost) sources() launch.TimezoneSources {
	return launch.TimezoneSources{
		LookupEnv: func(k string) (string, bool) { v, ok := h.env[k]; return v, ok },
		Readlink: func(p string) (string, error) {
			h.readlnk = append(h.readlnk, p)
			if t, ok := h.links[p]; ok {
				return t, nil
			}
			return "", errors.New("invalid argument")
		},
		ReadEtcTimezone: func() ([]byte, error) {
			h.etcRead++
			if !h.hasEtc {
				return nil, os.ErrNotExist
			}
			return []byte(h.etc), nil
		},
	}
}

func hostTZ(h *tzHost) launch.TimezoneResult {
	return launch.ResolveTimezone(launch.TimezoneSetting{Mode: launch.TimezoneHost}, nil, false, h.sources())
}

var _ = Describe("host time zone (CS-LNCH-213..217)", func() {
	DescribeTable("CS-LNCH-214: the host's zone, in order",
		func(h *tzHost, want string, readlinks int) {
			r := hostTZ(h)
			Expect(r.Value).To(Equal(want))
			Expect(r.Label).To(Equal(want))
			Expect(r.Note).To(BeEmpty())
			Expect(h.readlnk).To(HaveLen(readlinks))
		},
		Entry("TZ name", &tzHost{env: map[string]string{"TZ": "Europe/Berlin"}}, "Europe/Berlin", 0),
		Entry(":name drops the colon", &tzHost{env: map[string]string{"TZ": ":America/Chicago"}}, "America/Chicago", 0),
		Entry("POSIX string verbatim", &tzHost{env: map[string]string{"TZ": "CST6CDT,M3.2.0,M11.1.0"}}, "CST6CDT,M3.2.0,M11.1.0", 0),
		Entry("angle-bracket POSIX string", &tzHost{env: map[string]string{"TZ": "<+0530>-5:30"}}, "<+0530>-5:30", 0),
		Entry("path TZ naming a zone file: its text, nothing read",
			&tzHost{env: map[string]string{"TZ": ":/usr/share/zoneinfo/Europe/Berlin"}}, "Europe/Berlin", 0),
		Entry("path TZ under posix/", &tzHost{env: map[string]string{"TZ": "/usr/share/zoneinfo/posix/Asia/Tokyo"}}, "Asia/Tokyo", 0),
		Entry("TZ=:/etc/localtime follows the link",
			&tzHost{env: map[string]string{"TZ": ":/etc/localtime"}, links: map[string]string{"/etc/localtime": "../usr/share/zoneinfo/America/Chicago"}},
			"America/Chicago", 1),
		Entry("TZ=/etc/localtime unresolvable goes on to /etc/timezone",
			&tzHost{env: map[string]string{"TZ": "/etc/localtime"}, etc: "Europe/Paris\n", hasEtc: true}, "Europe/Paris", 1),
		Entry("Fedora relative link", &tzHost{links: map[string]string{"/etc/localtime": "../usr/share/zoneinfo/America/Chicago"}}, "America/Chicago", 1),
		Entry("macOS", &tzHost{links: map[string]string{"/etc/localtime": "/var/db/timezone/zoneinfo/America/New_York"}}, "America/New_York", 1),
		Entry("NixOS, one hop", &tzHost{links: map[string]string{"/etc/localtime": "/etc/zoneinfo/Europe/Oslo"}}, "Europe/Oslo", 1),
		Entry("posix/ link stripped", &tzHost{links: map[string]string{"/etc/localtime": "/usr/share/zoneinfo/posix/UTC"}}, "UTC", 1),
		Entry("right/ link stripped", &tzHost{links: map[string]string{"/etc/localtime": "/usr/share/zoneinfo/right/Europe/Rome"}}, "Europe/Rome", 1),
		Entry("Ubuntu Core, two hops", &tzHost{links: map[string]string{
			"/etc/localtime":          "/etc/writable/localtime",
			"/etc/writable/localtime": "/usr/share/zoneinfo/Asia/Seoul",
		}}, "Asia/Seoul", 2),
		Entry("a relative intermediate hop", &tzHost{links: map[string]string{
			"/etc/localtime":    "tz/localtime",
			"/etc/tz/localtime": "/usr/share/zoneinfo/Africa/Lagos",
		}}, "Africa/Lagos", 2),
		Entry("a relative text naming zoneinfo only once joined",
			&tzHost{links: map[string]string{"/etc/localtime": "zoneinfo/Europe/Oslo"}}, "Europe/Oslo", 1),
		Entry("empty TZ counts as unset", &tzHost{env: map[string]string{"TZ": ""}, links: map[string]string{"/etc/localtime": "/usr/share/zoneinfo/Etc/UTC"}}, "Etc/UTC", 1),
		Entry("regular /etc/localtime: /etc/timezone's first line", &tzHost{etc: "  Europe/Lisbon  \nignored\n", hasEtc: true}, "Europe/Lisbon", 1),
	)

	DescribeTable("CS-LNCH-214, CS-LNCH-215: unknown gives no value, label unknown and one Note",
		func(h *tzHost, why string) {
			r := hostTZ(h)
			Expect(r.Value).To(BeEmpty())
			Expect(r.Label).To(Equal(launch.TimezoneLabelUnknown))
			Expect(r.Note).To(HavePrefix("Note: could not tell the host's time zone ("))
			Expect(r.Note).To(HaveSuffix("); the sandbox runs in UTC. Set timezone: <Area/City> in .claude-sandbox/config.yaml."))
			Expect(r.Note).To(ContainSubstring(why))
			Expect(r.Note).NotTo(ContainSubstring("\x1b"))
		},
		Entry("nothing at all", &tzHost{}, "no readable /etc/timezone"),
		Entry("a path TZ reaching no zoneinfo", &tzHost{env: map[string]string{"TZ": ":/opt/tz/foo"}}, "TZ=:/opt/tz/foo names no zone file"),
		Entry("a 5-hop chain", &tzHost{links: map[string]string{
			"/etc/localtime": "/a", "/a": "/b", "/b": "/c", "/c": "/d", "/d": "/usr/share/zoneinfo/UTC",
		}}, "no readable /etc/timezone"),
		Entry("a bad host TZ shape, named printably", &tzHost{env: map[string]string{"TZ": "Bad Zone\x1b"}}, "TZ=Bad Zone"),
		Entry("a .. component", &tzHost{env: map[string]string{"TZ": "Europe/../etc"}}, `".." component`),
		Entry("a leading -", &tzHost{env: map[string]string{"TZ": "-x"}}, `starts with "-"`),
		Entry("too long", &tzHost{env: map[string]string{"TZ": strings.Repeat("a", 129)}}, "longer than 128"),
		Entry("an empty /etc/timezone", &tzHost{etc: "\n", hasEtc: true}, "/etc/timezone is empty"),
	)

	It("CS-LNCH-214: the 5-hop chain stops after 4 Readlinks", func() {
		h := &tzHost{links: map[string]string{"/etc/localtime": "/a", "/a": "/b", "/b": "/c", "/c": "/d", "/d": "/usr/share/zoneinfo/UTC"}}
		hostTZ(h)
		Expect(h.readlnk).To(Equal([]string{"/etc/localtime", "/a", "/b", "/c"}))
	})

	choice := func(envVal, key string) (launch.TimezoneSetting, error) {
		cfg := &cascade.Config{}
		if key != "" {
			cfg.Timezone = &key
		}
		return launch.TimezoneChoice(func(k string) string {
			if k == launch.TimezoneEnv {
				return envVal
			}
			return ""
		}, cfg, "/p/.claude-sandbox/config.yaml")
	}
	const keyFile = "/p/.claude-sandbox/config.yaml"

	DescribeTable("CS-LNCH-215, CS-LNCH-216: TimezoneChoice",
		func(envVal, key string, mode launch.TimezoneMode, value, source string) {
			got, err := choice(envVal, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Mode).To(Equal(mode))
			Expect(got.Value).To(Equal(value))
			Expect(got.Source).To(Equal(source))
		},
		Entry("default host", "", "", launch.TimezoneHost, "", ""),
		Entry("explicit host key", "", "host", launch.TimezoneHost, "", keyFile),
		Entry("key value", "", "Europe/Berlin", launch.TimezoneExplicit, "Europe/Berlin", keyFile),
		Entry("env beats key", "UTC", "Europe/Berlin", launch.TimezoneExplicit, "UTC", launch.TimezoneEnv),
		Entry("env off beats key", "0", "Europe/Berlin", launch.TimezoneOff, "", launch.TimezoneEnv),
		Entry("env host beats key", "HOST", "Europe/Berlin", launch.TimezoneHost, "", launch.TimezoneEnv),
		Entry("key none", "", "none", launch.TimezoneOff, "", keyFile),
		Entry("key OFF", "", "OFF", launch.TimezoneOff, "", keyFile),
		Entry("key false", "", "false", launch.TimezoneOff, "", keyFile),
		Entry("key No", "", "No", launch.TimezoneOff, "", keyFile),
		Entry("env none", "none", "", launch.TimezoneOff, "", launch.TimezoneEnv),
		Entry("POSIX string", "", "CST6CDT", launch.TimezoneExplicit, "CST6CDT", keyFile),
		Entry(":name drops the colon", "", ":Asia/Tokyo", launch.TimezoneExplicit, "Asia/Tokyo", keyFile),
		Entry("blank key is the default", "", "  ", launch.TimezoneHost, "", ""),
	)

	DescribeTable("CS-LNCH-215: a bad or path value is an error naming its source",
		func(envVal, key, want string) {
			_, err := choice(envVal, key)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(want))
		},
		Entry("key path", "", "/usr/share/zoneinfo/UTC", `timezone: "/usr/share/zoneinfo/UTC" in /p/.claude-sandbox/config.yaml: a path`),
		Entry("key :path", "", ":/etc/localtime", `in /p/.claude-sandbox/config.yaml: a path`),
		Entry("env path", "/etc/localtime", "", `CLAUDE_SANDBOX_TIMEZONE="/etc/localtime": a path`),
		Entry("env bad char", "Europe/Ber lin", "", `CLAUDE_SANDBOX_TIMEZONE="Europe/Ber lin": holds a character`),
		Entry("key ..", "", "Europe/../x", `".." component`),
		Entry("key leading -", "", "-oops", `starts with "-"`),
	)

	Describe("CS-LNCH-216: precedence and the Notes", func() {
		explicit := launch.TimezoneSetting{Mode: launch.TimezoneExplicit, Value: "UTC", Source: "/p/config.yaml"}
		host := launch.TimezoneSetting{Mode: launch.TimezoneHost}
		off := launch.TimezoneSetting{Mode: launch.TimezoneOff, Source: launch.TimezoneEnv}
		tzFile := []cascade.EnvFile{envFile("/p/.claude-sandbox/env", "TZ=Asia/Tokyo\n")}
		bareFile := []cascade.EnvFile{envFile("/p/.claude-sandbox/env", "TZ\n")}
		chicago := func() *tzHost {
			return &tzHost{links: map[string]string{"/etc/localtime": "/usr/share/zoneinfo/America/Chicago"}}
		}

		It("CS-LNCH-216: an env file assigning TZ wins over an explicit value, with the Note", func() {
			r := launch.ResolveTimezone(explicit, tzFile, true, chicago().sources())
			Expect(r).To(Equal(launch.TimezoneResult{Label: launch.TimezoneLabelEnvFile,
				Note: "Note: timezone: UTC (/p/config.yaml) is overridden by TZ in /p/.claude-sandbox/env; the sandbox uses that."}))
		})
		It("CS-LNCH-216: an env file under the host default is silent and reads nothing", func() {
			h := chicago()
			r := launch.ResolveTimezone(host, tzFile, false, h.sources())
			Expect(r).To(Equal(launch.TimezoneResult{Label: launch.TimezoneLabelEnvFile}))
			Expect(h.readlnk).To(BeEmpty())
		})
		It("CS-LNCH-216: a bare env-file TZ counts only when the launcher has TZ", func() {
			h := chicago()
			h.env = map[string]string{"TZ": "Europe/Berlin"}
			Expect(launch.ResolveTimezone(host, bareFile, false, h.sources()).Label).To(Equal(launch.TimezoneLabelEnvFile))
			h = chicago()
			r := launch.ResolveTimezone(host, bareFile, false, h.sources())
			Expect(r.Value).To(Equal("America/Chicago"))
		})
		It("CS-LNCH-216: an explicit value beats the image's TZ, with the Note", func() {
			r := launch.ResolveTimezone(explicit, nil, true, chicago().sources())
			Expect(r).To(Equal(launch.TimezoneResult{Value: "UTC", Label: "UTC",
				Note: "Note: timezone: UTC (/p/config.yaml) overrides TZ set by the image."}))
		})
		It("CS-LNCH-216: an explicit value with no image TZ is silent", func() {
			Expect(launch.ResolveTimezone(explicit, nil, false, chicago().sources())).To(Equal(launch.TimezoneResult{Value: "UTC", Label: "UTC"}))
		})
		It("CS-LNCH-216: the host default under an image TZ is silent, label image, nothing read", func() {
			h := chicago()
			Expect(launch.ResolveTimezone(host, nil, true, h.sources())).To(Equal(launch.TimezoneResult{Label: launch.TimezoneLabelImage}))
			Expect(h.readlnk).To(BeEmpty())
			Expect(h.etcRead).To(Equal(0))
		})
		It("CS-LNCH-216: off passes nothing, label none, silent under an image TZ", func() {
			h := chicago()
			Expect(launch.ResolveTimezone(off, nil, true, h.sources())).To(Equal(launch.TimezoneResult{Label: launch.TimezoneLabelNone}))
			Expect(launch.ResolveTimezone(off, nil, false, h.sources())).To(Equal(launch.TimezoneResult{Label: launch.TimezoneLabelNone}))
			Expect(h.readlnk).To(BeEmpty())
		})
		It("CS-LNCH-216: the host default resolves the host's zone", func() {
			Expect(launch.ResolveTimezone(host, nil, false, chicago().sources())).To(Equal(launch.TimezoneResult{Value: "America/Chicago", Label: "America/Chicago"}))
		})
	})

	Describe("Build renders only (CS-LNCH-213, CS-LNCH-217)", func() {
		var (
			proj, shadow string
			in           launch.Inputs
		)
		BeforeEach(func() {
			base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			home := filepath.Join(base, "home")
			proj, shadow = filepath.Join(base, "proj"), filepath.Join(base, "shadow")
			mkdir(home)
			mkdir(proj)
			mkdir(shadow)
			// The launcher's own TZ is set: Build must never forward it.
			env := map[string]string{"TZ": "Europe/Berlin"}
			in = launch.Inputs{
				ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
				Getenv:    func(k string) string { return env[k] },
				LookupEnv: lookupIn(env),
				ImageName: "claude-sandbox-df-proj-abc123", ImageID: "sha256:aaaa",
				Cfg: &cascade.Config{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
			}
		})
		build := func(i launch.Inputs) *launch.Plan {
			dir, err := os.MkdirTemp(shadow, "run")
			Expect(err).NotTo(HaveOccurred())
			i.TempDir = dir
			p, err := launch.Build(i)
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		tzEnv := func(p *launch.Plan) []string {
			var out []string
			for _, e := range p.EnvFlags {
				if e == "TZ" || strings.HasPrefix(e, "TZ=") {
					out = append(out, e)
				}
			}
			return out
		}
		tzLabel := func(p *launch.Plan) []string {
			var out []string
			for _, l := range p.Labels {
				if v, ok := strings.CutPrefix(l, launch.LabelTimezone+"="); ok {
					out = append(out, v)
				}
			}
			return out
		}

		DescribeTable("CS-LNCH-213: interactive, headless and ralph plans carry -e TZ=<value> and the label",
			func(mod func(*launch.Inputs)) {
				i := in
				i.Timezone, i.TimezoneLabel = "America/Chicago", "America/Chicago"
				mod(&i)
				p := build(i)
				Expect(tzEnv(p)).To(Equal([]string{"TZ=America/Chicago"}))
				Expect(tzLabel(p)).To(Equal([]string{"America/Chicago"}))
				Expect(p.CreateArgs(proj)).To(ContainElement("TZ=America/Chicago"))
				for _, v := range p.Volumes {
					Expect(v).NotTo(ContainSubstring("/etc/localtime"))
					Expect(v).NotTo(ContainSubstring("/etc/timezone"))
				}
			},
			Entry("interactive", func(*launch.Inputs) {}),
			Entry("headless", func(i *launch.Inputs) { i.Headless = true }),
			Entry("ralph", func(i *launch.Inputs) { i.RalphMode = true }),
		)

		It("CS-LNCH-217: no value passes no -e, never the launcher's own TZ, and the label defaults to none", func() {
			p := build(in)
			Expect(tzEnv(p)).To(BeEmpty())
			Expect(tzLabel(p)).To(Equal([]string{launch.TimezoneLabelNone}))
			i := in
			i.TimezoneLabel = launch.TimezoneLabelEnvFile
			Expect(tzLabel(build(i))).To(Equal([]string{launch.TimezoneLabelEnvFile}))
		})

		It("CS-LNCH-217: the fingerprint is equal with and without a zone, across two zones and a key", func() {
			none := build(in)
			a := in
			a.Timezone, a.TimezoneLabel = "America/Chicago", "America/Chicago"
			b := in
			b.Timezone, b.TimezoneLabel = "Europe/Berlin", "Europe/Berlin"
			Expect(build(a).ConfigHash).To(Equal(none.ConfigHash))
			Expect(build(b).ConfigHash).To(Equal(none.ConfigHash))
			key := "Asia/Tokyo"
			c := in
			c.Cfg = &cascade.Config{Timezone: &key}
			Expect(build(c).ConfigHash).To(Equal(none.ConfigHash))
		})
	})
})
