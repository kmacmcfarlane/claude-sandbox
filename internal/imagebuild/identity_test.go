package imagebuild_test

// Spec: CS-LNCH-216 — the run image's Config.Env TZ is read in the run-image
// inspect that already gives the image ID (ImageIdentity), no extra call.

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

var _ = Describe("ImageIdentity (CS-LNCH-216)", func() {
	const id = "sha256:0123abcd"

	// One fake image answering both templates as docker would render them.
	fakeImage := func(env ...string) *execx.Fake {
		f := &execx.Fake{}
		f.OnFunc("docker image inspect", func(c execx.Cmd) (string, error) {
			tmpl := c.Args[len(c.Args)-2]
			if tmpl == "{{.Id}}" {
				return id + "\n", nil
			}
			var b strings.Builder
			b.WriteString(id + "\x1f")
			for _, e := range env {
				b.WriteString(e + "\n")
			}
			b.WriteString("\n")
			return b.String(), nil
		})
		return f
	}

	It("CS-LNCH-216: returns the same id as ImageID, in one inspect", func() {
		f := fakeImage("PATH=/usr/bin", "TZ=Europe/Berlin")
		got, tz := imagebuild.ImageIdentity(f, "img:run")
		Expect(got).To(Equal(imagebuild.ImageID(f, "img:run")))
		Expect(got).To(Equal(id))
		Expect(tz).To(BeTrue())
		n := 0
		for _, l := range f.CommandLines() {
			if strings.Contains(l, "{{range .Config.Env}}") {
				n++
			}
		}
		Expect(n).To(Equal(1))
	})

	It("CS-LNCH-216: no TZ line (a TZX= or a value naming TZ does not count)", func() {
		f := fakeImage("PATH=/usr/bin", "TZX=1", "NOTE=TZ=x")
		got, tz := imagebuild.ImageIdentity(f, "img:run")
		Expect(got).To(Equal(id))
		Expect(tz).To(BeFalse())
	})

	It("CS-LNCH-216: output without the separator is all id, with no TZ", func() {
		f := &execx.Fake{}
		f.On("docker image inspect", "  "+id+"\n", nil)
		got, tz := imagebuild.ImageIdentity(f, "img:run")
		Expect(got).To(Equal(id))
		Expect(tz).To(BeFalse())
	})

	It("CS-LNCH-216: an inspect error gives no id and no TZ", func() {
		f := &execx.Fake{}
		f.On("docker image inspect", "TZ=x", execx.Fail(1))
		got, tz := imagebuild.ImageIdentity(f, "img:run")
		Expect(got).To(BeEmpty())
		Expect(tz).To(BeFalse())
	})
})
