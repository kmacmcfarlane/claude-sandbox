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
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

const (
	// VersionCacheTTL is how long a registry answer is trusted (CS-IMG-044)
	// and how long a failed background build of one version is not retried
	// (CS-IMG-047).
	VersionCacheTTL = 6 * time.Hour

	// PrefetchSubcommand is the hidden subcommand the background build runs.
	PrefetchSubcommand = "cli-prefetch"

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
		if readJSON(path, &c) == nil && semverRe.MatchString(c.Latest) && within(o.now(), c.Checked) {
			return c.Latest, nil
		}
	}
	v := latestClaudeVersion(o)
	if v == "" {
		return "", nil // not cached: the next launch asks again
	}
	return v, writeJSON(path, versionCache{Latest: v, Checked: o.now()})
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

	// CS-IMG-047: the last background build failed and the image still is not
	// on that version. One line, every launch, until it is.
	var st prefetchStatus
	failed := false
	if o.CacheDir != "" && readJSON(o.cacheFile(PrefetchStatusFile), &st) == nil {
		failed = !st.OK && st.Version != "" && st.Version != pinned
	}
	logPath := PrefetchLogPath(o.CacheDir)
	if failed {
		fmt.Fprintf(o.Err, "WARNING: the last background build of Claude Code %s failed; see %s (claude-sandbox --update retries it now).\n",
			st.Version, logPath)
	}

	latest, cacheErr := cachedLatestVersion(o, false)
	if latest == "" || latest == pinned {
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
// children are never touched.
func foregroundUpdate(o Options, pinned string) bool {
	latest, cacheErr := cachedLatestVersion(o, true)
	if cacheErr != nil {
		fmt.Fprintf(o.Err, "WARNING: could not write the Claude Code version cache (%v).\n", cacheErr)
	}
	if latest == "" || latest == pinned {
		return false
	}
	fmt.Fprintf(o.Out, "\nClaude Code update available: %s → %s\n", pinned, latest)
	err := buildCLI(o, latest, false)
	fmt.Fprintln(o.Out)
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
	detach := o.Detach
	if detach == nil {
		detach = StartDetached
	}
	return detach(DetachedCmd{
		Path: o.Self,
		Args: []string{PrefetchSubcommand, version},
		// The child resolves the same checkout (Dockerfile.cli) as this
		// launch, whatever its own location would suggest.
		Env: []string{"CLAUDE_SANDBOX_REPO_ROOT=" + o.RepoRoot},
		Log: PrefetchLogPath(o.CacheDir),
	})
}

// Prefetch is "claude-sandbox cli-prefetch <version>", the detached
// background build (CS-IMG-045..047). Its stdout and stderr are the log. It
// builds ONLY the CLI image, pinned to version, through the same build as a
// launch (CS-IMG-021), under the prefetch lock without waiting for it
// (CS-IMG-046), and records the outcome for the next launch (CS-IMG-047).
func Prefetch(o Options, version string) error {
	if !exactVersionRe.MatchString(version) {
		return fmt.Errorf("%s: want a version X.Y.Z, got %q", PrefetchSubcommand, version)
	}
	if o.CacheDir == "" {
		return fmt.Errorf("%s: no cache directory", PrefetchSubcommand)
	}
	stamp := func() string { return o.now().UTC().Format(time.RFC3339) }
	release, busy, err := tryPrefetchLock(o.cacheFile(PrefetchLockFile))
	if err != nil {
		return fmt.Errorf("%s: %w", PrefetchSubcommand, err)
	}
	if busy {
		fmt.Fprintf(o.Out, "%s %s %s: another background CLI build holds %s; skipping.\n",
			stamp(), PrefetchSubcommand, version, o.cacheFile(PrefetchLockFile))
		return nil
	}
	defer release()
	// The log holds the latest build only. The fd is O_APPEND, so writes
	// after the truncation land at the new end.
	_ = os.Truncate(PrefetchLogPath(o.CacheDir), 0)
	fmt.Fprintf(o.Out, "%s %s %s: started (pid %d).\n", stamp(), PrefetchSubcommand, version, os.Getpid())

	if pinnedClaudeVersion(o) == version {
		fmt.Fprintf(o.Out, "%s %s %s: %s is already pinned to %s; nothing to build.\n", stamp(), PrefetchSubcommand, version, CLIImageName, version)
		return recordPrefetch(o, version, true)
	}
	buildErr := buildCLI(o, version, false)
	if err := recordPrefetch(o, version, buildErr == nil); err != nil {
		fmt.Fprintf(o.Err, "%s %s %s: could not record the outcome: %v\n", stamp(), PrefetchSubcommand, version, err)
	}
	if buildErr != nil {
		fmt.Fprintf(o.Err, "%s %s %s: FAILED: %v\n", stamp(), PrefetchSubcommand, version, buildErr)
		return buildErr
	}
	fmt.Fprintf(o.Out, "%s %s %s: done; the next launch uses it.\n", stamp(), PrefetchSubcommand, version)
	return nil
}

func recordPrefetch(o Options, version string, ok bool) error {
	return writeJSON(o.cacheFile(PrefetchStatusFile), prefetchStatus{Version: version, OK: ok, Finished: o.now()})
}
