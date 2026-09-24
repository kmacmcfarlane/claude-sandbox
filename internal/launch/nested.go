package launch

// Nested launches (CS-LNCH-163..165). A launcher inside a sandbox resolves
// paths in its own container, but docker resolves every bind source on the
// HOST. A path the outer sandbox did not bind in is container-local, and
// handing it to docker makes the daemon bind an empty, root-owned host path
// of the same name. Each path the launcher mounts from a resolved location
// (rather than a fixed one the outer sandbox is known to mount) is checked
// here first.

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// nestedBindSourceOK reports whether path may be handed to docker as a bind
// source. Outside a sandbox it is always true and mountinfo is never read.
// Inside one it is true only when the path is demonstrably host-visible
// (CS-LNCH-163): /proc/self/mountinfo is readable and the mount holding the
// path (symlink-resolved when it resolves; CS-LNCH-162's longest-mount-point
// rule) is neither the container's root filesystem nor a tmpfs — a bind the
// outer sandbox made. An unreadable mountinfo is no evidence. why says what
// was found, for the warning.
func (in *Inputs) nestedBindSourceOK(path string) (ok bool, why string) {
	if !hostdirs.InSandbox(in.getenv) {
		return true, ""
	}
	mountinfo := in.MountInfo
	if mountinfo == nil {
		if testing.Testing() {
			panic("launch: a test resolved the real /proc/self/mountinfo; pass a fake mountinfo (Inputs.MountInfo)")
		}
		mountinfo = readMountInfo
	}
	text, err := mountinfo()
	if err != nil {
		return false, fmt.Sprintf("/proc/self/mountinfo is unreadable: %v", err)
	}
	p := filepath.Clean(path)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	mp, fstype, found := coveringMount(text, p)
	switch {
	case !found:
		return false, "no mount holds it"
	case mp == "/":
		return false, "it is on the container's own root filesystem"
	case fstype == "tmpfs":
		return false, "it is on a tmpfs mounted at " + mp + " inside the container"
	}
	return true, ""
}
