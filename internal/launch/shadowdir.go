package launch

// Shadow directory lifecycle (CS-LNCH-080..084). Each launch writes its shadow
// files into one fresh directory under the temp root and bind-mounts them. The
// launcher ends by exec'ing "docker start", so it can never remove its own
// directory once the session is over; a later launch sweeps it instead, once
// no container on the host still uses it.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// ShadowDirPrefix is the os.MkdirTemp pattern of a shadow directory. MkdirTemp
// appends decimal digits, which shadowDirName checks for exactly.
const ShadowDirPrefix = "claude-sandbox"

// LabelShadowDir names the container's shadow directory (CS-LNCH-080). Like
// every other label it is outside the config hash, which is computed from the
// resolved inputs, never from the argv.
const LabelShadowDir = "claude-sandbox.shadowdir"

// ShadowDirMinAge is how long a directory is left alone after it was last
// written. A launch holding the lock never sees another launch's directory
// before its create (the directory is made under the lock), but a launch that
// could not take the lock, or a launcher from before this change (which made
// the directory before waiting up to 30 s for the lock), can be anywhere
// between its MkdirTemp and its create. An hour is orders of magnitude more
// than that window; the cost is at most an hour of directories of a few KB.
const ShadowDirMinAge = time.Hour

// shadowDirName is exactly what os.MkdirTemp(root, ShadowDirPrefix) produces.
// Anything else under the temp root — "claude-sandbox-foo", another tool's
// directory — is never touched.
var shadowDirName = regexp.MustCompile(`^` + regexp.QuoteMeta(ShadowDirPrefix) + `[0-9]+$`)

// NewShadowDir makes a fresh shadow directory under root ("" = os.TempDir()).
func NewShadowDir(root string) (string, error) {
	return os.MkdirTemp(shadowRoot(root), ShadowDirPrefix)
}

// shadowRoot resolves the temp root shadow directories are made in and swept
// from ("" = os.TempDir()). Under go test it refuses the real temp root with a
// panic, which fails the test: a test that forgets to point Env.TempRoot (or
// Inputs.TempDir) at a scratch directory would otherwise sweep the real temp
// root against a faked, empty docker ps — deleting the shadow directories of
// live sessions on the machine running the tests. A fixture mistake must fail
// loudly, not quietly delete. Outside go test this is exactly the old default.
func shadowRoot(root string) string {
	if root == "" {
		root = os.TempDir()
	}
	if testing.Testing() && isSystemTempDir(root) {
		panic(fmt.Sprintf("launch: a test made or swept shadow directories in the real temp root %s; "+
			"set Env.TempRoot (or Inputs.TempDir) to a scratch directory such as GinkgoT().TempDir()", root))
	}
	return root
}

// NestedShadowSubdir is the directory under CLAUDE_CODE_TMPDIR that a
// launcher inside a sandbox makes its shadow directories in (CS-LNCH-161).
// It does not match shadowDirName, so no sweep ever considers it itself.
const NestedShadowSubdir = "claude-sandbox-shadow"

// ErrNoHostVisibleTempRoot marks a launcher inside a sandbox that found no
// temp root the host docker daemon can see (CS-LNCH-162).
var ErrNoHostVisibleTempRoot = errors.New("no host-visible temp root")

