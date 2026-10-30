package sessions

// Friendly session names (CS-SESS-114..117): the name Claude Code shows in its
// terminal title, read from the container's primary claude's peer-registry
// record, for display beside the instance noun. Display only: anything that
// cannot be read is no name, silently.

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// NameDisplayMax is the most runes of a session name any listing shows; a
// longer one is cut and ends in an ellipsis (CS-SESS-117).
const NameDisplayMax = 40

// NameLookup says where a container's records are looked for, and whose
// records are never a container's.
type NameLookup struct {
	// Dirs returns the registry directories of a container from its
	// claude-sandbox.registry label: the label's directory when set, else —
	// a container from before the label, and so from before the peers move —
	// the launcher's <config dir>/sessions and the legacy peers root's
	// sessions (resumeguard.RegistryDirs, which this package cannot import).
	// nil reads the label's directory alone.
	Dirs func(registryLabel string) []string
	// OwnDomain returns the launcher's own pid domain ("" when unknown): a
	// record from it is a claude on the host that shares the directory, never
	// the container's. Called at most once, and only when a record is read;
	// nil = unknown.
	OwnDomain func() string
}

// SessionNames maps container name to the cleaned name of the container's
// primary claude, for every session in found that has one. The primary's
// record is the one at the container's pid class (pid % 256) whose startedAt
// is at or after the container's creation (to the second) — the earliest
// such record, since the primary starts before any join. Each registry
// directory is listed once, and only the records at a class some session
// there watches are read. A directory or record that cannot be read gives no
// name (CS-SESS-114/117).
func SessionNames(found []Session, lk NameLookup) map[string]string {
	type want struct {
		s     Session
		class int
		dirs  []string
	}
	var wants []want
	classes := map[string]map[int]bool{} // dir -> watched classes
	for _, s := range found {
		class, err := strconv.Atoi(s.PIDClass)
		if err != nil || class < 0 || class > 255 || s.CreatedAt.IsZero() || s.Reserved() {
			continue
		}
		var dirs []string
		switch {
		case lk.Dirs != nil:
			dirs = lk.Dirs(s.RegistryDir)
		case s.RegistryDir != "":
			dirs = []string{s.RegistryDir}
		}
		w := want{s: s, class: class}
		for _, d := range dirs {
			if d == "" {
				continue
			}
			d = filepath.Clean(d)
			w.dirs = append(w.dirs, d)
			if classes[d] == nil {
				classes[d] = map[int]bool{}
			}
			classes[d][class] = true
		}
		wants = append(wants, w)
	}
	if len(wants) == 0 {
		return nil
	}
	records := map[string][]registry.Record{}
	for d, cls := range classes {
		records[d] = readClasses(d, cls)
	}
	domain := ""
	if lk.OwnDomain != nil {
		for _, rs := range records {
			if len(rs) > 0 {
				domain = lk.OwnDomain()
				break
			}
		}
	}
	out := map[string]string{}
	for _, w := range wants {
		var recs []registry.Record
		for _, d := range w.dirs {
			recs = append(recs, records[d]...)
		}
		if best, ok := PickPrimary(recs, w.class, w.s.CreatedAt, domain); ok && best.Name != "" {
			out[w.s.Name] = best.Name
		}
	}
	return out
}

// PickPrimary is CS-SESS-114's rule, shared by the friendly names and the
// restart (CS-SESS-106): of recs, the records at class (pid % 256) whose
// startedAt is at or after created (truncated to the second) and whose
// pidDomain is not ownDomain ("" = unknown, skip nothing), the earliest, ties
// to the lower pid. The primary claude starts before any join. Pure: no I/O,
// and a caller decides what a failed read means.
func PickPrimary(recs []registry.Record, class int, created time.Time, ownDomain string) (registry.Record, bool) {
	since := created.Truncate(time.Second).UnixMilli()
	var best *registry.Record
	for i := range recs {
		r := &recs[i]
		if r.PID%256 != class || r.StartedAt < since {
			continue
		}
		if ownDomain != "" && r.PIDDomain == ownDomain {
			continue
		}
		if best == nil || r.StartedAt < best.StartedAt || (r.StartedAt == best.StartedAt && r.PID < best.PID) {
			best = r
		}
	}
	if best == nil {
		return registry.Record{}, false
	}
	return *best, true
}

// readClasses reads the records at the given classes in one registry
// directory. Every failure is silent: a missing or unreadable directory, a
// malformed or vanished record.
func readClasses(dir string, classes map[int]bool) []registry.Record {
	guardRealHome(dir)
	d, err := registry.OpenDir(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	es, err := d.Entries()
	if err != nil {
		return nil
	}
	var out []registry.Record
	for _, e := range es {
		if !classes[e.PID%256] {
			continue
		}
		if r, err := d.Read(e); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// DisplayName is a session name as a listing shows it: printable (it is
// sandbox-writable text) and at most NameDisplayMax runes, a longer one cut
// to NameDisplayMax-1 runes plus "…".
func DisplayName(name string) string {
	name = registry.Printable(name)
	if utf8.RuneCountInString(name) <= NameDisplayMax {
		return name
	}
	r := []rune(name)
	return strings.TrimRight(string(r[:NameDisplayMax-1]), " ") + "…"
}

// guardRealHome panics under go test when a registry directory is one of the
// invoking user's real ones (its ~/.claude, the peers root or the legacy
// peers root, each by $HOME and by the user database): a test must never read
// the real registry.
func guardRealHome(dir string) {
	if !testing.Testing() {
		return
	}
	var homes []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		homes = append(homes, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		homes = append(homes, u.HomeDir)
	}
	for _, h := range homes {
		for _, rel := range []string{".claude", hostdirs.PeersRootRel, hostdirs.LegacyPeersRootRel} {
			if samePath(dir, filepath.Join(h, rel, "sessions")) {
				panic("sessions: a test would read the real registry " + dir + "; point HOME and the registry label at a scratch directory")
			}
		}
	}
}

// samePath compares two paths lexically and, where both resolve, by their
// symlink-resolved forms.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
