package launch_test

// Spec: spec/launch.feature CS-LNCH-206 — the endOnHangup key, its env
// override and the claude-sandbox.onhangup label.

import (
	"bytes"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("endOnHangup (CS-LNCH-206)", func() {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == launch.EndOnHangupEnv {
				return v
			}
			return ""
		}
	}
	on, off := true, false

	It("CS-LNCH-206: resolves env > key > on", func() {
		Expect(launch.EndOnHangupWanted(env(""), nil)).To(BeTrue())
		Expect(launch.EndOnHangupWanted(env(""), &cascade.Config{})).To(BeTrue())
		Expect(launch.EndOnHangupWanted(env(""), &cascade.Config{EndOnHangup: &off})).To(BeFalse())
		Expect(launch.EndOnHangupWanted(env("0"), &cascade.Config{EndOnHangup: &on})).To(BeFalse())
		Expect(launch.EndOnHangupWanted(env("no"), nil)).To(BeFalse())
		Expect(launch.EndOnHangupWanted(env("yes"), &cascade.Config{EndOnHangup: &off})).To(BeTrue())
		Expect(launch.EndOnHangupWanted(env("maybe"), &cascade.Config{EndOnHangup: &off})).To(BeFalse())
		for v, want := range map[string][2]bool{"1": {true, true}, "TRUE": {true, true}, "false": {false, true}, "": {false, false}, "x": {false, false}} {
			gotOn, gotSet := launch.EndOnHangupEnvSet(env(v))
			Expect([2]bool{gotOn, gotSet}).To(Equal(want), v)
		}
	})

	Describe("the label", func() {
		var in launch.Inputs
		BeforeEach(func() {
			base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			home, proj := filepath.Join(base, "home"), filepath.Join(base, "proj")
			mkdir(home)
			mkdir(proj)
			in = launch.Inputs{
				ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
				Getenv:    func(string) string { return "" },
				TempDir:   filepath.Join(base, "shadow"),
				ImageName: "claude-sandbox-proj",
				Out:       &bytes.Buffer{}, Err: &bytes.Buffer{},
			}
			mkdir(in.TempDir)
		})

		label := func(p *launch.Plan) string {
			for _, l := range p.Labels {
				if v, ok := cutPrefix(l, launch.LabelOnHangup+"="); ok {
					return v
				}
			}
			return "<none>"
		}

		It("CS-LNCH-206: end when wanted; keep when not, and always for --detach, ralph and headless; never hashed", func() {
			in.EndOnHangup = true
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(label(p)).To(Equal("end"))
			Expect(p.OnHangup).To(Equal(launch.OnHangupEnd))
			hash := p.ConfigHash

			for name, mut := range map[string]func(*launch.Inputs){
				"off":      func(i *launch.Inputs) { i.EndOnHangup = false },
				"detach":   func(i *launch.Inputs) { i.Detached = true },
				"ralph":    func(i *launch.Inputs) { i.RalphMode = true },
				"headless": func(i *launch.Inputs) { i.Headless = true },
			} {
				x := in
				mut(&x)
				Expect(launch.OnHangupLabelValue(x)).To(Equal(launch.OnHangupKeep), name)
			}
			x := in
			x.EndOnHangup = false
			x.TempDir = filepath.Join(filepath.Dir(in.TempDir), "shadow2")
			mkdir(x.TempDir)
			p2, err := launch.Build(x)
			Expect(err).NotTo(HaveOccurred())
			Expect(label(p2)).To(Equal("keep"))
			Expect(p2.ConfigHash).To(Equal(hash))
		})
	})
})

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}