// NestedShadowRoot resolves the shadow root of a launcher running inside a
// sandbox (CS-LNCH-161/162); home is the invoking user's home. The shadow
// files are bind-mounted, and a bind source resolves on the HOST: the
// container's own /tmp would give docker an empty, root-owned host path of
// the same name. Outside a sandbox it returns "" (the temp root, unchanged).
//
// Order: a non-empty TMPDIR is returned as is — setting it is the operator's
// statement that it is host-visible, and nothing inside the container can
// check that. Otherwise CLAUDE_CODE_TMPDIR, when it is absolute and under the
// config dir: the outer sandbox mounts the config dir at its real path
// (CS-LNCH-008) and derives CLAUDE_CODE_TMPDIR under it (CS-LNCH-034), so a
// path there is the same path on the host. The root is
// <CLAUDE_CODE_TMPDIR>/claude-sandbox-shadow, made a 0700 directory the user
// owns. Anything else is ErrNoHostVisibleTempRoot, wrapped with the reason.
//
// An explicit TMPDIR is checked only where the answer is certain
// (CS-LNCH-162): a relative one is refused, and so is one whose mount (the
// longest mount point in mountinfo covering it) is the container's root
// filesystem or a tmpfs — definitely container-local. Any other mount keeps
// the trust-the-operator rule. mountinfo is the seam for /proc/self/mountinfo
// (nil = read it); an unreadable one is no evidence, and TMPDIR is trusted.
func NestedShadowRoot(getenv func(string) string, home string, ops *hostdirs.Ops, mountinfo func() (string, error)) (string, error) {
	if !hostdirs.InSandbox(getenv) {
		return "", nil
	}
	if t := getenv("TMPDIR"); t != "" {
		if !filepath.IsAbs(t) {
			return "", fmt.Errorf("%w: TMPDIR=%s is not an absolute path", ErrNoHostVisibleTempRoot, t)
		}
		if mountinfo == nil {
			if testing.Testing() {
				panic("launch: a test resolved the real /proc/self/mountinfo; pass a fake mountinfo (Env.MountInfo)")
			}
			mountinfo = readMountInfo
		}
		if text, err := mountinfo(); err == nil {
			resolved := filepath.Clean(t)
			if r, err := filepath.EvalSymlinks(resolved); err == nil {
				resolved = r
			}
			if mp, fstype, ok := coveringMount(text, resolved); ok && (mp == "/" || fstype == "tmpfs") {
				what := "the container's own root filesystem"
				if mp != "/" {
					what = "a tmpfs mounted at " + mp + " inside the container"
				}
				return "", fmt.Errorf("%w: TMPDIR=%s is on %s", ErrNoHostVisibleTempRoot, t, what)
			}
		}
		return t, nil
	}
	configDir := getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" && home != "" {
		configDir = filepath.Join(home, ".claude")
	}
	cct := getenv("CLAUDE_CODE_TMPDIR")
	switch {
	case cct == "":
		return "", fmt.Errorf("%w: CLAUDE_CODE_TMPDIR is not set", ErrNoHostVisibleTempRoot)
	case !filepath.IsAbs(cct) || !filepath.IsAbs(configDir):
		return "", fmt.Errorf("%w: CLAUDE_CODE_TMPDIR=%s is not an absolute path under the config dir %q", ErrNoHostVisibleTempRoot, cct, configDir)
	}
	cct, cfg := filepath.Clean(cct), filepath.Clean(configDir)
	if !underDir(cct, cfg) && !underDirResolved(cct, cfg) {
		return "", fmt.Errorf("%w: CLAUDE_CODE_TMPDIR=%s is not under the config dir %s, the one directory the outer sandbox is known to mount at its real path", ErrNoHostVisibleTempRoot, cct, cfg)
	}
	root := filepath.Join(cct, NestedShadowSubdir)
	if err := hostdirs.EnsureOwnedDir(root, hostdirs.OwnedDirMode, ops); err != nil {
		return "", fmt.Errorf("%w: cannot prepare %s: %v", ErrNoHostVisibleTempRoot, root, err)
	}
	return root, nil
}

// underDir reports whether path is dir or lies below it (both clean).
func underDir(path, dir string) bool {
	return path == dir || dir == "/" || strings.HasPrefix(path, dir+"/")
}

// underDirResolved is underDir on the symlink-resolved paths: a config dir
// reached through a symlink (or a CLAUDE_CODE_TMPDIR spelled through one)
// is still the same directory. False when either cannot be resolved.
func underDirResolved(path, dir string) bool {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return underDir(p, d)
}

func readMountInfo() (string, error) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	return string(b), err
}

// coveringMount returns the mount point and filesystem type of the mount
// that holds path, per mountinfo text (proc(5)): the longest mount point that
// is path or a prefix of it by whole components; among equal ones the later
// line, which is mounted over the earlier.
func coveringMount(text, path string) (mountPoint, fstype string, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mp := filepath.Clean(unescapeMountInfo(fields[4]))
		sep := slices.Index(fields, "-")
		if sep < 0 || sep+1 >= len(fields) {
			continue
		}
		if !underDir(path, mp) {
			continue
		}
		if !ok || len(mp) >= len(mountPoint) {
			mountPoint, fstype, ok = mp, fields[sep+1], true
		}
	}
	return mountPoint, fstype, ok
}

