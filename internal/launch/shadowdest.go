package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// shadowMount is one single-file shadow bind: a generated file in the shadow
// directory mounted read-only over dest (CS-LNCH-010, 013, 017).
type shadowMount struct {
	spec, dest string
	// name is the shadow file's digest name (tempFile), dropped from the
	// fingerprint inputs when the mount is left out (CS-LNCH-170).
	name string
	// lost says what the session starts without when the mount is left out.
	lost string
}

// placeholderMode is the mode of a mount-point placeholder the launcher
// creates (CS-LNCH-169). The file is empty and exists only so docker has a
// mount point; the container sees the shadow over it. On the host it becomes
// the user's own (empty) CLAUDE.md, .mcp.json or gitconfig — private user
// config — so the house rule's owner-only mode (hostdirs.OwnedDirMode for
// directories) applies; copying a sibling's mode would buy nothing and could
// widen a file that is later filled with secrets (an .mcp.json with tokens).
const placeholderMode = 0o600

// placeholderDirMode is the mode of a missing directory between the covering
// mount and the placeholder, as hostdirs.OwnedDirMode.
const placeholderDirMode = 0o700

func (in *Inputs) addShadowMount(p *Plan, tmp, dest, name, lost string) {
	spec := fmt.Sprintf("%s:%s:ro", tmp, dest)
	p.Volumes = append(p.Volumes, spec)
	in.shadowMounts = append(in.shadowMounts, shadowMount{spec: spec, dest: dest, name: name, lost: lost})
}

// prepareShadowDests runs once every mount of the launch is known
// (CS-LNCH-169..171). A shadow destination that does not exist, under a
// read-write same-path mount, would be created by runc THROUGH that bind — an
// empty root-owned file on the host the user cannot remove. It is created
// here as the invoking user instead; when that is not possible the shadow
// mount is left out with one warning, so docker never creates it.
//
// Drift (CS-LNCH-170): the fingerprint follows the applied mount set, and the
// left-out file's content digest goes with it. The drift check builds the same
// plan (sessions_cmd.wouldBeFingerprint → Build), so it creates the same
// placeholder, or leaves out the same mount, and agrees with the launch.
func (in *Inputs) prepareShadowDests(p *Plan) {
	for _, sm := range in.shadowMounts {
		cover, ok := deepestMountOf(p.Volumes, sm.dest)
		if !ok || !isSamePathRW(cover) {
			continue // CS-LNCH-169: nothing to protect
		}
		coverRoot := filepath.Clean(strings.Split(cover, ":")[1])
		err := in.ensureShadowDest(sm.dest, coverRoot)
		if err == nil {
			continue
		}
		p.Volumes = removeString(p.Volumes, sm.spec)
		in.dropShadowDigest(sm.name)
		fmt.Fprintf(in.Err, "WARNING: %s is not shadowed in this session: it does not exist on the host and lies under the read-write mount %s, and it cannot be created as you (%v), so docker would create it there as root; the session starts without %s.\n", sm.dest, cover, err, sm.lost)
	}
}

// ensureShadowDest makes dest exist as the invoking user, below coverRoot
// (CS-LNCH-169). An existing dest of any kind is left as it is: docker makes
// no mount point then.
func (in *Inputs) ensureShadowDest(dest, coverRoot string) error {
	if _, err := os.Lstat(dest); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(dest)
	if testing.Testing() && (isRealHome(parent) || isRealHome(filepath.Dir(parent))) {
		panic(fmt.Sprintf("launch: a test would create %s under the real home; set HOME (Inputs.Home) and CLAUDE_CONFIG_DIR to scratch directories", dest))
	}
	// CS-LNCH-171: nested, a file made here reaches the host only through a
	// bind the outer sandbox made (the CS-LNCH-163 rule).
	if vis, why := in.nestedBindSourceOK(parent); !vis {
		return fmt.Errorf("this launcher runs inside a sandbox that does not mount %s at the same path: %s", parent, why)
	}
	if err := mkPlaceholderParents(coverRoot, parent); err != nil {
		return err
	}
	d, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	fi, err := d.Stat()
	d.Close()
	if err != nil {
		return err
	}
	getuid := os.Getuid
	if in.Getuid != nil {
		getuid = in.Getuid
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if uid := getuid(); int(st.Uid) != uid {
			return fmt.Errorf("%s is owned by uid %d, not by the invoking user (uid %d)", parent, st.Uid, uid)
		}
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, placeholderMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Appeared meanwhile (a concurrent launch): it exists, which is
			// all docker needs.
			if _, lerr := os.Lstat(dest); lerr == nil {
				return nil
			}
		}
		return err
	}
	return f.Close()
}

// mkPlaceholderParents creates the missing directories from coverRoot down to
// parent, 0700, refusing a symlink or a non-directory on the way. The cover
// root itself is the mount's source and is not checked here: docker resolves
// it on the host as it always has.
func mkPlaceholderParents(coverRoot, parent string) error {
	rel, err := filepath.Rel(coverRoot, parent)
	if err != nil || rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is not under %s", parent, coverRoot)
	}
	cur := coverRoot
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(cur, placeholderDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if fi, err = os.Lstat(cur); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink", cur)
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	return nil
}

// deepestMountOf returns the -v spec whose container side is the DEEPEST
// strict ancestor of path — the mount runc creates path's mount point in.
// A spec at path itself (the shadow bind) is not a cover.
func deepestMountOf(volumes []string, path string) (string, bool) {
	path = filepath.Clean(path)
	best, bestLen := "", -1
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			continue
		}
		dst := filepath.Clean(parts[1])
		if dst != "/" && !strings.HasPrefix(path, dst+"/") {
			continue
		}
		if dst == "/" && path == "/" {
			continue
		}
		if len(dst) > bestLen {
			best, bestLen = v, len(dst)
		}
	}
	return best, bestLen >= 0
}

// isSamePathRW reports whether a -v spec binds a path at itself read-write.
func isSamePathRW(spec string) bool {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || filepath.Clean(parts[0]) != filepath.Clean(parts[1]) {
		return false
	}
	if len(parts) >= 3 {
		for _, o := range strings.Split(parts[2], ",") {
			if o == "ro" {
				return false
			}
		}
	}
	return true
}

func removeString(xs []string, x string) []string {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func (in *Inputs) dropShadowDigest(name string) {
	out := in.shadowDigests[:0]
	for _, d := range in.shadowDigests {
		if d.Path != name {
			out = append(out, d)
		}
	}
	in.shadowDigests = out
}
