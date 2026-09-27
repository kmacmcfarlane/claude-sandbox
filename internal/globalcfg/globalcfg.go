// Package globalcfg decides how Claude Code's global config file
// ($HOME/.claude.json in the default layout) reaches a sandbox, and makes the
// in-container link of the linked layout.
// Spec: spec/global-config.feature (CS-GCFG).
//
// The legacy layout bind-mounts $HOME/.claude.json into every container as a
// single file. Claude Code's lock ($HOME/.claude.json.lock) is then private to
// each container's overlay, and its temp-file rename onto the mount point
// fails, so it writes in place (ftruncate + write): concurrent sandboxes tear
// the file and a 0-byte read can write defaults over it. The LINKED layout
// keeps the real file in the config dir, which every sandbox mounts read-write
// at the same path, and makes $HOME/.claude.json a one-level symlink to it on
// the host and in every container. Claude Code writes through the link with
// allowSymlink, staging the temp file beside the target and renaming it
// there — atomically.
//
// Classify is the host-side decision (the launcher); EnsureLink is the
// in-container half (the pidslot helper). The health check and the
// migrate/revert commands build on this package (Classify's Link, Target and
// Resolved are the paths they need).
package globalcfg

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// EnvVar carries the link target T into the container. Set by the
	// launcher only in the linked layout; an env flag, never hashed.
	EnvVar = "CLAUDE_SANDBOX_GLOBAL_CONFIG"
	// ExitLink is pidslot's status when the link cannot be made
	// (EX_CONFIG, CS-GCFG-036). The launcher's own codes are 2, 3 and 4.
	ExitLink = 78
	// LinkPrefix starts pidslot's one failure line, so the launcher's host
	// message can point at it (CS-GCFG-038).
	LinkPrefix = "claude-sandbox: global config link:"

	// FileName is the global config file's base name.
	FileName = ".claude.json"
	// LegacyConfigName is Claude Code's older global file inside the config
	// dir; when it exists Claude Code uses it instead (CS-GCFG-029).
	LegacyConfigName = ".config.json"
	// configDirName is the default config dir under $HOME.
	configDirName = ".claude"
	// LinkText is the relative link text the linked layout uses on the host.
	LinkText = configDirName + "/" + FileName
)

// Mode is the layout the launcher found on the host.
type Mode int

const (
	// ModeRelocated: CLAUDE_CONFIG_DIR is set. Claude Code reads
	// $CLAUDE_CONFIG_DIR/.claude.json inside the config-dir mount; this
	// feature does nothing (CS-GCFG-027).
	ModeRelocated Mode = iota
	// ModeMissing: nothing at $HOME/.claude.json.
	ModeMissing
	// ModeLegacy: $HOME/.claude.json is a regular file (CS-GCFG-025).
	ModeLegacy
	// ModeLinked: a one-level symlink whose lexical target is
	// $HOME/.claude/.claude.json, a regular file (CS-GCFG-016).
	ModeLinked
	// ModeRefused: any other symlink state, or something that is neither a
	// file nor a symlink (CS-GCFG-019..024). Nothing is mounted.
	ModeRefused
	// ModeConfigJSON: $HOME/.claude/.config.json exists and wins
	// (CS-GCFG-029). Legacy behaviour for .claude.json.
	ModeConfigJSON
)

// Layout is what Classify found.
type Layout struct {
	Mode Mode
	// Link is $HOME/.claude.json (L); Target is the file the linked layout
	// keeps, $HOME/.claude/.claude.json (T), lexically.
	Link, Target string
	// LinkText is the raw symlink text when Link is a symlink; Resolved is
	// the lexical path it names (absolute-aware Join, then Clean).
	LinkText, Resolved string
	// Problem says why a ModeRefused layout was refused.
	Problem string
	// Regular is true when Link Lstats as a regular file: the only case in
	// which it may be single-file-mounted (ModeLegacy, ModeConfigJSON).
	Regular bool
	// SplitBrain is true when Link is a regular file while Target also
	// exists (CS-GCFG-026).
	SplitBrain bool
}

// ConfigDir is the default layout's config dir, $HOME/.claude.
func ConfigDir(home string) string { return filepath.Join(home, configDirName) }

// resolveLink is the lexical target a symlink's text names from dir:
// the text itself when absolute (filepath.Join does not reset on an absolute
// second argument), else joined onto dir; then Clean (CS-GCFG-017).
func resolveLink(dir, text string) string {
	if !filepath.IsAbs(text) {
		text = filepath.Join(dir, text)
	}
	return filepath.Clean(text)
}

