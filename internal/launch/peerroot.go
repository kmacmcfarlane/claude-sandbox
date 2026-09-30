package launch

// The shared peer registry's root and its drain-then-switch move out of the
// cache root (spec/host-dirs.feature, CS-DIR-010..019; operator decision 34).
//
// The registry is state: live records and listening sockets whose absolute
// paths every bridged session advertises. It cannot move under running
// sessions — their binds hold the old inode and their records advertise the
// old path — so a launch keeps the legacy root while any container on the
// host mounts it, and takes the new one at the first launch that finds none.
// The choice is made under the launch lock from the discovery that already
// runs there (reserve.go), and handed to Build as Inputs.PeerRoot.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// LabelPeerRoot records the peers root a container's bridge applied, or
// PeerRootNone when the bridge was off or stood down (CS-DIR-012). docker
// renders an absent label and an empty one alike, so "none" is what tells a
// new unbridged container from one that predates the label. Read by the drift
// check (CS-DIR-017); never hashed, like every label.
const LabelPeerRoot = "claude-sandbox.peerroot"

// PeerRootNone is LabelPeerRoot's value for a container without the bridge.
const PeerRootNone = "none"

// PeerRootChoice is the root a launch's bridge uses and why.
type PeerRootChoice struct {
	// Root is the chosen peers root (absolute).
	Root string
	// Legacy is this user's legacy root; New the new one. Both absolute.
	Legacy, New string
	// Pinned counts the listed containers with a bind source at or under
	// Legacy (CS-DIR-011).
	Pinned int
	// DiscoveryFailed: the listing failed, and Root was chosen from whether
	// Legacy exists (CS-DIR-014/015).
	DiscoveryFailed bool
	// Keep: the launch is marked kept and took New despite the pin
	// (CS-DIR-016). Nothing sets it yet: no launch is marked kept.
	Keep bool
}

// OnLegacy reports whether the choice is the legacy root.
func (c PeerRootChoice) OnLegacy() bool { return c.Root == c.Legacy }

// PinsLegacyRoot reports whether a container with these bind sources pins
// home's legacy peers root: a source that, path-cleaned, equals the root or
// lies under it (the peers/sessions source counts). Sources are the split
// {{.Mounts}} of docker ps --no-trunc. Another user's legacy root never
// matches (CS-DIR-013).
func PinsLegacyRoot(home string, sources []string) bool {
	legacy := filepath.Clean(hostdirs.LegacyPeersRoot(home))
	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if c := filepath.Clean(s); c == legacy || strings.HasPrefix(c, legacy+"/") {
			return true
		}
	}
	return false
}

// ChoosePeerRoot picks the peers root for a launch (CS-DIR-010..016).
// mounts holds the bind sources of EVERY row the launch's discovery returned,
// exited ones docker is still removing included; discoveryErr is that
// discovery's error. keep marks a kept launch (CS-DIR-016; no caller sets it
// yet).
//
// A failed discovery fails closed (CS-DIR-014): the legacy root when it is a
// real directory — an empty list would read as "nothing pins it" and split
// the registry under live legacy sessions — else the new root (CS-DIR-015).
func ChoosePeerRoot(home string, mounts [][]string, discoveryErr error, keep bool) PeerRootChoice {
	c := PeerRootChoice{Legacy: hostdirs.LegacyPeersRoot(home), New: hostdirs.PeersRoot(home)}
	if discoveryErr != nil {
		c.DiscoveryFailed = true
		c.Root = c.New
		if fi, err := os.Lstat(c.Legacy); err == nil && fi.IsDir() {
			c.Root = c.Legacy
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			// Cannot tell: keep the old location, the side that cannot
			// split a registry that may still be in use.
			c.Root = c.Legacy
		}
		return c
	}
	for _, m := range mounts {
		if PinsLegacyRoot(home, m) {
			c.Pinned++
		}
	}
	c.Root = c.New
	if c.Pinned > 0 {
		if keep {
			c.Keep = true
		} else {
			c.Root = c.Legacy
		}
	}
	return c
}

// SplitMounts splits docker ps's {{.Mounts}} (comma-joined, --no-trunc) into
// its sources. A source holding "," splits wrongly, which can only make a
// legacy match fail (CS-DIR-011: accepted, bounded by the next drain).
func SplitMounts(field string) []string {
	var out []string
	for _, s := range strings.Split(field, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DriftPeerRoot is the root the attach/join drift check's would-be plan uses
// for a container (CS-DIR-017/018): the path its peerroot label names; for a
// container without the label, the legacy root when one of its bind sources
// lies under it; otherwise the new root. "none" means "no root to reuse",
// never "the bridge is off" — whether the bridge applies stays the config's,
// so CS-LNCH-052's drift still shows.
func DriftPeerRoot(home, label string, mounts []string) string {
	switch {
	case label != "" && label != PeerRootNone && filepath.IsAbs(label):
		return filepath.Clean(label)
	case label == "" && PinsLegacyRoot(home, mounts):
		return hostdirs.LegacyPeersRoot(home)
	}
	return hostdirs.PeersRoot(home)
}
