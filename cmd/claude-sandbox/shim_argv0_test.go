package main

// Spec: spec/tmux.feature (CS-TMUX-001..003, CS-TMUX-068) — the bin/claude-sandbox shim
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
	"errors"
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

	It("CS-TMUX-068: tmux restore --pin and --rearm exec an up-to-date binary with argv[0] \"claude-sandbox\" and print nothing", func() {
		for _, form := range []string{"--pin", "--rearm"} {
			Expect(os.RemoveAll(out)).To(Succeed())
			combined, err := shimRaw("tmux", "restore", form)
			Expect(err).NotTo(HaveOccurred(), form)
			Expect(combined).To(BeEmpty(), form)
			b, err := os.ReadFile(out)
			Expect(err).NotTo(HaveOccurred(), form)
			var rec shimArgv0Record
			Expect(json.Unmarshal(b, &rec)).To(Succeed())
			Expect(rec.Args).To(Equal([]string{"claude-sandbox", "tmux", "restore", form}))
		}
	})

	It("CS-TMUX-068: tmux restore --pin and --rearm never build: a stale binary is exit 0, silent, and not run", func() {
		src := filepath.Join(repo, "cmd", "claude-sandbox", "main.go")
		Expect(os.MkdirAll(filepath.Dir(src), 0o755)).To(Succeed())
		Expect(os.WriteFile(src, []byte("package main\n"), 0o644)).To(Succeed())
		future := time.Now().Add(time.Hour)
		Expect(os.Chtimes(src, future, future)).To(Succeed())
		for _, form := range []string{"--pin", "--rearm"} {
			combined, err := shimRaw("tmux", "restore", form)
			Expect(err).NotTo(HaveOccurred(), form)
			Expect(combined).To(BeEmpty(), form)
			Expect(out).NotTo(BeAnExistingFile(), form)
		}
		Expect(filepath.Join(repo, "bin", "dist", "gocache")).NotTo(BeADirectory())
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
// `go` (or `docker`) that sleeps, counts its builds, records its -o target and
// writes a script there in place, as `go build -o` does when it copies across
// filesystems; that script records whether fd 9 (the lock) reached it. The
// shim must hand the build a temporary name and rename it over the binary, or
// a concurrent shim execs a file still being written (ETXTBSY, or an empty
// script and no output: the flake this guards against).
var _ = Describe("shim build lock (spec/tmux.feature)", func() {
	var (
		repo    string
		binDir  string
		pathDir string
		count   string
		ran     string
		outs    string
	)

	tools := []string{"bash", "sh", "readlink", "dirname", "find", "mkdir", "sleep", "chmod", "id", "cat", "mv", "rm"}

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
		outs = filepath.Join(scratch, "outs")
	})

	// built is the body of the "binary" the fakes write: it records one line
	// per run ("leaked" when fd 9 is open in it) and writes one marker line to
	// each of its stdout and stderr, so a test can see that both reach the
	// caller unchanged.
	built := func() string {
		return "#!/bin/sh\nif [ -e /proc/$$/fd/9 ]; then echo leaked >> '" + ran + "'; else echo ok >> '" + ran + "'; fi\n" +
			"echo launcher-stdout\necho launcher-stderr >&2\n"
	}
	// The fakes print a line to stdout as a real build may (a first docker
	// pull's progress); the shim must send it to stderr.
	const buildNoise = "build-tool-stdout"
	// writeOut is the fakes' common tail: record the -o target, then write the
	// binary there in place (truncate, then write, as `go build -o` copies) and
	// make it executable.
	writeOut := func() string {
		return "echo \"$out\" >> '" + outs + "'\ncat > \"$out\" <<'BIN'\n" + built() + "BIN\nchmod +x \"$out\"\n"
	}
	// fakeGo writes a `go` that takes 1 s and then writes the -o target.
	fakeGo := func() {
		script := "#!/bin/sh\necho build >> '" + count + "'\necho " + buildNoise + "\nsleep 1\nout=\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=$2; shift; done\n" + writeOut()
		Expect(os.WriteFile(filepath.Join(pathDir, "go"), []byte(script), 0o755)).To(Succeed())
	}
	// fakeDocker writes a `docker` that maps "-v <repo>:/src" and writes the
	// -o target (relative to the container's /src) the same way.
	fakeDocker := func() {
		script := "#!/bin/sh\necho build >> '" + count + "'\necho " + buildNoise + "\nsleep 1\nsrc=\no=\nwhile [ $# -gt 0 ]; do [ \"$1\" = -v ] && src=${2%:/src}; [ \"$1\" = -o ] && o=$2; shift; done\n" +
			"out=\"$src/$o\"\n" + writeOut()
		Expect(os.WriteFile(filepath.Join(pathDir, "docker"), []byte(script), 0o755)).To(Succeed())
	}
	// renamedIn checks that every build wrote a temporary file in bin/dist/,
	// never bin/dist/claude-sandbox itself, and that none is left behind: the
	// shim renamed each over the binary.
	renamedIn := func() {
		b, err := os.ReadFile(outs)
		Expect(err).NotTo(HaveOccurred())
		targets := strings.Fields(string(b))
		Expect(targets).NotTo(BeEmpty())
		for _, t := range targets {
			Expect(filepath.Dir(t)).To(Equal(binDir), "the temporary file sits beside the binary (one filesystem, so the rename is atomic)")
			Expect(filepath.Base(t)).NotTo(Equal("claude-sandbox"), "the build never writes the binary in place")
			Expect(t).NotTo(BeAnExistingFile(), "renamed over the binary")
		}
		Expect(filepath.Join(binDir, "claude-sandbox")).To(BeAnExistingFile())
	}
	withFlock := func() {
		p, err := exec.LookPath("flock")
		if err != nil {
			Skip("flock (util-linux) is not installed")
		}
		Expect(os.Symlink(p, filepath.Join(pathDir, "flock"))).To(Succeed())
	}
	// holdLock takes the build lock from the test process; the returned func
	// releases it.
	holdLock := func() func() {
		Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
		f, err := os.OpenFile(filepath.Join(binDir, ".build.lock"), os.O_RDWR|os.O_CREATE, 0o600)
		Expect(err).NotTo(HaveOccurred())
		Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
		return func() { _ = f.Close() }
	}
	type result struct {
		stdout, stderr string
		err            error
	}
	// run runs the shim with stdout and stderr captured apart.
	run := func(extraEnv []string, args ...string) result {
		bash := filepath.Join(pathDir, "bash")
		cmd := exec.Command(bash, append([]string{filepath.Join(repo, "bin", "claude-sandbox")}, args...)...)
		cmd.Env = append([]string{"PATH=" + pathDir, "HOME=" + repo}, extraEnv...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return result{stdout.String(), stderr.String(), err}
	}
	race := func(n int) []result {
		res := make([]result, n)
		done := make(chan int, n)
		for i := 0; i < n; i++ {
			go func(i int) {
				res[i] = run(nil, "--new")
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
	// launched checks one stale run: it succeeded, its stdout is the
	// launcher's alone, and its stderr still reaches the caller after the
	// shim's lock handling (a stray redirection on the fd close once sent it
	// to /dev/null for every stale launch).
	launched := func(r result) {
		Expect(r.err).NotTo(HaveOccurred(), r.stderr)
		Expect(r.stdout).To(Equal("launcher-stdout\n"), "build output and shim messages never reach stdout")
		Expect(r.stderr).To(HaveSuffix("launcher-stderr\n"), "the exec'd launcher keeps the caller's stderr")
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
			// The test holds the lock for the first second, so the shims queue
			// behind it; released, one builds and the others find the binary
			// up to date under the lock. A shim the scheduler starts only
			// after the build finds it fresh and neither waits nor builds,
			// which is also correct, so only "at least one waited" is fixed.
			release := holdLock()
			defer release()
			timer := time.AfterFunc(time.Second, release)
			defer timer.Stop()
			res := race(3)
			building, waiting := 0, 0
			for _, r := range res {
				launched(r)
				if strings.Contains(r.stderr, "Building claude-sandbox binary") {
					building++
					Expect(r.stderr).To(ContainSubstring(buildNoise), "build output goes to stderr")
				}
				if strings.Contains(r.stderr, "Waiting for another claude-sandbox build") {
					waiting++
				}
			}
			Expect(lines(count)).To(HaveLen(1), "one build for three launches")
			Expect(building).To(Equal(1))
			Expect(waiting).To(BeNumerically(">=", 1))
			Expect(lines(ran)).To(Equal([]string{"ok", "ok", "ok"}), "every launch execs the binary, and fd 9 never reaches it")
			renamedIn()
		})
	}

	It("CS-TMUX-073: past the bounded wait a waiter warns and builds without the lock", func() {
		withFlock()
		fakeGo()
		release := holdLock()
		defer release()
		r := run([]string{"CLAUDE_SANDBOX_BUILD_LOCK_WAIT=1"}, "--new")
		launched(r)
		Expect(r.stderr).To(ContainSubstring("Waiting for another claude-sandbox build"))
		Expect(r.stderr).To(ContainSubstring("WARNING: the other claude-sandbox build still holds " + filepath.Join(binDir, ".build.lock") + " after 1s; building without the lock."))
		Expect(r.stderr).To(ContainSubstring("Building claude-sandbox binary (host go)"))
		Expect(lines(count)).To(HaveLen(1))
		Expect(lines(ran)).To(Equal([]string{"ok"}))
		renamedIn()
	})

	It("CS-TMUX-073: a lock file that cannot be opened means an unlocked build, with no message", func() {
		withFlock()
		fakeGo()
		Expect(os.MkdirAll(filepath.Join(binDir, ".build.lock"), 0o755)).To(Succeed())
		r := run(nil, "--new")
		launched(r)
		Expect(r.stderr).NotTo(ContainSubstring("Waiting"))
		Expect(r.stderr).NotTo(ContainSubstring("WARNING"))
		Expect(r.stderr).To(ContainSubstring("Building claude-sandbox binary (host go)"))
		Expect(lines(count)).To(HaveLen(1))
		Expect(lines(ran)).To(Equal([]string{"ok"}))
		renamedIn()
	})

	It("CS-TMUX-073: without flock the shim builds unlocked, as before, with no message", func() {
		fakeGo()
		res := race(2)
		for _, r := range res {
			launched(r)
			Expect(r.stderr).NotTo(ContainSubstring("Waiting"))
			Expect(r.stderr).NotTo(ContainSubstring("WARNING"))
		}
		Expect(lines(count)).To(HaveLen(2))
		Expect(filepath.Join(binDir, ".build.lock")).NotTo(BeAnExistingFile())
		Expect(lines(ran)).To(Equal([]string{"ok", "ok"}))
		// Two unlocked builds at once: each writes its own temporary file and
		// renames it, so neither shim can exec a file the other is writing.
		renamedIn()
		Expect(lines(outs)).To(HaveLen(2))
		Expect(lines(outs)[0]).NotTo(Equal(lines(outs)[1]))
	})

	It("CS-TMUX-073: a failed build removes its temporary file and leaves the binary as it was", func() {
		// A `go` that writes part of its output and fails, as a build killed
		// mid-copy would.
		script := "#!/bin/sh\necho build >> '" + count + "'\nout=\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=$2; shift; done\n" +
			"echo \"$out\" >> '" + outs + "'\necho '#!/bin/sh' > \"$out\"\nexit 3\n"
		Expect(os.WriteFile(filepath.Join(pathDir, "go"), []byte(script), 0o755)).To(Succeed())
		r := run(nil, "--new")
		var ee *exec.ExitError
		Expect(errors.As(r.err, &ee)).To(BeTrue(), r.stderr)
		Expect(ee.ExitCode()).To(Equal(3), "the build's own status")
		Expect(r.stdout).To(BeEmpty())
		Expect(lines(outs)).To(HaveLen(1))
		Expect(lines(outs)[0]).NotTo(BeAnExistingFile(), "the temporary file is removed")
		Expect(filepath.Join(binDir, "claude-sandbox")).NotTo(BeAnExistingFile(), "nothing half-written takes the binary's place")
		Expect(ran).NotTo(BeAnExistingFile())
	})

	It("CS-TMUX-073: an up-to-date binary takes no lock and builds nothing", func() {
		withFlock()
		fakeGo()
		Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(binDir, "claude-sandbox"), []byte(built()), 0o755)).To(Succeed())
		r := run(nil, "--new")
		Expect(r.err).NotTo(HaveOccurred(), r.stderr)
		Expect(r.stdout).To(Equal("launcher-stdout\n"))
		Expect(r.stderr).To(Equal("launcher-stderr\n"))
		Expect(count).NotTo(BeAnExistingFile())
		Expect(filepath.Join(binDir, ".build.lock")).NotTo(BeAnExistingFile())
		Expect(lines(ran)).To(Equal([]string{"ok"}))
	})

	It("CS-TMUX-068: the tmux restore --pin/--rearm fast path never waits on a held build lock; --resurrected builds", func() {
		withFlock()
		fakeGo()
		Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(binDir, "claude-sandbox"), []byte(built()), 0o755)).To(Succeed())
		future := time.Now().Add(time.Hour)
		Expect(os.Chtimes(filepath.Join(repo, "cmd", "claude-sandbox", "main.go"), future, future)).To(Succeed())
		release := holdLock()
		for _, form := range []string{"--pin", "--rearm"} {
			start := time.Now()
			r := run(nil, "tmux", "restore", form)
			Expect(r.err).NotTo(HaveOccurred(), form)
			Expect(r.stdout+r.stderr).To(BeEmpty(), form)
			Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second), form)
		}
		Expect(count).NotTo(BeAnExistingFile())
		Expect(ran).NotTo(BeAnExistingFile())
		release()
		// Typed into a pane's shell, --resurrected is no hook: it builds.
		r := run(nil, "tmux", "restore", "--resurrected")
		launched(r)
		Expect(lines(count)).To(HaveLen(1))
	})

	It("CS-TMUX-073: the tmux save fast path never waits on a held build lock", func() {
		withFlock()
		fakeGo()
		Expect(os.MkdirAll(binDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(binDir, "claude-sandbox"), []byte(built()), 0o755)).To(Succeed())
		release := holdLock()
		defer release()
		// Stale too: the hook neither builds nor runs it, and never queues.
		future := time.Now().Add(time.Hour)
		Expect(os.Chtimes(filepath.Join(repo, "cmd", "claude-sandbox", "main.go"), future, future)).To(Succeed())
		start := time.Now()
		r := run(nil, "tmux", "save", "/r/tmux_resurrect_x.txt")
		Expect(r.err).NotTo(HaveOccurred())
		Expect(r.stdout + r.stderr).To(BeEmpty())
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
		Expect(count).NotTo(BeAnExistingFile())
		Expect(ran).NotTo(BeAnExistingFile())
	})
})
