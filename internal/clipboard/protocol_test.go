package clipboard_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
)

var _ = Describe("the bridge protocol (CS-LNCH-198)", func() {
	It("CS-LNCH-198: parses request lines, osc= paths included, and refuses malformed ones", func() {
		r, err := clipboard.ParseRequest("CSCB1 clipboard 12 osc=ancestor:1:2")
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(clipboard.Request{Op: "clipboard", Len: 12, OSC: "ancestor:1:2"}))
		Expect(clipboard.FormatRequest(r)).To(Equal("CSCB1 clipboard 12 osc=ancestor:1:2\n"))
		r, err = clipboard.ParseRequest("CSCB1 ping 0")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Op).To(Equal("ping"))
		for _, bad := range []string{
			"", "CSCB1", "CSCB2 clipboard 1", "CSCB1 paste 1", "CSCB1 clipboard -1",
			"CSCB1 clipboard 01", "CSCB1 clipboard x", "CSCB1 ping 3", "CSCB1 clear-primary 1",
			"CSCB1 clipboard 1 osc=elsewhere", "CSCB1 clipboard 1 osc=ancestor:5:1",
			"CSCB1 clipboard 1 osc=ancestor:1:3", "CSCB1 clipboard 1 osc=tty extra",
		} {
			_, err := clipboard.ParseRequest(bad)
			Expect(err).To(HaveOccurred(), bad)
		}
	})

	It("CS-LNCH-198: payloads: 1 MiB, UTF-8, no C0 but tab/LF/CR, no DEL, no C1", func() {
		reason := func(s string) string {
			if err := clipboard.ValidatePayload([]byte(s)); err != nil {
				return err.Error()
			}
			return ""
		}
		Expect(reason("\x1b[201~rm -rf ~\n")).To(Equal("control-char"))
		Expect(reason("\xc2\x9b201~")).To(Equal("control-char"))
		Expect(reason("\x7f")).To(Equal("control-char"))
		Expect(reason("\x00")).To(Equal("control-char"))
		Expect(reason("\x1b")).To(Equal("control-char"))
		Expect(reason("a\tb\r\nc")).To(BeEmpty())
		Expect(reason("日本語 ✓")).To(BeEmpty())
		Expect(reason("\xff")).To(Equal("utf8"))
		Expect(reason(strings.Repeat("a", clipboard.MaxCopyBytes))).To(BeEmpty())
		Expect(reason(strings.Repeat("a", clipboard.MaxCopyBytes+1))).To(Equal("too-large"))
	})
})

// FuzzValidatePayload (CS-LNCH-198): an accepted payload is valid UTF-8 and
// holds no rejected code point, so no ESC, C1 or DEL ever passes.
func FuzzValidatePayload(f *testing.F) {
	for _, s := range []string{"\x1b[201~rm -rf ~\n", "\xc2\x9b201~", "\x7f", "\x00", "\x1b", "a\tb\r\nc", "日本語 ✓", "\xff"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if clipboard.ValidatePayload(b) != nil {
			return
		}
		if !utf8.Valid(b) {
			t.Fatalf("accepted invalid UTF-8 %q", b)
		}
		for _, r := range string(b) {
			if r == 0x1b || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
				t.Fatalf("accepted %U in %q", r, b)
			}
		}
	})
}
