package registry_test

// Spec: spec/tmux.feature CS-TMUX-034 and spec/sessions.feature
// CS-SESS-066/067 — the shared hardened reader of Claude Code's peer
// registry. Each caller's policy on what it reports (the save hook: any bad
// record is a miss; the resume guard: retry ErrPartial, fail closed) is
// covered in internal/tmuxpane/save_test.go and
// internal/resumeguard/resumeguard_test.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

const convID = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"

var _ = Describe("registry", func() {
	var dir string

	BeforeEach(func() {
		dir = filepath.Join(GinkgoT().TempDir(), "sessions")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	})

	write := func(name, body string) {
		Expect(os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)).To(Succeed())
	}

	// byName indexes ReadAll's results by file name.
	byName := func() map[string]registry.Result {
		res, err := registry.ReadAll(dir)
		Expect(err).NotTo(HaveOccurred())
		out := map[string]registry.Result{}
		for _, r := range res {
			out[r.Entry.Name] = r
		}
		return out
	}

	It("CS-SESS-066: a valid record gives every field the readers use; other names are not entries", func() {
		write("7.json", fmt.Sprintf(`{"pid":7,"sessionId":%q,"cwd":"/p","startedAt":12,"procStart":"34",`+
			`"pidDomain":"linux::pid:[1]","name":"n","nameSource":"user","secret":"x"}`, convID))
		write("notes.txt", "x")
		write("x7.json", "{}")
		res := byName()
		Expect(res).To(HaveLen(1))
		Expect(res["7.json"].Err).NotTo(HaveOccurred())
		Expect(res["7.json"].Entry).To(Equal(registry.Entry{Name: "7.json", PID: 7}))
		Expect(res["7.json"].Record).To(Equal(registry.Record{PID: 7, SessionID: convID, Cwd: "/p", StartedAt: 12,
			ProcStart: "34", PIDDomain: "linux::pid:[1]", Name: "n", NameSource: "user"}))
	})

	It("CS-SESS-067: a bad record wraps ErrMalformed, and ErrPartial only when a partial write could explain it", func() {
		write("1.json", "{")
		write("2.json", fmt.Sprintf(`{"pid":2,"sessionId":%q,"pad":%q}`, convID, strings.Repeat("x", registry.MaxRecordSize)))
		write("3.json", fmt.Sprintf(`{"pid":9,"sessionId":%q}`, convID))
		write("4.json", `{"pid":4,"sessionId":"my-name"}`)
		write("99.json", fmt.Sprintf(`{"pid":5,"sessionId":%q}`, convID))
		Expect(os.Symlink("99.json", filepath.Join(dir, "5.json"))).To(Succeed())
		Expect(syscall.Mkfifo(filepath.Join(dir, "6.json"), 0o600)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(dir, "8.json"), 0o700)).To(Succeed())

		done := make(chan map[string]registry.Result)
		go func() { defer GinkgoRecover(); done <- byName() }()
		var res map[string]registry.Result
		Eventually(done, 2*time.Second).Should(Receive(&res), "a FIFO never blocks")

		partial := map[string]bool{"1.json": true, "2.json": true}
		for _, n := range []string{"1.json", "2.json", "3.json", "4.json", "5.json", "6.json", "8.json", "99.json"} {
			err := res[n].Err
			Expect(errors.Is(err, registry.ErrMalformed)).To(BeTrue(), n)
			Expect(errors.Is(err, registry.ErrPartial)).To(Equal(partial[n]), n)
			Expect(err.Error()).To(ContainSubstring(n))
		}
	})

	It("CS-SESS-066: a record that vanished is os.ErrNotExist, not malformed", func() {
		d, err := registry.OpenDir(dir)
		Expect(err).NotTo(HaveOccurred())
		defer d.Close()
		_, err = d.Read(registry.Entry{Name: "7.json", PID: 7})
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
		Expect(errors.Is(err, registry.ErrMalformed)).To(BeFalse())
	})

	It("CS-SESS-066: a missing directory is os.ErrNotExist; a symlink or a file is an error of its own", func() {
		_, err := registry.ReadAll(filepath.Join(dir, "absent"))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())

		link := dir + "-link"
		Expect(os.Symlink(dir, link)).To(Succeed())
		_, err = registry.ReadAll(link)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, os.ErrNotExist)).To(BeFalse())

		file := filepath.Join(dir, "file")
		write("file", "")
		_, err = registry.ReadAll(file)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, os.ErrNotExist)).To(BeFalse())
	})

	It("CS-SESS-067: more than MaxEntries entries of any kind is a directory error", func() {
		for i := 0; i < registry.MaxEntries; i++ {
			write(fmt.Sprintf("x%d", i), "")
		}
		_, err := registry.ReadAll(dir)
		Expect(err).NotTo(HaveOccurred(), "exactly MaxEntries is read")
		write("x-one-more", "")
		_, err = registry.ReadAll(dir)
		Expect(err).To(MatchError(ContainSubstring("more than 10000 entries")))
	})

	It("CS-TMUX-034: names: control characters stripped, over 200 characters or a leading '-' absent; name sources from the 2.1.284 set", func() {
		Expect(registry.CleanName("  fix\x07 the\nbug ")).To(Equal("fix the bug"))
		Expect(registry.CleanName("--dangerous")).To(BeEmpty())
		// Format (Cf: bidi overrides, zero-width), private-use (Co) and
		// surrogate (Cs, as invalid UTF-8 decodes to U+FFFD, kept) characters.
		Expect(registry.CleanName("safe\u202egnp.exe\u200b\ue000")).To(Equal("safegnp.exe"))
		Expect(registry.CleanName("\u2066-x\u2069")).To(BeEmpty(), "a hidden character cannot hide a leading '-'")
		Expect(registry.CleanName(strings.Repeat("é", 201))).To(BeEmpty())
		Expect(registry.CleanName(strings.Repeat("é", 200))).To(HaveLen(400))
		for _, s := range []string{"user", "peer", "derived", "collision", "auto", "hook"} {
			Expect(registry.NameSources[s]).To(BeTrue(), s)
		}
		Expect(registry.NameSources).To(HaveLen(6))
	})

	It("CS-TMUX-034: a record's name is cleaned and its source kept only for a kept name from the set", func() {
		rec := func(name, source string) registry.Record {
			b, err := json.Marshal(map[string]any{"pid": 7, "sessionId": convID, "name": name, "nameSource": source})
			Expect(err).NotTo(HaveOccurred())
			write("7.json", string(b))
			r := byName()["7.json"]
			Expect(r.Err).NotTo(HaveOccurred())
			return r.Record
		}
		Expect(rec("a\u001b[31mb\tc", "user")).To(And(
			HaveField("Name", "a [31mb c"), HaveField("NameSource", "user")))
		Expect(rec("n", "made-up").NameSource).To(BeEmpty())
		Expect(rec("", "user").NameSource).To(BeEmpty(), "no name, no name source")
		Expect(rec("-x", "user")).To(And(HaveField("Name", ""), HaveField("NameSource", "")))
	})

	It("CS-SESS-066: IsUUID accepts only a canonical conversation id", func() {
		Expect(registry.IsUUID(convID)).To(BeTrue())
		Expect(registry.IsUUID(strings.ToUpper(convID))).To(BeTrue())
		Expect(registry.IsUUID("my-name")).To(BeFalse())
		Expect(registry.IsUUID(convID + "0")).To(BeFalse())
	})
})
