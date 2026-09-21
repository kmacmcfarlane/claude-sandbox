package imagebuild

// The Claude Code update check (CS-IMG-006..009, CS-IMG-044..047). It never
// blocks a launch: the registry answer is cached for VersionCacheTTL, and a
// newer version is built in the background by a detached
// "claude-sandbox cli-prefetch <version>" for the NEXT launch, which picks it
// up through the cap's fingerprint (the cap hashes the CLI image ID). Only
// --update checks and builds in the foreground, as before.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

const (
	// VersionCacheTTL is how long a registry answer is trusted (CS-IMG-044)
	// and how long a failed background build of one version is not retried
	// (CS-IMG-047).
	VersionCacheTTL = 6 * time.Hour

	// PrefetchSubcommand is the hidden subcommand the background build runs.
	PrefetchSubcommand = "cli-prefetch"

	// PrefetchTag is where the background build puts its image until it
	// moves CLIImageName onto it (CS-IMG-045).
	PrefetchTag = CLIImageName + ":prefetch"

	// Files under Options.CacheDir.
	VersionCacheFile   = "claude-version.json"
	PrefetchLockFile   = "cli-prefetch.lock"
	PrefetchLogFile    = "cli-prefetch.log"
	PrefetchStatusFile = "cli-prefetch.json"
)

// errPrefetchBusy reports that a background CLI build holds the lock.
var errPrefetchBusy = errors.New("a background CLI build is already running")

// exactVersionRe is what cli-prefetch accepts as its pin.
var exactVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// versionCache is the registry answer and when it was obtained (CS-IMG-044).
type versionCache struct {
	Latest  string    `json:"latest"`
	Checked time.Time `json:"checked"`
}

// prefetchStatus is the outcome of the last background build (CS-IMG-047).
type prefetchStatus struct {
	Version  string    `json:"version"`
	OK       bool      `json:"ok"`
	Finished time.Time `json:"finished"`
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) cacheFile(name string) string { return filepath.Join(o.CacheDir, name) }

// PrefetchLogPath is the background build's log under cacheDir.
func PrefetchLogPath(cacheDir string) string { return filepath.Join(cacheDir, PrefetchLogFile) }

// within reports whether t is no more than VersionCacheTTL before now. A time
// in the future (a clock stepped back) does not count as fresh.
func within(now, t time.Time) bool {
	age := now.Sub(t)
	return age >= 0 && age < VersionCacheTTL
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// writeJSON replaces path atomically, so a concurrent reader sees the old
// file or the new one, never half of one.
func writeJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(raw, '\n'))
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// cachedLatestVersion is the registry's latest release through the version
// cache (CS-IMG-044). fresh skips the cache read (--update); a version the
// registry does report is always written back. The error is a cache write
// failure, for the caller's single warning; the version is still good.
func cachedLatestVersion(o Options, fresh bool) (string, error) {
	if o.CacheDir == "" {
		return latestClaudeVersion(o), nil
	}
	path := o.cacheFile(VersionCacheFile)
	if !fresh {
		var c versionCache
		// Anchored: a corrupt value such as "1.2.4 junk" would otherwise be
		// printed and handed to a background build that rejects it, on every
		// launch until the TTL ran out.
		if readJSON(path, &c) == nil && exactVersionRe.MatchString(c.Latest) && within(o.now(), c.Checked) {
			return c.Latest, nil
		}
	}
	v := latestClaudeVersion(o)
	if v == "" {
		return "", nil // not cached: the next launch asks again
	}
	return v, writeJSON(path, versionCache{Latest: v, Checked: o.now()})
}

