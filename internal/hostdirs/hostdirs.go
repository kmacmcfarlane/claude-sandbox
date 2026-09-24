// Package hostdirs is the single place that names the launcher's own host
// directories under $HOME, classes them, and holds the one rule for a
// directory the launcher owns (spec/host-dirs.feature, CS-DIR).
//
// Classes: a CACHE may be deleted by any cleaner at any time and is rebuilt
// automatically; STATE cannot be rebuilt from another store. Caches (and the
// locks and one-shot status files beside them) live under CacheRoot; state
// lives under StateRoot, which is never mounted into a container, whole or as
// a parent. internal/paths owns project-level foreign paths only; this
// package owns the per-user ones.
package hostdirs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CacheRootRel is the cache root relative to $HOME. XDG_CACHE_HOME is
// deliberately not honoured (CS-DIR-003): the package-cache mounts and the
// host launch lock derive from this path, and a launcher that moved them
// would lock a different file from one that did not.
const CacheRootRel = ".cache/claude-sandbox"

// StateRootDefaultRel is the state root relative to $HOME when
// XDG_STATE_HOME does not name an absolute directory (CS-DIR-001/002).
const StateRootDefaultRel = ".local/state/claude-sandbox"

// PeersRootRel is the shared peer registry's planned root relative to $HOME.
// A SIBLING of the state root, never inside it, so no container bind is ever
// at or under StateRoot; pinned to $HOME with no env lookup because the same
// path must be computed on the host and in every container. Nothing uses it
// yet: the registry still lives under CacheRoot until the peers move lands.
const PeersRootRel = ".local/state/claude-sandbox-peers"

// OwnedDirMode is the mode for StateRoot and every directory under it.
const OwnedDirMode os.FileMode = 0o700

// CacheRoot is the launcher's cache root for a home directory.
func CacheRoot(home string) string {
	return filepath.Join(home, CacheRootRel)
}

// StateRoot is the launcher's state root: $XDG_STATE_HOME/claude-sandbox
// when XDG_STATE_HOME is set to an absolute path, else
// <home>/.local/state/claude-sandbox. The XDG base-directory spec says a
// relative value is invalid and must be ignored (CS-DIR-002). getenv nil
// reads as "unset".
func StateRoot(home string, getenv func(string) string) string {
	if getenv != nil {
		if x := getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
			return filepath.Join(x, "claude-sandbox")
		}
	}
	return filepath.Join(home, StateRootDefaultRel)
}

// PeersRoot is the planned shared-peer-registry root for a home directory
// (see PeersRootRel).
func PeersRoot(home string) string {
	return filepath.Join(home, PeersRootRel)
}

// InSandbox reports whether the launcher runs inside a claude-sandbox
// container: CLAUDE_SANDBOX_PROJECT_DIR is set in every one of them
// (CS-DIR-006). /.dockerenv is deliberately not consulted — it marks any
// docker container, a CI job's included. There, StateRoot is container-local
// and dies with the container, so state consumers skip or refuse.
func InSandbox(getenv func(string) string) bool {
	return getenv != nil && getenv("CLAUDE_SANDBOX_PROJECT_DIR") != ""
}

// Ops are EnsureOwnedDir's seams. A nil *Ops, or a nil field, means the real
// call.
type Ops struct {
	// Chmod restricts the directory; nil means os.Chmod.
	Chmod func(string, os.FileMode) error
	// Getuid is the invoking user; nil means os.Getuid.
	Getuid func() int
}

// EnsureOwnedDir makes dir a real directory owned by the invoking user with
// exactly mode, creating it (and missing parents) as that user when absent
// and tightening it when it exists wider (CS-DIR-004).
//
// It refuses, before any chmod, a symlink (chmod follows links, and a link's
// target is not the launcher's to re-mode), a non-directory and a directory
// another uid owns (CS-DIR-005). MkdirAll accepts a link to a directory, so
// the Lstat comes after it. The window between Lstat and Chmod is open only
// to someone who can already write the parent.
//
// Error texts are stable: the peer-registry stand-down warning quotes them
// (CS-LNCH-107).
func EnsureOwnedDir(dir string, mode os.FileMode, ops *Ops) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return fmt.Errorf("creating it: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("it is a symlink; the launcher restricts only a real directory")
	}
	if !fi.IsDir() {
		return errors.New("it is not a directory")
	}
	uid := os.Getuid
	chmod := os.Chmod
	if ops != nil {
		if ops.Getuid != nil {
			uid = ops.Getuid
		}
		if ops.Chmod != nil {
			chmod = ops.Chmod
		}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid() {
		return fmt.Errorf("it is owned by uid %d, not by the invoking user (uid %d)", st.Uid, uid())
	}
	if err := chmod(dir, mode); err != nil {
		return fmt.Errorf("restricting it to %#o: %w", mode, err)
	}
	return nil
}
