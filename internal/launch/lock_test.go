package launch_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("host launch lock (CS-SESS-048)", func() {
	var home string

	BeforeEach(func() {
		home = GinkgoT().TempDir()
	})

	It("CS-SESS-048: lives at ~/.cache/claude-sandbox/launch.lock and is created as the invoking user", func() {
		path := launch.LaunchLockPath(home)
		Expect(path).To(Equal(filepath.Join(home, ".cache", "claude-sandbox", "launch.lock")))

		release, err := launch.FileLock{Path: path}.Acquire()
		Expect(err).NotTo(HaveOccurred())
		defer release()
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(filepath.Join(home, ".cache", "claude-sandbox")).To(BeADirectory())
	})

	It("CS-SESS-048: is exclusive until released", func() {
		path := launch.LaunchLockPath(home)
		release, err := launch.FileLock{Path: path}.Acquire()
		Expect(err).NotTo(HaveOccurred())

		// flock binds to the open file description, so a second open in the
		// same process contends exactly like a second launcher would.
		_, err = launch.FileLock{Path: path, Timeout: 60 * time.Millisecond}.Acquire()
		Expect(err).To(MatchError(ContainSubstring("still held")))

		release()
		again, err := launch.FileLock{Path: path, Timeout: time.Second}.Acquire()
		Expect(err).NotTo(HaveOccurred())
		again()
	})

	It("CS-SESS-048: a lock directory that cannot be created is an error, not a hang", func() {
		blocker := filepath.Join(home, ".cache")
		Expect(os.WriteFile(blocker, nil, 0o644)).To(Succeed()) // a file where the dir must go
		_, err := launch.FileLock{Path: launch.LaunchLockPath(home)}.Acquire()
		Expect(err).To(HaveOccurred())
	})
})