// newer reports whether version a is strictly newer than b, comparing
// X.Y.Z numerically. Anything that is not an exact X.Y.Z is never newer, and
// nothing is newer than a b that cannot be read.
func newer(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	if !exactVersionRe.MatchString(v) {
		return out, false
	}
	for i, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// UpdateCheck compares the CLI image's pinned Claude Code version with the
// registry's (CS-IMG-006, CS-IMG-044). A newer version is built in the
// background for the next launch (CS-IMG-045..047) — never prompted for and
// never waited on (CS-IMG-008) — except with --update, which checks and
// builds now (CS-IMG-009). cliBuilt skips the check: a fresh CLI image is
// already current. Returns whether the CLI image was rebuilt HERE, which only
// --update does.
func UpdateCheck(o Options, cliBuilt bool) bool {
	if o.NoUpdateCheck || cliBuilt {
		return false
	}
	pinned := pinnedClaudeVersion(o)
	if pinned == "" {
		return false
	}
	if o.AutoUpdate {
		return foregroundUpdate(o, pinned)
	}

	// CS-IMG-047: the last background build failed for a version the image
	// has not reached. Only a NEWER one counts: an image already past it
	// (built by --update, --rebuild or a later background build) makes the
	// record moot. One line, every launch, until then.
	var st prefetchStatus
	failed := false
	if o.CacheDir != "" && readJSON(o.cacheFile(PrefetchStatusFile), &st) == nil {
		failed = !st.OK && newer(st.Version, pinned)
	}
	logPath := PrefetchLogPath(o.CacheDir)
	if failed {
		fmt.Fprintf(o.Err, "WARNING: the last background build of Claude Code %s failed; see %s (claude-sandbox --update retries it now).\n",
			st.Version, logPath)
	}

	latest, cacheErr := cachedLatestVersion(o, false)
	if !newer(latest, pinned) {
		// Same version, unreadable answer, or a registry behind the image:
		// nothing to build, and never a downgrade.
		if cacheErr != nil {
			// CS-IMG-047: advisory; the version itself is good.
			fmt.Fprintf(o.Err, "WARNING: could not write the Claude Code version cache (%v); the next launch asks the registry again.\n", cacheErr)
		}
		return false
	}
	if cacheErr != nil {
		// CS-IMG-047: the directory the lock and the log live in is unusable
		// too, so one line covers both and nothing is started.
		fmt.Fprintf(o.Err, "WARNING: Claude Code update available: %s → %s, but the version cache could not be written (%v), so no background build starts; claude-sandbox --update builds it now.\n",
			pinned, latest, cacheErr)
		return false
	}
	if failed && st.Version == latest && within(o.now(), st.Finished) {
		// CS-IMG-047: not retried in the background until the TTL has
		// passed; the warning above already named the version and the log.
		return false
	}
	if o.CacheDir == "" {
		fmt.Fprintf(o.Out, "Claude Code update available: %s → %s (claude-sandbox --update builds it).\n", pinned, latest)
		return false
	}
	switch err := startPrefetch(o, latest); {
	case errors.Is(err, errPrefetchBusy):
		fmt.Fprintf(o.Out, "Claude Code update available: %s → %s; a background build is already running (log: %s).\n",
			pinned, latest, logPath)
	case err != nil:
		fmt.Fprintf(o.Err, "WARNING: Claude Code update available: %s → %s, but the background build could not start (%v); claude-sandbox --update builds it now.\n",
			pinned, latest, err)
	default:
		fmt.Fprintf(o.Out, "Claude Code update available: %s → %s; building it in the background for the next launch (log: %s).\n",
			pinned, latest, logPath)
	}
	return false
}

// foregroundUpdate is --update (CS-IMG-009): ask the registry now and build
// the CLI image now, pinned to its answer. The pin is a build arg, so the
// install layer busts on its own; no --no-cache needed, and the base and
// children are never touched. A success is recorded like a background one,
// so a stale failure record cannot outlive it (CS-IMG-047).
func foregroundUpdate(o Options, pinned string) bool {
	latest, cacheErr := cachedLatestVersion(o, true)
	if cacheErr != nil {
		fmt.Fprintf(o.Err, "WARNING: could not write the Claude Code version cache (%v).\n", cacheErr)
	}
	if !newer(latest, pinned) {
		return false
	}
	fmt.Fprintf(o.Out, "\nClaude Code update available: %s → %s\n", pinned, latest)
	err := buildCLI(o, latest, false)
	fmt.Fprintln(o.Out)
	if err == nil && o.CacheDir != "" {
		if rerr := recordPrefetch(o, latest, true); rerr != nil {
			fmt.Fprintf(o.Err, "WARNING: could not record the Claude Code update in %s (%v).\n", o.cacheFile(PrefetchStatusFile), rerr)
		}
	}
	return err == nil
}

// tryPrefetchLock takes an exclusive flock on path without waiting. busy reports a
// lock held elsewhere; release is nil unless the lock was taken. The fd is
// close-on-exec (os.OpenFile always sets it), so a lock taken by the
// launcher is never inherited by the processes it starts.
func tryPrefetchLock(path string) (release func(), busy bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, false, nil
}

// startPrefetch starts the detached background build of version unless one
// is running (CS-IMG-045/046). The probe only avoids a pointless spawn: the
// child takes the lock itself, so two launches racing past the probe still
// build once.
//
// The child goes through Runner.Start with Cmd.Detach — its own session, no
// parent-death signal, reaped in the background — so it outlives the
// launcher and its terminal. stdin stays nil (/dev/null); stdout and stderr
// are the log, opened for append and handed over as a real fd, so nothing in
// the launcher has to drain a pipe.
func startPrefetch(o Options, version string) error {
	release, busy, err := tryPrefetchLock(o.cacheFile(PrefetchLockFile))
	if err != nil {
		return err
	}
	if busy {
		return errPrefetchBusy
	}
	release()
	if o.Self == "" {
		return errors.New("the launcher's own path is unknown")
	}
	log, err := os.OpenFile(PrefetchLogPath(o.CacheDir), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close() // the child holds its own copy once started
	_, err = o.Runner.Start(execx.Cmd{
		Name: o.Self,
		Args: []string{PrefetchSubcommand, "--dir", o.CacheDir, version},
		// The child resolves the same checkout (Dockerfile.cli) as this
		// launch, whatever its own location would suggest.
		Env:    []string{"CLAUDE_SANDBOX_REPO_ROOT=" + o.RepoRoot},
		Stdout: log,
		Stderr: log,
		Detach: true,
	})
	return err
}

// Prefetch is "claude-sandbox cli-prefetch <version>", the detached
// background build (CS-IMG-045..047). Its stdout and stderr are the log. It
// builds ONLY the CLI image, pinned to version, through the same build as a
// launch (CS-IMG-021), under the prefetch lock without waiting for it
// (CS-IMG-046), and records the outcome for the next launch (CS-IMG-047).
//
// It never moves claude-sandbox-cli backwards: it skips unless version is
// newer than the image's pin, builds under PrefetchTag, and checks again
// before "docker tag" moves claude-sandbox-cli — a foreground --update to a
// later version may have finished during the minutes the build took.
func Prefetch(o Options, version string) error {
	if !exactVersionRe.MatchString(version) {
		return fmt.Errorf("%s: want a version X.Y.Z, got %q", PrefetchSubcommand, version)
	}
	if o.CacheDir == "" {
		return fmt.Errorf("%s: no cache directory", PrefetchSubcommand)
	}
	stamp := func() string { return o.now().UTC().Format(time.RFC3339) }
	logf := func(w io.Writer, format string, a ...any) {
		fmt.Fprintf(w, "%s %s %s: %s\n", stamp(), PrefetchSubcommand, version, fmt.Sprintf(format, a...))
	}
	release, busy, err := tryPrefetchLock(o.cacheFile(PrefetchLockFile))
	if err != nil {
		return fmt.Errorf("%s: %w", PrefetchSubcommand, err)
	}
	if busy {
		logf(o.Out, "another background CLI build holds %s; skipping.", o.cacheFile(PrefetchLockFile))
		return nil
	}
	defer release()
	// The log holds the latest build only. The fd is O_APPEND, so writes
	// after the truncation land at the new end.
	_ = os.Truncate(PrefetchLogPath(o.CacheDir), 0)
	logf(o.Out, "started (pid %d).", os.Getpid())

	if pinned := pinnedClaudeVersion(o); pinned != "" && !newer(version, pinned) {
		logf(o.Out, "%s is already at %s; nothing to build.", CLIImageName, pinned)
		return recordPrefetch(o, version, true)
	}
	buildErr := buildCLITagged(o, PrefetchTag, version, false)
	if buildErr == nil {
		if pinned := pinnedClaudeVersion(o); pinned != "" && !newer(version, pinned) {
			logf(o.Out, "%s moved to %s during the build; leaving it there.", CLIImageName, pinned)
			untagPrefetch(o)
			return recordPrefetch(o, version, true)
		}
		buildErr = o.Runner.Run(execx.Cmd{Name: "docker", Args: []string{"tag", PrefetchTag, CLIImageName}, Stdout: o.Out, Stderr: o.Err})
	}
	untagPrefetch(o)
	if err := recordPrefetch(o, version, buildErr == nil); err != nil {
		logf(o.Err, "could not record the outcome: %v", err)
	}
	if buildErr != nil {
		logf(o.Err, "FAILED: %v", buildErr)
		return buildErr
	}
	logf(o.Out, "done; %s is now %s and the next launch uses it.", CLIImageName, version)
	return nil
}

// untagPrefetch removes the PrefetchTag name. Once claude-sandbox-cli points
// at the same image this deletes only the name; otherwise the image goes
// with it. Best effort: a leftover tag is overwritten by the next build.
func untagPrefetch(o Options) {
	_ = o.Runner.Run(execx.Cmd{Name: "docker", Args: []string{"rmi", PrefetchTag}, Stdout: io.Discard, Stderr: io.Discard})
}

func recordPrefetch(o Options, version string, ok bool) error {
	return writeJSON(o.cacheFile(PrefetchStatusFile), prefetchStatus{Version: version, OK: ok, Finished: o.now()})
}
