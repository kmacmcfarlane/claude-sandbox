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
}

// OnLegacy reports whether the choice is the legacy root.
func (c PeerRootChoice) OnLegacy() bool { return c.Root == c.Legacy }

// PinsLegacyRoot reports whether a container with these bind sources pins
// home's legacy peers root: a source that, path-cleaned, equals the root or
// lies under it (the peers/sessions source counts). Sources are the split
// {{.Mounts}} of docker ps --no-trunc. Another user's legacy root never
// matches (CS-DIR-013). The match is lexical; see pinsLegacyRoot for the
// host's same-file fallback.
func PinsLegacyRoot(home string, sources []string) bool {
	return pinsLegacyRoot(home, sources, false)
}

// statPeerSource stats a pin candidate; a seam so a test can prove which
// sources are never stat'ed.
var statPeerSource = os.Stat

// pinsLegacyRoot is PinsLegacyRoot; with sameFile (on the host only — in a
// sandbox a stat would read the container's view) a source that does not
// match lexically still pins when it, or its parent for a sessions/ or
// cc-socks/ source, stats as the same directory as the legacy root: a $HOME
// reached through a symlinked ancestor (CS-DIR-011, the globalcfg
// precedent). Only a candidate that, path-cleaned, ends in
// "/.cache/claude-sandbox/peers" is stat'ed — the legacy root under another
// spelling of a home — so no other source (a cascade mount such as
// /mnt/nas/peers on a hung hard-mounted share) is ever stat'ed: the choice
// runs under the launch lock, and a stat blocked in D state would stall
// every other launch into the unserialized fallback.
func pinsLegacyRoot(home string, sources []string, sameFile bool) bool {
	legacy := filepath.Clean(hostdirs.LegacyPeersRoot(home))
	suffix := "/" + hostdirs.LegacyPeersRootRel
	var legacyFI os.FileInfo
	statted := false
	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		c := filepath.Clean(s)
		if c == legacy || strings.HasPrefix(c, legacy+"/") {
			return true
		}
		if !sameFile {
			continue
		}
		cand := c
		if b := filepath.Base(c); b == peerSessionsDir || b == peerSocketsDir {
			cand = filepath.Dir(c)
		}
		if !strings.HasSuffix(cand, suffix) {
			continue
		}
		if !statted {
			statted = true
			legacyFI, _ = statPeerSource(legacy)
		}
		if legacyFI == nil {
			// No legacy root to compare with: no stat can match, but a later
			// source in this row may still match by path.
			continue
		}
		if fi, err := statPeerSource(cand); err == nil && os.SameFile(fi, legacyFI) {
			return true
		}
	}
	return false
}

// ChoosePeerRoot picks the peers root for a launch (CS-DIR-010..015).
// mounts holds the bind sources of EVERY row the launch's discovery returned,
// exited ones docker is still removing included; discoveryErr is that
// discovery's error. onHost enables the same-file fallback of the pin (CS-DIR-011); a
// launcher inside a sandbox passes false.
//
// A failed discovery (CS-DIR-014/015): the NEW root when it is a real
// directory — it exists only once a launch switched, and the legacy
// directory is never deleted, so its mere existence would send this launch
// back to it and every later launch would pin to it; else the legacy root
// when it is a real directory (fail closed before the switch: an empty list
// would read as "nothing pins it"); else the new root.
func ChoosePeerRoot(home string, mounts [][]string, discoveryErr error, onHost bool) PeerRootChoice {
	c := PeerRootChoice{Legacy: hostdirs.LegacyPeersRoot(home), New: hostdirs.PeersRoot(home)}
	if discoveryErr != nil {
		c.DiscoveryFailed = true
		c.Root = c.New
		if fi, err := os.Lstat(c.New); err == nil && fi.IsDir() {
			return c
		}
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
		if pinsLegacyRoot(home, m, onHost) {
			c.Pinned++
		}
	}
	c.Root = c.New
	if c.Pinned > 0 {
		c.Root = c.Legacy
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
