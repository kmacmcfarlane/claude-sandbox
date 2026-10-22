package clients_test

// Spec: spec/launch.feature CS-LNCH-204 (the lease), spec/host-dirs.feature
// CS-DIR-029 (where it lives, the prune). Real flock on scratch files: flock
// locks belong to an open file description, so a second open in this same
// process conflicts with the first exactly as another process would.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

const (
	idA = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"
	idB = "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"
)

func leaseFiles(root, id string) []string {
	ents, err := os.ReadDir(clients.ContainerDir(root, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// lockable reports whether LOCK_EX|LOCK_NB on p succeeds right now.
func lockable(p string) bool {
	f, err := os.Open(p)
	Expect(err).NotTo(HaveOccurred())
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

var _ = Describe("client leases (CS-LNCH-204, CS-DIR-029)", func() {
	var root string
	BeforeEach(func() { root = GinkgoT().TempDir() })

	It("CS-LNCH-204, CS-DIR-029: a lease is a locked <pid>.<rand>.lease under <StateRoot>/clients/<id>/, 0700 dirs, 0600 file", func() {
		since := time.UnixMilli(1_790_000_000_000)
		l, err := clients.Take(root, idA, clients.KindAttach, since, "%7")
		Expect(err).NotTo(HaveOccurred())
		defer l.Release()

		Expect(filepath.Dir(l.Path)).To(Equal(filepath.Join(root, "clients", idA)))
		Expect(filepath.Base(l.Path)).To(MatchRegexp(`^[0-9]+\.[0-9a-f]{16}\.lease$`))
		Expect(leaseFiles(root, idA)).To(ConsistOf(filepath.Base(l.Path)), "no temp file left")
		for _, d := range []string{filepath.Join(root, "clients"), filepath.Dir(l.Path)} {
			fi, err := os.Stat(d)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		}
		fi, err := os.Lstat(l.Path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		var rec clients.Record
		b, _ := os.ReadFile(l.Path)
		Expect(json.Unmarshal(b, &rec)).To(Succeed())
		Expect(rec).To(Equal(clients.Record{V: 1, Kind: clients.KindAttach, Since: since.UnixMilli(), TmuxPane: "%7"}))
		Expect(lockable(l.Path)).To(BeFalse(), "held while the client lives")
	})

	It("CS-LNCH-204: a reader sees a held lease as live, and a released one is gone", func() {
		l, err := clients.Take(root, idA, clients.KindPrimary, time.Now(), "")
		Expect(err).NotTo(HaveOccurred())
		ls, err := clients.ReadLeases(root, idA)
		Expect(err).NotTo(HaveOccurred())
		Expect(ls).To(HaveLen(1))
		Expect(ls[0].Held).To(BeTrue())
		Expect(ls[0].Record.Kind).To(Equal(clients.KindPrimary))

		l.Release()
		l.Release() // idempotent
		Expect(leaseFiles(root, idA)).To(BeEmpty())
		ls, err = clients.ReadLeases(root, idA)
		Expect(err).NotTo(HaveOccurred())
		Expect(ls).To(BeEmpty())
		var nilLease *clients.Lease
		Expect(nilLease.Release).NotTo(Panic())
	})

	It("CS-LNCH-204: a lease whose holder died (lock dropped, file left) reads as not held", func() {
		dir := clients.ContainerDir(root, idA)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		dead := filepath.Join(dir, "4242.0123456789abcdef.lease")
		Expect(os.WriteFile(dead, []byte(`{"v":1,"kind":"attach","since":1}`), 0o600)).To(Succeed())
		ls, err := clients.ReadLeases(root, idA)
		Expect(err).NotTo(HaveOccurred())
		Expect(ls).To(HaveLen(1))
		Expect(ls[0].Held).To(BeFalse())
	})

	It("CS-LNCH-204: refuses an id that is not 64 hex, and panics on an empty state root under go test", func() {
		_, err := clients.Take(root, "abc", clients.KindAttach, time.Now(), "")
		Expect(err).To(HaveOccurred())
		Expect(func() { clients.Take("", idA, clients.KindAttach, time.Now(), "") }).To(PanicWith(ContainSubstring("real one")))
	})

	It("CS-DIR-029: a state root at or inside the real one panics under go test, before anything is written", func() {
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		real := hostdirs.StateRoot(home, os.Getenv)
		for _, r := range []string{real, real + "/", filepath.Join(real, "sub"), filepath.Join(home, ".local/state/claude-sandbox")} {
			Expect(func() { clients.Take(r, idA, clients.KindAttach, time.Now(), "") }).To(PanicWith(ContainSubstring("real state root")), r)
			Expect(func() { clients.ReadLeases(r, idA) }).To(PanicWith(ContainSubstring("real state root")), r)
			Expect(func() { clients.Prune(r, nil, nil, time.Now()) }).To(PanicWith(ContainSubstring("real state root")), r)
		}
		// Reached through a symlink: resolved before the comparison.
		link := filepath.Join(GinkgoT().TempDir(), "state")
		Expect(os.Symlink(home, link)).To(Succeed())
		Expect(func() {
			clients.Take(filepath.Join(link, ".local/state/claude-sandbox"), idA, clients.KindAttach, time.Now(), "")
		}).
			To(PanicWith(ContainSubstring("real state root")))
	})

	It("CS-LNCH-204: a state root it cannot own (a symlink) fails the take, nothing is followed", func() {
		target := GinkgoT().TempDir()
		link := filepath.Join(GinkgoT().TempDir(), "state")
		Expect(os.Symlink(target, link)).To(Succeed())
		_, err := clients.Take(link, idA, clients.KindAttach, time.Now(), "")
		Expect(err).To(MatchError(ContainSubstring("symlink")))
		Expect(leaseFiles(target, idA)).To(BeEmpty())
	})

	It("CS-LNCH-204: in the temp-then-rename window a reader sees no lease and a prune keeps the locked temp file", func() {
		var during []clients.LeaseInfo
		var tmpKept bool
		defer clients.SetTestHook(func(step, tmp string) {
			if step != "locked" {
				return
			}
			var err error
			during, err = clients.ReadLeases(root, idA)
			Expect(err).NotTo(HaveOccurred())
			// Old enough by the clock, but locked: kept.
			Expect(clients.Prune(root, nil, nil, time.Now().Add(time.Hour))).To(Succeed())
			_, err = os.Lstat(tmp)
			tmpKept = err == nil
		})()
		l, err := clients.Take(root, idA, clients.KindJoin, time.Now(), "")
		Expect(err).NotTo(HaveOccurred())
		defer l.Release()
		Expect(during).To(BeEmpty(), "the temp file is never read as a lease")
		Expect(tmpKept).To(BeTrue())
		Expect(lockable(l.Path)).To(BeFalse())
	})

	It("CS-LNCH-204: a prune that removes the unlocked temp file (or the directory) before the flock makes Take retry", func() {
		calls := 0
		defer clients.SetTestHook(func(step, tmp string) {
			if step != "created" {
				return
			}
			calls++
			if calls == 1 {
				// The id is unknown to discovery and the temp file looks old:
				// the prune locks it, unlinks it and removes the directory.
				Expect(clients.Prune(root, []string{idB}, nil, time.Now().Add(time.Hour))).To(Succeed())
				_, err := os.Lstat(filepath.Dir(tmp))
				Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
			}
		})()
		l, err := clients.Take(root, idA, clients.KindAttach, time.Now(), "")
		Expect(err).NotTo(HaveOccurred())
		defer l.Release()
		Expect(calls).To(Equal(2))
		Expect(lockable(l.Path)).To(BeFalse())
		Expect(leaseFiles(root, idA)).To(ConsistOf(filepath.Base(l.Path)))
	})

	It("CS-LNCH-204: a prune holding the temp file's lock makes Take retry rather than wait", func() {
		calls := 0
		var held *os.File
		defer clients.SetTestHook(func(step, tmp string) {
			if step == "created" {
				calls++
				if calls == 1 {
					f, err := os.Open(tmp)
					Expect(err).NotTo(HaveOccurred())
					Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
					held = f
				}
			}
		})()
		l, err := clients.Take(root, idA, clients.KindAttach, time.Now(), "")
		Expect(err).NotTo(HaveOccurred())
		defer l.Release()
		held.Close()
		Expect(calls).To(Equal(2))
		Expect(leaseFiles(root, idA)).To(ContainElement(filepath.Base(l.Path)))
	})
})

var _ = Describe("pruning client directories (CS-DIR-029)", func() {
	var root string
	now := time.Now()
	BeforeEach(func() { root = GinkgoT().TempDir() })

	write := func(id, name string) string {
		dir := clients.ContainerDir(root, id)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		p := filepath.Join(dir, name)
		Expect(os.WriteFile(p, []byte("{}"), 0o600)).To(Succeed())
		return p
	}

	It("CS-DIR-029: a directory no discovered row names goes whole once its leases are dead; a held lease keeps it", func() {
		write(idA, "1.00.lease")
		write(idA, "left.json")
		live, err := clients.Take(root, idB, clients.KindAttach, now, "")
		Expect(err).NotTo(HaveOccurred())
		defer live.Release()
		write(idB, "2.00.lease") // a dead lease beside a live one

		Expect(clients.Prune(root, nil, nil, now)).To(Succeed())
		_, err = os.Lstat(clients.ContainerDir(root, idA))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
		Expect(leaseFiles(root, idB)).To(ConsistOf(filepath.Base(live.Path)), "only the dead lease went")
	})

	It("CS-DIR-029: in a directory a row names, only dead leases go", func() {
		dead := write(idA, "1.00.lease")
		other := write(idA, "left.json")
		Expect(clients.Prune(root, []string{idA}, nil, now)).To(Succeed())
		_, err := os.Lstat(dead)
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
		Expect(other).To(BeAnExistingFile())
	})

	It("CS-DIR-029: a failed discovery, or a row without a 64-hex id, removes nothing", func() {
		dead := write(idA, "1.00.lease")
		Expect(clients.Prune(root, nil, errors.New("docker ps: boom"), now)).To(Succeed())
		Expect(dead).To(BeAnExistingFile())
		Expect(clients.Prune(root, []string{idB, "abc123"}, nil, now)).To(Succeed())
		Expect(dead).To(BeAnExistingFile())
	})

	It("CS-DIR-029: a held *.lock (the session-end helper's end.lock) keeps the whole directory", func() {
		dead := write(idA, "1.00.lease")
		lock := write(idA, clients.EndLockName)
		f, err := os.Open(lock)
		Expect(err).NotTo(HaveOccurred())
		Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
		Expect(clients.Prune(root, nil, nil, now)).To(Succeed())
		Expect(dead).To(BeAnExistingFile())
		Expect(lock).To(BeAnExistingFile())

		f.Close() // the helper exited
		Expect(clients.Prune(root, nil, nil, now)).To(Succeed())
		_, err = os.Lstat(clients.ContainerDir(root, idA))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
	})

	It("CS-DIR-029: in a directory a row names, no lock file is ever locked; a held end.lock does not stop dead leases going", func() {
		dead := write(idA, "1.00.lease")
		lock := write(idA, clients.EndLockName)
		f, err := os.Open(lock)
		Expect(err).NotTo(HaveOccurred())
		defer f.Close()
		Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
		Expect(clients.Prune(root, []string{idA}, nil, now)).To(Succeed())
		_, err = os.Lstat(dead)
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
		Expect(lock).To(BeAnExistingFile())
	})

	It("CS-DIR-029: a young temp file is a lease being taken and stays; an old dead one goes", func() {
		tmp := write(idA, ".1.00.tmp")
		Expect(clients.Prune(root, []string{idA}, nil, now)).To(Succeed())
		Expect(tmp).To(BeAnExistingFile())
		Expect(clients.Prune(root, []string{idA}, nil, now.Add(2*clients.TempMinAge))).To(Succeed())
		_, err := os.Lstat(tmp)
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
	})

	It("CS-DIR-029: never follows a symlinked clients dir or lease, and ignores names that are not ids", func() {
		target := GinkgoT().TempDir()
		victim := filepath.Join(target, "x.lease")
		Expect(os.WriteFile(victim, nil, 0o600)).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(root, "clients"))).To(Succeed())
		Expect(clients.Prune(root, nil, nil, now)).To(Succeed())
		Expect(victim).To(BeAnExistingFile())

		Expect(os.Remove(filepath.Join(root, "clients"))).To(Succeed())
		dir := clients.ContainerDir(root, idA)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		Expect(os.Symlink(victim, filepath.Join(dir, "9.00.lease"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "clients", "not-an-id"), 0o700)).To(Succeed())
		Expect(clients.Prune(root, []string{idA}, nil, now)).To(Succeed())
		Expect(victim).To(BeAnExistingFile())
		Expect(filepath.Join(root, "clients", "not-an-id")).To(BeADirectory())
	})

	It("CS-DIR-029: a missing clients dir is no error", func() {
		Expect(clients.Prune(root, nil, nil, now)).To(Succeed())
		Expect(strings.HasSuffix(clients.ContainerDir(root, idA), idA)).To(BeTrue())
	})
})
