package main

// Spec: spec/tmux.feature (CS-TMUX-001..002) — the bin/claude-sandbox shim
// execs the launcher with argv[0] "claude-sandbox", so the first word of the
// full command line tmux-resurrect saves matches a plain `claude-sandbox`
// @resurrect-processes entry (the bin/dist path, possibly with a space, never did).
//
// The shim is run for real (bash) from a scratch copy of the repo layout. Its
// bin/dist/claude-sandbox is a symlink to THIS test binary: a script would not
// do, because the kernel replaces a script's argv[0] with the interpreter's.
// When shimArgv0Env is set, the init below records os.Args and the repo-root
// variable and exits before any test runs.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const shimArgv0Env = "CLAUDE_SANDBOX_TEST_SHIM_ARGV0_OUT"

type shimArgv0Record struct {
	Args     []string `json:"args"`
	RepoRoot string   `json:"repoRoot"`
}

func init() {
	out := os.Getenv(shimArgv0Env)
	if out == "" {
		return
	}
	b, _ := json.Marshal(shimArgv0Record{Args: os.Args, RepoRoot: os.Getenv("CLAUDE_SANDBOX_REPO_ROOT")})
	if err := os.WriteFile(out, b, 0o600); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

var _ = Describe("shim argv0 (spec/tmux.feature)", func() {
	var (
		repo string
		out  string
	)

	BeforeEach(func() {
		scratch := GinkgoT().TempDir()
		repo = filepath.Join(scratch, "repo")
		Expect(os.MkdirAll(filepath.Join(repo, "bin", "dist"), 0o755)).To(Succeed())
		// The shim resolves its own path (readlink -f); compare physical paths.
		var err error
		repo, err = filepath.EvalSymlinks(repo)
		Expect(err).NotTo(HaveOccurred())

		shim, err := os.ReadFile(filepath.Join("..", "..", "bin", "claude-sandbox"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(repo, "bin", "claude-sandbox"), shim, 0o755)).To(Succeed())

		self, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		// No build sources exist in the scratch repo, so the shim never builds.
		Expect(os.Symlink(self, filepath.Join(repo, "bin", "dist", "claude-sandbox"))).To(Succeed())

		out = filepath.Join(scratch, "argv.json")
	})

	// shimRaw runs the shim and returns its combined output and error.
	shimRaw := func(args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{filepath.Join(repo, "bin", "claude-sandbox")}, args...)...)
		env := []string{shimArgv0Env + "=" + out}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, shimArgv0Env+"=") && !strings.HasPrefix(kv, "CLAUDE_SANDBOX_REPO_ROOT=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env
		combined, err := cmd.CombinedOutput()
		return string(combined), err
	}
	runShim := func(args ...string) shimArgv0Record {
		combined, err := shimRaw(args...)
		Expect(err).NotTo(HaveOccurred(), combined)
		b, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())
		var rec shimArgv0Record
		Expect(json.Unmarshal(b, &rec)).To(Succeed())
		return rec
	}

	It("CS-TMUX-001: the shim execs the launcher with argv[0] \"claude-sandbox\"", func() {
		rec := runShim("--new", "--", "--resume", "a b", "")
		Expect(rec.Args).To(Equal([]string{"claude-sandbox", "--new", "--", "--resume", "a b", ""}))
		Expect(rec.RepoRoot).To(Equal(repo))
	})

	It("CS-TMUX-003: tmux save execs an up-to-date binary with argv[0] \"claude-sandbox\" and prints nothing", func() {
		combined, err := shimRaw("tmux", "save", "/r/tmux_resurrect_x.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(combined).To(BeEmpty())
		b, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())
		var rec shimArgv0Record
		Expect(json.Unmarshal(b, &rec)).To(Succeed())
		Expect(rec.Args).To(Equal([]string{"claude-sandbox", "tmux", "save", "/r/tmux_resurrect_x.txt"}))
		Expect(rec.RepoRoot).To(Equal(repo))
	})

	It("CS-TMUX-003: tmux save never builds: a stale or missing binary is exit 0, silent, and not run", func() {
		// A source newer than the binary: every other path would build.
		src := filepath.Join(repo, "cmd", "claude-sandbox", "main.go")
		Expect(os.MkdirAll(filepath.Dir(src), 0o755)).To(Succeed())
		Expect(os.WriteFile(src, []byte("package main\n"), 0o644)).To(Succeed())
		future := time.Now().Add(time.Hour)
		Expect(os.Chtimes(src, future, future)).To(Succeed())
		combined, err := shimRaw("tmux", "save", "/r/tmux_resurrect_x.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(combined).To(BeEmpty(), "no \"Building…\" line")
		Expect(out).NotTo(BeAnExistingFile(), "a stale binary is not run")
		Expect(filepath.Join(repo, "bin", "dist", "gocache")).NotTo(BeADirectory())

		Expect(os.Remove(filepath.Join(repo, "bin", "dist", "claude-sandbox"))).To(Succeed())
		combined, err = shimRaw("tmux", "save", "/r/tmux_resurrect_x.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(combined).To(BeEmpty())
		Expect(out).NotTo(BeAnExistingFile())
	})

	It("CS-TMUX-002: the completion fast path execs with the same argv[0]", func() {
		for _, sub := range []string{"__complete", "__completeNoDesc"} {
			rec := runShim(sub, "--attach=", "")
			Expect(rec.Args).To(Equal([]string{"claude-sandbox", sub, "--attach=", ""}))
			Expect(rec.RepoRoot).To(Equal(repo))
		}
	})
})

