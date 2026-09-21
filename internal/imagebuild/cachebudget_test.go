package imagebuild_test

// Spec: spec/image-build.feature CS-IMG-042/043 — the detached checker's
// result file and its one-shot consumption. Real files in a scratch dir;
// docker is execx.Fake.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

var _ = Describe("detached cache-budget check", func() {
	const (
		dfSmall  = `{"Size":"10GB","Type":"Build Cache"}` + "\n"
		gcSmallC = "GC Policy rule#0:\n All: false\n Filters: type==exec.cachemount\n Max Used Space: 2.764GB\n" +
			"GC Policy rule#1:\n All: true\n Max Used Space: 669.6GiB\n"
		gcHealthy = "GC Policy rule#0:\n All: false\n Filters: type==exec.cachemount\n Max Used Space: 11.58GiB\n" +
			"GC Policy rule#1:\n All: true\n Max Used Space: 669.6GiB\n"
	)
	var (
		fake *execx.Fake
		o    imagebuild.Options
		dir  string
		at   time.Time
	)
	clock := func() time.Time { return at }
	result := func() string { return filepath.Join(dir, imagebuild.CacheBudgetFile) }
	read := func() imagebuild.CacheBudgetResult {
		data, err := os.ReadFile(result())
		Expect(err).NotTo(HaveOccurred())
		var r imagebuild.CacheBudgetResult
		Expect(json.Unmarshal(data, &r)).To(Succeed())
		return r
	}

	BeforeEach(func() {
		fake = &execx.Fake{}
		o = imagebuild.Options{Runner: fake, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		dir = filepath.Join(GinkgoT().TempDir(), "cache")
		at = time.Date(2026, 9, 21, 18, 3, 0, 0, time.UTC)
	})

	Describe("RunCacheBudgetCheck", func() {
		It("CS-IMG-042: writes the report and the time of the check, creating the dir", func() {
			fake.On("docker system df --format {{json .}}", dfSmall, nil)
			fake.On("docker buildx inspect", gcSmallC, nil)
			Expect(imagebuild.RunCacheBudgetCheck(o, dir, clock)).To(Succeed())
			r := read()
			Expect(r.CheckedAt.Equal(at)).To(BeTrue())
			Expect(r.Report).To(ContainSubstring("caps cache mounts at 3 GB"))
			Expect(r.Report).To(ContainSubstring("Pruning does not help"))
			// Nothing reaches the checker's own streams: the file is the output.
			Expect(o.Err.(*bytes.Buffer).String()).To(BeEmpty())
			// Atomic write: no temp file is left beside it.
			entries, err := os.ReadDir(dir)
			Expect(err).NotTo(HaveOccurred())
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			Expect(names).To(ConsistOf(imagebuild.CacheBudgetFile, imagebuild.CacheBudgetLockFile))
		})

		It("CS-IMG-042: a healthy host, or unparseable docker output, writes an empty report", func() {
			fake.On("docker system df --format {{json .}}", dfSmall, nil)
			fake.On("docker buildx inspect", gcHealthy, nil)
			Expect(imagebuild.RunCacheBudgetCheck(o, dir, clock)).To(Succeed())
			Expect(read().Report).To(BeEmpty())

			fake2 := &execx.Fake{}
			fake2.On("docker system df", "not json\n", nil)
			o.Runner = fake2
			Expect(imagebuild.RunCacheBudgetCheck(o, dir, clock)).To(Succeed())
			Expect(read().Report).To(BeEmpty())
		})

		It("CS-IMG-042: on lock contention it returns at once, running and writing nothing", func() {
			Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
			held, err := os.OpenFile(filepath.Join(dir, imagebuild.CacheBudgetLockFile), os.O_RDWR|os.O_CREATE, 0o600)
			Expect(err).NotTo(HaveOccurred())
			defer held.Close()
			Expect(syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())

			done := make(chan error, 1)
			go func() { done <- imagebuild.RunCacheBudgetCheck(o, dir, clock) }()
			Eventually(done, time.Second).Should(Receive(MatchError(imagebuild.ErrCacheBudgetBusy)))
			Expect(fake.Calls).To(BeEmpty())
			Expect(result()).NotTo(BeAnExistingFile())

			// Released (a checker that died releases it too): the next one runs.
			Expect(syscall.Flock(int(held.Fd()), syscall.LOCK_UN)).To(Succeed())
			Expect(imagebuild.RunCacheBudgetCheck(o, dir, clock)).To(Succeed())
			Expect(result()).To(BeAnExistingFile())
		})
	})

	Describe("ConsumeCacheBudget", func() {
		write := func(content string) {
			Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
			Expect(os.WriteFile(result(), []byte(content), 0o644)).To(Succeed())
		}

		It("CS-IMG-043: prints a non-empty report once, headed by the time of the check, and removes the file", func() {
			fake.On("docker system df --format {{json .}}", dfSmall, nil)
			fake.On("docker buildx inspect", gcSmallC, nil)
			Expect(imagebuild.RunCacheBudgetCheck(o, dir, clock)).To(Succeed())
			fake.Calls = nil

			var w bytes.Buffer
			imagebuild.ConsumeCacheBudget(dir, &w)
			Expect(w.String()).To(ContainSubstring("Build-cache check after the image build of " + at.Local().Format("2006-01-02 15:04")))
			Expect(w.String()).To(ContainSubstring("caps cache mounts at 3 GB"))
			Expect(result()).NotTo(BeAnExistingFile())
			Expect(fake.Calls).To(BeEmpty(), "reading the result costs no docker call")

			w.Reset()
			imagebuild.ConsumeCacheBudget(dir, &w)
			Expect(w.String()).To(BeEmpty(), "once")
		})

		It("CS-IMG-043: an empty report prints nothing and is removed", func() {
			write(`{"checkedAt":"2026-09-21T18:03:00Z","report":""}`)
			var w bytes.Buffer
			imagebuild.ConsumeCacheBudget(dir, &w)
			Expect(w.String()).To(BeEmpty())
			Expect(result()).NotTo(BeAnExistingFile())
		})

		It("CS-IMG-043: an unreadable file prints nothing and is removed", func() {
			write("{not json")
			var w bytes.Buffer
			imagebuild.ConsumeCacheBudget(dir, &w)
			Expect(w.String()).To(BeEmpty())
			Expect(result()).NotTo(BeAnExistingFile())
			entries, _ := os.ReadDir(dir)
			Expect(entries).To(BeEmpty(), "the claimed copy is removed too")
		})

		It("CS-IMG-043: no file, no output", func() {
			var w bytes.Buffer
			imagebuild.ConsumeCacheBudget(filepath.Join(dir, "missing"), &w)
			Expect(w.String()).To(BeEmpty())
		})
	})
})
