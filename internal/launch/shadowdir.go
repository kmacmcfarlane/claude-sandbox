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
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
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
	return os.MkdirTemp(root, ShadowDirPrefix)
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
	if root == "" {
		root = os.TempDir()
	}
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
	inUse, err := shadowDirsInUse(r)
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, dir := range candidates {
		if inUse[filepath.Base(dir)] {
			continue
		}
		// Re-check right before removing: the listing took time.
		if !stale(dir, uid, now, minAge) {
			continue
		}
		// RemoveAll unlinks symlinks inside the directory, never follows them.
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, dir)
	}
	return removed, errors.Join(errs...)
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