// Classify decides the default layout's mode from the host filesystem
// (CS-GCFG-016..029). configDirEnv is the launcher's CLAUDE_CONFIG_DIR; any
// non-empty value is ModeRelocated. Nothing is resolved with EvalSymlinks:
// Claude Code reads the link once and writes beside what it names.
func Classify(home, configDirEnv string) Layout {
	cfgDir := ConfigDir(home)
	l := Layout{
		Link:   filepath.Join(filepath.Clean(home), FileName),
		Target: filepath.Join(cfgDir, FileName),
	}
	if configDirEnv != "" {
		l.Mode = ModeRelocated
		return l
	}
	fi, err := os.Lstat(l.Link)
	if err == nil {
		l.Regular = fi.Mode().IsRegular()
	}
	// CS-GCFG-029: Claude Code prefers <configDir>/.config.json.
	if _, cerr := os.Lstat(filepath.Join(cfgDir, LegacyConfigName)); cerr == nil {
		l.Mode = ModeConfigJSON
		return l
	}
	switch {
	case err != nil:
		l.Mode = ModeMissing
	case l.Regular:
		l.Mode = ModeLegacy
		if _, terr := os.Lstat(l.Target); terr == nil {
			l.SplitBrain = true
		}
	case fi.Mode()&os.ModeSymlink != 0:
		l.classifyLink(cfgDir)
	default:
		l.Mode = ModeRefused
		l.Problem = fmt.Sprintf("%s is neither a regular file nor a symlink (%s)", l.Link, fi.Mode().Type())
	}
	return l
}

// classifyLink applies the strict lexical rule to a symlinked Link.
func (l *Layout) classifyLink(cfgDir string) {
	l.Mode = ModeRefused
	text, err := os.Readlink(l.Link)
	if err != nil {
		l.Problem = fmt.Sprintf("its link cannot be read (%v)", err)
		return
	}
	l.LinkText = text
	l.Resolved = resolveLink(filepath.Dir(l.Link), text)
	if l.Resolved != l.Target {
		// CS-GCFG-019/020: a chain ends here too — its first hop is not T.
		l.Problem = fmt.Sprintf("it must name %s directly (one level, no chain)", l.Target)
		return
	}
	// CS-GCFG-023: the container mounts the config dir at its lexical path.
	if ci, err := os.Lstat(cfgDir); err == nil && ci.Mode()&os.ModeSymlink != 0 {
		l.Problem = fmt.Sprintf("the config dir %s is itself a symlink, so the file the link reaches is not the one the sandbox mounts", cfgDir)
		return
	}
	ti, err := os.Lstat(l.Resolved)
	switch {
	case errors.Is(err, os.ErrNotExist):
		l.Problem = "the target does not exist (a dangling link)" // CS-GCFG-021
	case err != nil:
		l.Problem = fmt.Sprintf("the target cannot be read (%v)", err)
	case !ti.Mode().IsRegular():
		l.Problem = fmt.Sprintf("the target is not a regular file (%s)", describeType(ti.Mode())) // CS-GCFG-022
	default:
		l.Mode = ModeLinked
	}
}

func describeType(m os.FileMode) string {
	switch {
	case m.IsDir():
		return "a directory"
	case m&os.ModeSymlink != 0:
		return "a symlink"
	}
	return m.Type().String()
}

// SuspiciousConfigDir reports whether a CLAUDE_CONFIG_DIR value is relative
// or holds a path element starting with "~" (CS-GCFG-028): Claude Code
// resolves a relative one against its cwd and takes "~" literally.
func SuspiciousConfigDir(v string) bool {
	if v == "" {
		return false
	}
	if !filepath.IsAbs(v) {
		return true
	}
	for _, e := range strings.Split(v, "/") {
		if strings.HasPrefix(e, "~") {
			return true
		}
	}
	return false
}

// ConfigDirWarning is the CS-GCFG-028 warning.
func ConfigDirWarning(v string) string {
	return fmt.Sprintf("WARNING: CLAUDE_CONFIG_DIR=%q is not a plain absolute path: Claude Code resolves a relative one against its working directory and takes \"~\" literally, so the session's config may not be the directory the sandbox mounts. Use an absolute path.\n", v)
}

// RefusedWarning is the one CS-GCFG-019..024 warning.
func (l Layout) RefusedWarning() string {
	what := l.Link
	if l.LinkText != "" {
		what = fmt.Sprintf("%s (a symlink to %s)", l.Link, l.LinkText)
	}
	return fmt.Sprintf("WARNING: %s is not mounted: %s. The linked layout needs %s to be a symlink straight to %s, a regular file; anything else is never single-file-mounted, so this session starts without the global config (Claude Code's first-run path). See README \"Global config (~/.claude.json)\".\n",
		what, l.Problem, l.Link, l.Target)
}

