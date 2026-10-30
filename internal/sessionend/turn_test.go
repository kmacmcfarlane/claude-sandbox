package sessionend_test

// Spec: spec/sessions.feature CS-SESS-108 and spec/launch.feature
// CS-LNCH-218 — the exports the restart shares with the helper: the turn
// rule (Turn, with its Clock seam) and one attempt at end.lock (TryEndLock).

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessionend"
)

var _ = Describe("the turn rule and end.lock, as exported (CS-SESS-108, CS-LNCH-218)", func() {
	var f *fixture
	BeforeEach(func() { f = newFixture() })

	turn := func() *sessionend.Turn {
		return sessionend.NewTurn(sessionend.TurnOptions{
			Runner: f.fake, ProcRoot: f.proc, Container: cid, Class: 7, Since: t0.UnixMilli(),
			RegistryDirs: []string{f.reg}, Clock: f.clock,
		})
	}

	It("CS-SESS-108: idle or waiting records end; busy, shell or unknown wait; none is none", func() {
		v, recs, err := turn().Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(sessionend.VerdictNone))
		Expect(recs).To(BeEmpty())

		f.record(263, conv1, "idle", "", t0.Add(time.Second))
		f.record(519, conv2, "waiting", "", t0.Add(time.Minute))
		v, recs, err = turn().Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(sessionend.VerdictEnd))
		Expect(recs).To(HaveLen(2))

		f.record(519, conv2, "busy", "", t0.Add(time.Minute))
		v, _, _ = turn().Read()
		Expect(v).To(Equal(sessionend.VerdictWait))

		f.record(519, conv2, "shell", "", t0.Add(time.Minute))
		v, _, _ = turn().Read()
		Expect(v).To(Equal(sessionend.VerdictWait))
	})

	It("CS-SESS-108: a record never seen parsing counts as unknown work (wait)", func() {
		f.record(263, conv1, "idle", "", t0.Add(time.Second))
		Expect(os.WriteFile(filepath.Join(f.reg, "519.json"), []byte(`{"pid":519,`), 0o600)).To(Succeed())
		v, recs, err := turn().Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(sessionend.VerdictWait))
		Expect(recs).To(HaveLen(1))
	})

	It("CS-LNCH-218: the Clock seam: a live record is confirmed again only after LiveRecheck", func() {
		f.procEntry(9001, 4242)
		f.fake.On(topP, "PID\n9001\n", nil)
		f.record(263, conv1, "busy", "4242", t0.Add(time.Second))
		tr := turn()
		v, _, _ := tr.Read()
		Expect(v).To(Equal(sessionend.VerdictWait))
		Expect(f.calls(topP)).To(Equal(1))
		f.clock.now = f.clock.now.Add(sessionend.LiveRecheck - time.Second)
		tr.Read()
		Expect(f.calls(topP)).To(Equal(1))
		f.clock.now = f.clock.now.Add(2 * time.Second)
		tr.Read()
		Expect(f.calls(topP)).To(Equal(2))
	})

	It("CS-LNCH-218: TryEndLock takes the lock once; a second attempt reads held; closing releases it", func() {
		l1, held, err := sessionend.TryEndLock(f.state, cid)
		Expect(err).NotTo(HaveOccurred())
		Expect(held).To(BeFalse())
		Expect(l1).NotTo(BeNil())
		fi, err := os.Lstat(filepath.Join(clients.ContainerDir(f.state, cid), clients.EndLockName))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		l2, held, err := sessionend.TryEndLock(f.state, cid)
		Expect(err).NotTo(HaveOccurred())
		Expect(held).To(BeTrue())
		Expect(l2).To(BeNil())

		Expect(l1.Close()).To(Succeed())
		l3, held, err := sessionend.TryEndLock(f.state, cid)
		Expect(err).NotTo(HaveOccurred())
		Expect(held).To(BeFalse())
		l3.Close()
	})

	It("CS-LNCH-218: a lock file that is a symlink is an error, never followed", func() {
		dir := clients.ContainerDir(f.state, cid)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		Expect(os.Symlink(filepath.Join(f.state, "elsewhere"), filepath.Join(dir, clients.EndLockName))).To(Succeed())
		_, _, err := sessionend.TryEndLock(f.state, cid)
		Expect(err).To(MatchError(syscall.ELOOP))
	})
})