// unescapeMountInfo decodes the kernel's octal escapes (\040 space, \011
// tab, \012 newline, \134 backslash) in a mountinfo field.
func unescapeMountInfo(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// isSystemTempDir reports whether dir is os.TempDir(), however it is spelled
// (a trailing slash, a symlink).
func isSystemTempDir(dir string) bool {
	sys := os.TempDir()
	if filepath.Clean(dir) == filepath.Clean(sys) {
		return true
	}
	a, errA := os.Stat(dir)
	b, errB := os.Stat(sys)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// PruneShadowDirs removes the shadow directories under root that no container
// can still be using (CS-LNCH-081): named like a shadow directory, a real
// directory (never a symlink), owned by uid, unmodified for longer than
// minAge, not keep, and neither named by any container's shadowdir label nor
// the parent of any container's bind-mount source. Containers are listed in
// every state and regardless of labels, so an exited container kept without
// --rm, or one from a launcher that predates the label, still protects its
// directory. It returns the directories removed.
//
// The docker listing runs only when there is a candidate (CS-LNCH-082). If it
// fails nothing is removed: without it, "unused" cannot be known.
func PruneShadowDirs(r execx.Runner, root string, uid int, now time.Time, minAge time.Duration, keep string) ([]string, error) {
	return NewShadowSweep(r).Prune(root, uid, now, minAge, keep)
}

// ShadowSweep sweeps several roots with ONE container listing (CS-LNCH-166):
// the listing runs lazily, the first time any root has a candidate, and is
// reused for every later root — a headless probe must not pay for a docker ps
// per root.
type ShadowSweep struct {
	r      execx.Runner
	inUse  map[string]bool
	listed bool
	err    error
}

// NewShadowSweep returns a sweep that lists containers through r.
func NewShadowSweep(r execx.Runner) *ShadowSweep { return &ShadowSweep{r: r} }

// Prune is PruneShadowDirs for one root, sharing the sweep's listing. When the
// listing failed it removes nothing; the error is returned only by the Prune
// that ran it, so a failed listing warns once however many roots follow.
func (s *ShadowSweep) Prune(root string, uid int, now time.Time, minAge time.Duration, keep string) ([]string, error) {
	root = shadowRoot(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}
	var candidates []string
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if !shadowDirName.MatchString(e.Name()) || filepath.Clean(dir) == filepath.Clean(keep) {
			continue
		}
		if stale(dir, uid, now, minAge) {
			candidates = append(candidates, dir)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if !s.listed {
		s.listed = true
		s.inUse, s.err = shadowDirsInUse(s.r)
		if s.err != nil {
			return nil, s.err
		}
	}
	if s.err != nil {
		return nil, nil
	}
	var removed []string
	var errs []error
	for _, dir := range candidates {
		if s.inUse[filepath.Base(dir)] {
			continue
		}
		// Re-check right before removing: the listing took time.
		if !stale(dir, uid, now, minAge) {
			continue
		}
		// RemoveAll unlinks symlinks inside the directory, never follows them.
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, setAside(dir, err, now, minAge))
			continue
		}
		removed = append(removed, dir)
	}
	return removed, errors.Join(errs...)
}

// UnremovableSuffix is appended to a shadow directory whose removal failed
// (CS-LNCH-082). The result no longer matches shadowDirName, so no later sweep
// considers it and its warning is printed once, not on every launch.
const UnremovableSuffix = ".unremovable"

// setAside keeps a directory that could not be removed from warning on every
// later launch, and returns the error to report for it. It is renamed in place
// to <dir>.unremovable (never retried: whatever made a file inside it
// unremovable — another owner, a read-only subdirectory — does not go away by
// itself, and the warning says to remove it by hand). If even the rename
// fails, the directory's mtime is set to now, so stale() skips it for minAge
// and it is retried, and warned about, at most once per minAge.
func setAside(dir string, rmErr error, now time.Time, minAge time.Duration) error {
	aside := dir + UnremovableSuffix
	if err := os.Rename(dir, aside); err == nil {
		return fmt.Errorf("%w (renamed it %s so later launches skip it; remove it by hand)", rmErr, aside)
	}
	if err := os.Chtimes(dir, now, now); err == nil {
		return fmt.Errorf("%w (retrying in %s)", rmErr, minAge)
	}
	return rmErr
}

// stale reports whether dir is a real directory owned by uid and last modified
// more than minAge before now. Lstat: a symlink is not a directory here.
func stale(dir string, uid int, now time.Time, minAge time.Duration) bool {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.Mode().IsDir() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != uid {
		return false
	}
	return now.Sub(fi.ModTime()) > minAge
}

// psShadowFormat lists a container's shadowdir label and its mount sources.
// {{.Mounts}} with --no-trunc is the comma-joined list of bind sources (volume
// names for volumes). The separator cannot occur in either field.
const psShadowFormat = `{{.Label "` + LabelShadowDir + `"}}` + "\x1f" + `{{.Mounts}}`

// shadowDirsInUse returns the base names of the shadow directories any
// container on the host names or mounts from. Base names, not paths: the same
// directory can be spelled through a different temp root (a symlinked TMPDIR),
// and a name collision between two random MkdirTemp suffixes only ever keeps a
// directory, never removes one.
func shadowDirsInUse(r execx.Runner) (map[string]bool, error) {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"ps", "-a", "--no-trunc", "--format", psShadowFormat},
		Stderr: io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	inUse := map[string]bool{}
	add := func(p string) {
		if b := filepath.Base(p); shadowDirName.MatchString(b) {
			inUse[b] = true
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		label, mounts, _ := strings.Cut(line, "\x1f")
		if label = strings.TrimSpace(label); label != "" {
			add(label)
		}
		// A shadow file is <root>/claude-sandbox<digits>/<name>, so its parent
		// names the directory. Splitting on commas is safe for that parent even
		// if the temp root itself contains a comma: the last two components of
		// the source survive the split.
		for _, m := range strings.Split(mounts, ",") {
			if m = strings.TrimSpace(m); m != "" {
				add(m)
				add(filepath.Dir(m))
			}
		}
	}
	return inUse, nil
}