// LinkCmd and UnlinkCmd are the manual steps the split-brain warning and the
// exit-78 message give (CS-GCFG-026/038), to run with every Claude session
// exited. A later feature adds checked commands (locking, verification).
const (
	//
	// Both are GUARDED: they do nothing unless the layout is the one they
	// expect. Unguarded, the undo step deletes the only config when the
	// operator already reverted by hand, and the link step run on an existing
	// (dangling) link moves the link into ~/.claude/ and builds a chain, or
	// overwrites an existing target. No "mv -n": its exit status differs
	// across coreutils versions.
	LinkCmd = "test -f ~/.claude.json && ! test -L ~/.claude.json && ! test -e ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json" +
		" && mv ~/.claude.json ~/.claude/.claude.json && ln -s .claude/.claude.json ~/.claude.json"
	UnlinkCmd = "test -L ~/.claude.json && test -f ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json" +
		" && rm ~/.claude.json && mv ~/.claude/.claude.json ~/.claude.json"
	// keepCopy is said before either step.
	keepCopy = "copy ~/.claude.json somewhere outside ~/.claude/ first"
)

// SiblingLinkNote is the CS-GCFG-027 note: with CLAUDE_CONFIG_DIR set, a
// symlinked <parent>/.claude.json is not mounted.
func SiblingLinkNote(path string) string {
	return fmt.Sprintf("Note: %s is a symlink; it is not mounted (a symlink is never single-file-mounted, and with CLAUDE_CONFIG_DIR set Claude Code reads $CLAUDE_CONFIG_DIR/.claude.json).\n", path)
}

// ConfigJSONNote is the CS-GCFG-029 note.
func (l Layout) ConfigJSONNote() string {
	return fmt.Sprintf("Note: %s exists, so Claude Code uses it as the global config; %s stays in the legacy layout.\n",
		filepath.Join(filepath.Dir(l.Target), LegacyConfigName), l.Link)
}

// SplitBrainWarning is the CS-GCFG-026 warning.
func (l Layout) SplitBrainWarning() string {
	mtime := func(p string) string {
		if fi, err := os.Stat(p); err == nil {
			return fi.ModTime().UTC().Format(time.RFC3339)
		}
		return "unknown"
	}
	return fmt.Sprintf("WARNING: two global config files: %s (a regular file, modified %s) and %s (modified %s). With CLAUDE_CONFIG_DIR unset Claude Code uses %s; the other is stale — typically a tool replaced the link by rename. With every Claude session exited (host and sandboxes), %s; merge what you need into %s by hand, remove the stale %s, and then either keep the legacy layout or restore the link (the step does nothing unless the layout is as expected): %s. A later claude-sandbox release adds checked commands for this.\n",
		l.Link, mtime(l.Link), l.Target, mtime(l.Target), l.Link, keepCopy, l.Link, l.Target, LinkCmd)
}

// ExitMessage is the launcher's host-side explanation of a session that
// exited ExitLink (CS-GCFG-038..040). container names the container for the
// kept-container fix. It says "likely": the status alone is the evidence.
func ExitMessage(container string) string {
	if container == "" {
		container = "<container>"
	}
	return fmt.Sprintf("The session exited with %d, likely the global-config link check (see the %q line above).\n"+
		"  Fix, with every Claude session exited (host and sandboxes) — %s. Each step does nothing\n"+
		"  unless the layout is the one it expects. Make ~/.claude.json a symlink to .claude/.claude.json:\n"+
		"    %s\n"+
		"  or undo the link:\n"+
		"    %s\n"+
		"  then relaunch.\n"+
		"  A kept container whose link target moved or vanished fails on every start: docker rm %s, then relaunch.\n"+
		"  A later claude-sandbox release adds checked commands for this.\n",
		ExitLink, LinkPrefix, keepCopy, LinkCmd, UnlinkCmd, container)
}

// LinkOps are EnsureLink's filesystem seams. A nil *LinkOps, or a nil field,
// means the real call.
type LinkOps struct {
	Lstat    func(string) (os.FileInfo, error)
	Readlink func(string) (string, error)
	Symlink  func(oldname, newname string) error
	Rename   func(oldpath, newpath string) error
	Link     func(oldname, newname string) error
	Remove   func(string) error
	Now      func() time.Time
}