// Spec: spec/tmux.feature (CS-TMUX-073) — the shim's builds serialise on a
// flock in bin/dist/.build.lock. The shim runs for real (bash) in a scratch
// repo, with a PATH holding only symlinks to the tools it needs plus a fake
// `go` (or `docker`) that sleeps, counts its builds and writes a script as the
// binary; that script records whether fd 9 (the lock) reached it.
var _ = Describe("shim build lock (spec/tmux.feature)", func() {
	var (
		repo    string
		binDir  string
		pathDir string
		count   string
		ran     string
	)

	tools := []string{"bash", "sh", "readlink", "dirname", "find", "mkdir", "sleep", "chmod", "id", "cat"}

	BeforeEach(func() {
		scratch := GinkgoT().TempDir()
		repo = filepath.Join(scratch, "repo")
		Expect(os.MkdirAll(filepath.Join(repo, "bin"), 0o755)).To(Succeed())
		var err error
		repo, err = filepath.EvalSymlinks(repo)
		Expect(err).NotTo(HaveOccurred())
		shim, err := os.ReadFile(filepath.Join("..", "..", "bin", "claude-sandbox"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(repo, "bin", "claude-sandbox"), shim, 0o755)).To(Succeed())
		// A build source and no binary: every launch path is stale.
		src := filepath.Join(repo, "cmd", "claude-sandbox", "main.go")
		Expect(os.MkdirAll(filepath.Dir(src), 0o755)).To(Succeed())
		Expect(os.WriteFile(src, []byte("package main\n"), 0o644)).To(Succeed())
		past := time.Now().Add(-time.Hour)
		Expect(os.Chtimes(src, past, past)).To(Succeed())

		binDir = filepath.Join(repo, "bin", "dist")
		pathDir = filepath.Join(scratch, "path")
		Expect(os.MkdirAll(pathDir, 0o755)).To(Succeed())
		for _, t := range tools {
			p, err := exec.LookPath(t)
			Expect(err).NotTo(HaveOccurred(), t)
			Expect(os.Symlink(p, filepath.Join(pathDir, t))).To(Succeed())
		}
		count = filepath.Join(scratch, "builds")
		ran = filepath.Join(scratch, "ran")
	})

	// built is the body of the "binary" the fakes write: it records one line
	// per run, "leaked" when fd 9 is open in it.
	built := func() string {
		return "#!/bin/sh\nif [ -e /proc/$$/fd/9 ]; then echo leaked >> '" + ran + "'; else echo ok >> '" + ran + "'; fi\n"
	}
	// fakeGo writes a `go` that takes 1 s and then writes the -o target.
	fakeGo := func() {
		script := "#!/bin/sh\necho build >> '" + count + "'\nsleep 1\nout=\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=$2; shift; done\n" +
			"cat > \"$out\" <<'BIN'\n" + built() + "BIN\nchmod +x \"$out\"\n"
		Expect(os.WriteFile(filepath.Join(pathDir, "go"), []byte(script), 0o755)).To(Succeed())
	}
	// fakeDocker writes a `docker` that maps "-v <repo>:/src" and writes
	// <repo>/bin/dist/claude-sandbox the same way.
	fakeDocker := func() {
		script := "#!/bin/sh\necho build >> '" + count + "'\nsleep 1\nsrc=\nwhile [ $# -gt 0 ]; do [ \"$1\" = -v ] && src=${2%:/src}; shift; done\n" +
			"out=\"$src/bin/dist/claude-sandbox\"\ncat > \"$out\" <<'BIN'\n" + built() + "BIN\nchmod +x \"$out\"\n"
		Expect(os.WriteFile(filepath.Join(pathDir, "docker"), []byte(script), 0o755)).To(Succeed())
	}
	withFlock := func() {
		p, err := exec.LookPath("flock")
		if err != nil {
			Skip("flock (util-linux) is not installed")
		}
		Expect(os.Symlink(p, filepath.Join(pathDir, "flock"))).To(Succeed())
	}
	shimCmd := func(args ...string) *exec.Cmd {
		bash := filepath.Join(pathDir, "bash")
		cmd := exec.Command(bash, append([]string{filepath.Join(repo, "bin", "claude-sandbox")}, args...)...)
		cmd.Env = []string{"PATH=" + pathDir, "HOME=" + repo}
		return cmd
	}
	type result struct {
		out string
		err error
	}
	race := func(n int) []result {
		res := make([]result, n)
		done := make(chan int, n)
		for i := 0; i < n; i++ {
			go func(i int) {
				b, err := shimCmd("--new").CombinedOutput()
				res[i] = result{string(b), err}
				done <- i
			}(i)
		}
		for i := 0; i < n; i++ {
			Eventually(done, 30*time.Second).Should(Receive())
		}
		return res
	}
	lines := func(p string) []string {
		b, err := os.ReadFile(p)
		Expect(err).NotTo(HaveOccurred())
		return strings.Fields(string(b))
	}

	for _, kind := range []string{"host go", "docker golang"} {
		kind := kind
		It("CS-TMUX-073: concurrent stale launches run one build ("+kind+"); the rest wait and exec it", func() {
			withFlock()
			if kind == "host go" {
				fakeGo()
			} else {
				fakeDocker()
			}
			// The test holds the lock first, so every shim queues behind it
			// whatever the scheduling; released, one builds and two find the
			// binary up to date under the lock.
			Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
			f, err := os.OpenFile(filepath.Join(binDir, ".build.lock"), os.O_RDWR|os.O_CREATE, 0o600)
			Expect(err).NotTo(HaveOccurred())
			defer f.Close()
			Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
			release := time.AfterFunc(time.Second, func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) })
			defer release.Stop()
			res := race(3)
			building, waiting := 0, 0
			for _, r := range res {
				Expect(r.err).NotTo(HaveOccurred(), r.out)
				if strings.Contains(r.out, "Building claude-sandbox binary") {
					building++
				}
				if strings.Contains(r.out, "Waiting for another claude-sandbox build") {
					waiting++
				}
			}
			Expect(lines(count)).To(HaveLen(1), "one build for three launches")
			Expect(building).To(Equal(1))
			Expect(waiting).To(Equal(3))
			Expect(lines(ran)).To(Equal([]string{"ok", "ok", "ok"}), "every launch execs the binary, and fd 9 never reaches it")
		})
	}

	It("CS-TMUX-073: without flock the shim builds unlocked, as before, with no message", func() {
		fakeGo()
		res := race(2)
		for _, r := range res {
			Expect(r.err).NotTo(HaveOccurred(), r.out)
			Expect(r.out).NotTo(ContainSubstring("Waiting"))
			Expect(r.out).NotTo(ContainSubstring("WARNING"))
		}
		Expect(lines(count)).To(HaveLen(2))
		Expect(filepath.Join(binDir, ".build.lock")).NotTo(BeAnExistingFile())
		Expect(lines(ran)).To(Equal([]string{"ok", "ok"}))
	})

	It("CS-TMUX-073: an up-to-date binary takes no lock and builds nothing", func() {
		withFlock()
		fakeGo()
		Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(binDir, "claude-sandbox"), []byte(built()), 0o755)).To(Succeed())
		b, err := shimCmd("--new").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(b))
		Expect(string(b)).To(BeEmpty())
		Expect(count).NotTo(BeAnExistingFile())
		Expect(filepath.Join(binDir, ".build.lock")).NotTo(BeAnExistingFile())
		Expect(lines(ran)).To(Equal([]string{"ok"}))
	})

	It("CS-TMUX-073: the tmux save fast path never waits on a held build lock", func() {
		withFlock()
		fakeGo()
		Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(binDir, "claude-sandbox"), []byte(built()), 0o755)).To(Succeed())
		f, err := os.OpenFile(filepath.Join(binDir, ".build.lock"), os.O_RDWR|os.O_CREATE, 0o600)
		Expect(err).NotTo(HaveOccurred())
		defer f.Close()
		Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
		// Stale too: the hook neither builds nor runs it, and never queues.
		future := time.Now().Add(time.Hour)
		Expect(os.Chtimes(filepath.Join(repo, "cmd", "claude-sandbox", "main.go"), future, future)).To(Succeed())
		start := time.Now()
		b, err := shimCmd("tmux", "save", "/r/tmux_resurrect_x.txt").CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(BeEmpty())
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
		Expect(count).NotTo(BeAnExistingFile())
		Expect(ran).NotTo(BeAnExistingFile())
	})
})