func (o *LinkOps) fill() *LinkOps {
	r := LinkOps{}
	if o != nil {
		r = *o
	}
	if r.Lstat == nil {
		r.Lstat = os.Lstat
	}
	if r.Readlink == nil {
		r.Readlink = os.Readlink
	}
	if r.Symlink == nil {
		r.Symlink = os.Symlink
	}
	if r.Rename == nil {
		r.Rename = os.Rename
	}
	if r.Link == nil {
		r.Link = os.Link
	}
	if r.Remove == nil {
		r.Remove = os.Remove
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	return &r
}

// EnsureLink makes home/.claude.json a symlink whose text is target
// (CS-GCFG-033..037). It is the pidslot helper's first step when EnvVar is
// set. Every failure is returned; the caller refuses to exec (exit ExitLink).
// A replaced regular file is kept at <link>.replaced-<ms>-<pid>-<rand>,
// named on warn. Under go test it panics for the real home.
func EnsureLink(home, target string, warn io.Writer, o *LinkOps) error {
	ops := o.fill()
	if home == "" {
		return errors.New("HOME is not set")
	}
	if testing.Testing() && isRealHome(home) {
		// The CS-LNCH-138 precedent: a forgotten fixture must fail loudly
		// before anything is made under the operator's real home.
		panic(fmt.Sprintf("globalcfg: a test would link %s under the real home; use a scratch HOME", filepath.Join(home, FileName)))
	}
	// CS-GCFG-036: the only target the linked layout ever names. Anything
	// else (an env-file value, a stale container) is refused.
	if want := filepath.Join(filepath.Clean(home), configDirName, FileName); filepath.Clean(target) != want {
		return fmt.Errorf("%s=%q: must be %s", EnvVar, target, want)
	}
	target = filepath.Clean(target)
	ti, err := ops.Lstat(target)
	if err != nil {
		return fmt.Errorf("%s: %w", target, unwrapPath(err))
	}
	if !ti.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file (%s)", target, describeType(ti.Mode()))
	}
	link := filepath.Join(home, FileName)
	fi, err := ops.Lstat(link)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Absent: create it.
	case err != nil:
		return fmt.Errorf("%s: %w", link, unwrapPath(err))
	case fi.Mode()&os.ModeSymlink != 0:
		if text, rerr := ops.Readlink(link); rerr == nil && text == target {
			return nil // CS-GCFG-033: already right.
		}
		// CS-GCFG-034: a wrong link is replaced by the swap below.
	case fi.Mode().IsRegular():
		// CS-GCFG-035: keep the bytes, with no moment the path is missing.
		aside := fmt.Sprintf("%s.replaced-%d-%d-%s", link, ops.Now().UnixMilli(), os.Getpid(), randHex())
		if lerr := ops.Link(link, aside); lerr != nil {
			// A racing helper may have swapped the link in meanwhile.
			if fi2, err2 := ops.Lstat(link); err2 == nil && fi2.Mode()&os.ModeSymlink != 0 {
				if text, rerr := ops.Readlink(link); rerr == nil && text == target {
					return nil
				}
			}
			if cerr := copyFileSync(link, aside); cerr != nil {
				return fmt.Errorf("%s: moving the existing file aside to %s: %w", link, aside, cerr)
			}
		}
		fmt.Fprintf(warn, "Warning: %s was a regular file (an image leftover); it is kept at %s and replaced by a link to %s.\n", link, aside, target)
	default:
		return fmt.Errorf("%s: neither a regular file nor a symlink (%s)", link, describeType(fi.Mode()))
	}
	return swapLink(ops, link, target)
}

// swapLink stages a uniquely named symlink beside link and renames it over
// link: atomic, and racing callers each rename their own (CS-GCFG-037).
func swapLink(ops *LinkOps, link, target string) error {
	tmp := fmt.Sprintf("%s.link-%d-%s", link, os.Getpid(), randHex())
	if err := ops.Symlink(target, tmp); err != nil {
		return fmt.Errorf("%s: %w", tmp, unwrapPath(err))
	}
	if err := ops.Rename(tmp, link); err != nil {
		ops.Remove(tmp)
		return fmt.Errorf("%s: %w", link, unwrapPath(err))
	}
	return nil
}

// randHex is 8 random hex digits for unique sibling names.
func randHex() string {
	var b [4]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// isRealHome reports whether home is the invoking user's actual home, by
// $HOME or by the user database (HOME can be unset under env -i).
func isRealHome(home string) bool {
	var reals []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		reals = append(reals, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		reals = append(reals, u.HomeDir)
	}
	for _, r := range reals {
		a, errA := filepath.EvalSymlinks(home)
		b, errB := filepath.EvalSymlinks(r)
		if errA != nil || errB != nil {
			a, b = filepath.Clean(home), filepath.Clean(r)
		}
		if a == b {
			return true
		}
	}
	return false
}

// unwrapPath drops the *PathError wrapper, whose path the caller names.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// copyFileSync copies src to a new dst (O_EXCL, 0600) and fsyncs it.
func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
