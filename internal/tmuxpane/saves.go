package tmuxpane

// Reading resurrect's saves for `tmux restore` (CS-TMUX-045..050, plan
// sandbox-reboot-restore 10 § 2/§ 3, 11 § 2.4, 12 § 7): where the saves are,
// which ones exist, which one "--from" names, and whether a save looks sparse
// next to what earlier tmux servers ended with. Everything here only reads.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// The sparse rule's and the listing's constants, in one place
// (CS-TMUX-050). Operator answer 65 (2026-10-01): kept as built, with no
// tuning engineered until a real restore shows they are wrong.
const (
	// SparseLifetimes is how many earlier tmux server lifetimes the
	// baseline takes the final save of.
	SparseLifetimes = 3
	// SparseMinDrop and SparseFraction: a save is sparse when it has at
	// least SparseMinDrop rows fewer AND at least SparseFraction fewer than
	// the baseline.
	SparseMinDrop     = 2
	SparseFractionNum = 1
	SparseFractionDen = 3
	// SparseFallbackSaves is the baseline when no earlier lifetime is
	// known: the saves right before the chosen one.
	SparseFallbackSaves = 5
	// ListDays and ListDaysAll are --list's window (CS-TMUX-048).
	ListDays    = 7
	ListDaysAll = 30
	// ScanMax caps a sidecar scan that stands in for the lifetimes index
	// (11 § 2.4).
	ScanMax = 500
)

// ResurrectDirOption is resurrect's own option naming its dir.
const ResurrectDirOption = "@resurrect-dir"

// DirOptions are ResolveResurrectDir's seams.
type DirOptions struct {
	Runner   execx.Runner
	Home     string
	Getenv   func(string) string
	Hostname func() (string, error)
}

// ResolveResurrectDir finds the resurrect dir as tmux-resurrect cff343c does
// (CS-TMUX-045, scripts/helpers.sh resurrect_dir and variables.sh): the
// @resurrect-dir option with $HOME, $HOSTNAME and ~ expanded (its sed
// replaces every occurrence), else ~/.tmux/resurrect when it exists, else
// ${XDG_DATA_HOME:-~/.local/share}/tmux/resurrect. A tmux that does not
// answer within CallTimeout (no server, outside tmux) skips the option.
func ResolveResurrectDir(o DirOptions) string {
	if out, ok := bounded(o.Runner, CallTimeout, "tmux", "show-option", "-gqv", ResurrectDirOption); ok {
		if v := strings.TrimSpace(out); v != "" {
			host := ""
			if o.Hostname != nil {
				host, _ = o.Hostname()
			}
			v = strings.ReplaceAll(v, "$HOME", o.Home)
			v = strings.ReplaceAll(v, "$HOSTNAME", host)
			v = strings.ReplaceAll(v, "~", o.Home)
			if filepath.IsAbs(v) {
				return filepath.Clean(v)
			}
		}
	}
	legacy := filepath.Join(o.Home, ".tmux", "resurrect")
	if fi, err := os.Stat(legacy); err == nil && fi.IsDir() {
		return legacy
	}
	data := ""
	if o.Getenv != nil {
		data = o.Getenv("XDG_DATA_HOME")
	}
	if data == "" {
		data = filepath.Join(o.Home, ".local", "share")
	}
	return filepath.Join(data, "tmux", "resurrect")
}

// Saves is one resurrect dir's saves, read lazily: the stamps of every state
// file and sidecar, and each sidecar parsed at most once.
type Saves struct {
	Dir string
	// Stamps holds every stamp with a state file or a sidecar, newest first.
	Stamps []string
	state  map[string]bool
	side   map[string]bool
	cache  map[string]sidecarRead
}

type sidecarRead struct {
	sc  Sidecar
	err error
}

// OpenSaves lists dir. A missing dir is an empty list.
func OpenSaves(dir string) (*Saves, error) {
	s := &Saves{Dir: dir, state: map[string]bool{}, side: map[string]bool{}, cache: map[string]sidecarRead{}}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		st := StampOf(name)
		if st == "" {
			continue
		}
		if stateFileRE.MatchString(name) {
			s.state[st] = true
		} else {
			s.side[st] = true
		}
		if !seen[st] {
			seen[st] = true
			s.Stamps = append(s.Stamps, st)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(s.Stamps)))
	return s, nil
}

// HasState and HasSidecar report what exists for a stamp.
func (s *Saves) HasState(stamp string) bool   { return s.state[stamp] }
func (s *Saves) HasSidecar(stamp string) bool { return s.side[stamp] }

// Sidecar reads stamp's sidecar once (CS-TMUX-046); os.ErrNotExist when it
// has none.
func (s *Saves) Sidecar(stamp string) (Sidecar, error) {
	if r, ok := s.cache[stamp]; ok {
		return r.sc, r.err
	}
	var r sidecarRead
	if !s.side[stamp] {
		r.err = os.ErrNotExist
	} else {
		r.sc, r.err = ReadSidecar(filepath.Join(s.Dir, SidecarName(stamp)))
	}
	s.cache[stamp] = r
	return r.sc, r.err
}

// Last is the stamp "last" points at; "" when the link is missing, points
// outside the dir or at something that is not a state file.
func (s *Saves) Last() string {
	link, err := os.Readlink(filepath.Join(s.Dir, "last"))
	if err != nil {
		return ""
	}
	if filepath.IsAbs(link) {
		if filepath.Dir(filepath.Clean(link)) != filepath.Clean(s.Dir) {
			return ""
		}
		link = filepath.Base(link)
	}
	if strings.Contains(link, "/") || !stateFileRE.MatchString(link) {
		return ""
	}
	return StampOf(link)
}

// ErrBadSave is a --from value that names no save (CS-TMUX-049): exit 2.
var ErrBadSave = errors.New("not a save")

// ErrNoPrevious is "--from previous" with nothing to point at.
var ErrNoPrevious = errors.New("no previous tmux server")

// Resolve maps a --from value to a stamp (CS-TMUX-049): "last" (or ""),
// "previous", a stamp, or the base name of a state file or sidecar in the
// dir. running is the running tmux server (nil outside tmux), needed only
// for "previous"; idx is the lifetimes index (nil to scan).
func (s *Saves) Resolve(from string, running *Server, idx []Lifetime) (string, error) {
	switch {
	case from == "" || from == "last":
		if st := s.Last(); st != "" && (s.HasState(st) || s.HasSidecar(st)) {
			return st, nil
		}
		return "", fmt.Errorf("%w: the \"last\" link in %s names no save", ErrBadSave, s.Dir)
	case from == "previous":
		if running == nil {
			return "", fmt.Errorf("%w: \"previous\" needs the running tmux server (run it inside tmux)", ErrNoPrevious)
		}
		if st := s.Previous(*running, idx); st != "" {
			return st, nil
		}
		return "", fmt.Errorf("%w: no save records another tmux server", ErrNoPrevious)
	}
	st := from
	if !IsStamp(st) {
		if strings.Contains(from, "/") {
			return "", fmt.Errorf("%w: %q is a path; name a save in %s", ErrBadSave, from, s.Dir)
		}
		st = StampOf(from)
	}
	if st == "" {
		return "", fmt.Errorf("%w: %q", ErrBadSave, from)
	}
	if !s.HasState(st) && !s.HasSidecar(st) {
		return "", fmt.Errorf("%w: no save %s in %s", ErrBadSave, st, s.Dir)
	}
	return st, nil
}

// Previous is the newest save of a tmux server other than running
// (CS-TMUX-049): from the index, else a scan of at most ScanMax sidecars.
// "" when none is found.
func (s *Saves) Previous(running Server, idx []Lifetime) string {
	for _, l := range idx {
		if l.Server != running && (s.HasSidecar(l.Last) || s.HasState(l.Last)) {
			return l.Last
		}
	}
	n := 0
	for _, st := range s.Stamps {
		if !s.HasSidecar(st) {
			continue
		}
		if n++; n > ScanMax {
			break
		}
		if sc, err := s.Sidecar(st); err == nil && sc.Server != nil && *sc.Server != running {
			return st
		}
	}
	return ""
}

// Sparse is the sparse rule's verdict on one save (CS-TMUX-050).
type Sparse struct {
	Stamp string
	N     int
	// M is the baseline (the median); K how many values it came from, and
	// Lifetimes whether they were earlier servers' final saves (else the
	// saves right before). Known is false when no baseline was found.
	M, K      int
	Lifetimes bool
	Known     bool
	Sparse    bool
}

// IsSparse is the rule: at least SparseMinDrop fewer AND at least
// SparseFraction fewer than m.
func IsSparse(n, m int) bool {
	frac := (m*SparseFractionNum + SparseFractionDen - 1) / SparseFractionDen
	return m-n >= max(SparseMinDrop, frac)
}

// median of vs; with an even count the upper middle value, so the warning
// errs towards speaking.
func median(vs []int) int {
	c := append([]int(nil), vs...)
	sort.Ints(c)
	return c[len(c)/2]
}

// SparseOf judges the save stamp, whose sidecar has n rows and server srv
// (nil when unknown), against the lifetimes before its own (CS-TMUX-050).
func (s *Saves) SparseOf(stamp string, n int, srv *Server, idx []Lifetime) Sparse {
	v, _ := s.sparseOf(stamp, n, srv, idx, nil)
	return v
}

// sparseOf is SparseOf with a stop the sidecar scan checks before each read
// (--pin's whole-run deadline, CS-TMUX-064); done is false when it stopped,
// and the verdict is then not to be used.
func (s *Saves) sparseOf(stamp string, n int, srv *Server, idx []Lifetime, stop func() bool) (Sparse, bool) {
	v := Sparse{Stamp: stamp, N: n}
	var vals []int
	for _, l := range idx {
		if len(vals) == SparseLifetimes {
			break
		}
		if l.Last < stamp && (srv == nil || l.Server != *srv) {
			vals = append(vals, l.Rows)
		}
	}
	if len(vals) > 0 {
		v.Lifetimes = true
	} else {
		// The index knows no earlier lifetime: scan, newest first, for the
		// newest sidecar of each other server; failing that, the saves right
		// before this one.
		seen := map[Server]bool{}
		var before []int
		scanned := 0
		for _, st := range s.Stamps {
			if st >= stamp || !s.HasSidecar(st) {
				continue
			}
			if scanned++; scanned > ScanMax {
				break
			}
			if stop != nil && stop() {
				return v, false
			}
			sc, err := s.Sidecar(st)
			if err != nil {
				continue
			}
			if len(before) < SparseFallbackSaves {
				before = append(before, len(sc.Panes))
			}
			if sc.Server != nil && (srv == nil || *sc.Server != *srv) && !seen[*sc.Server] {
				seen[*sc.Server] = true
				vals = append(vals, len(sc.Panes))
				if len(vals) == SparseLifetimes {
					break
				}
			}
		}
		if len(vals) > 0 {
			v.Lifetimes = true
		} else {
			vals = before
		}
	}
	if len(vals) == 0 {
		return v, true
	}
	v.Known, v.K, v.M = true, len(vals), median(vals)
	v.Sparse = IsSparse(n, v.M)
	return v, true
}

// Line is the one line a restore or a dry-run prints before it acts on a
// sparse save (CS-TMUX-050); "" when it is not sparse.
func (v Sparse) Line() string {
	if !v.Sparse {
		return ""
	}
	what := fmt.Sprintf("the saves before the last %d tmux restarts had %d", v.K, v.M)
	if !v.Lifetimes {
		what = fmt.Sprintf("the %d saves before it had %d", v.K, v.M)
	}
	return fmt.Sprintf("claude-sandbox: this save (%s) has %d sandbox panes; %s. "+
		"If sessions are missing, list earlier saves: claude-sandbox tmux restore --list "+
		"(ignore this if you closed them on purpose).", v.Stamp, v.N, what)
}

// Run is one --list line: consecutive saves of one server naming the same
// sandbox sessions (CS-TMUX-048).
type Run struct {
	Newest, Oldest string
	Count          int
	// Record is false for saves without a sidecar ("no record"); Err is set
	// for an unreadable one.
	Record bool
	Err    error
	Server *Server
	// Rows, Active, Pending and Crashed count the newest save's rows.
	Rows, Active, Pending, Crashed int
	Last                           bool
	Sparse                         Sparse
	key                            string
}

// runKey is what makes two saves one run: the server and the multiset of
// sessions (container id, else name; conversation; state). Coordinates are
// left out: renumbered windows hold the same sessions.
func runKey(sc Sidecar) string {
	var ks []string
	for _, r := range sc.Panes {
		id := r.Mark.ContainerID
		if id == "" {
			id = r.Mark.Container
		}
		ks = append(ks, id+"\x00"+r.Mark.Conversation+"\x00"+r.Mark.State)
	}
	sort.Strings(ks)
	srv := "?"
	if sc.Server != nil {
		srv = strconv.Itoa(sc.Server.PID) + "." + strconv.FormatInt(sc.Server.Start, 10)
	}
	return srv + "\x01" + strings.Join(ks, "\x01")
}

// Runs collapses the saves at or after since into runs, newest first
// (CS-TMUX-048), each judged by the sparse rule.
func (s *Saves) Runs(since time.Time, idx []Lifetime) []Run {
	last := s.Last()
	var out []Run
	for _, st := range s.Stamps {
		if t := StampTime(st); t.IsZero() || t.Before(since) {
			continue
		}
		r := Run{Newest: st, Oldest: st, Count: 1}
		if s.HasSidecar(st) {
			sc, err := s.Sidecar(st)
			if err != nil {
				r.Err, r.key = err, "!"+st // an unreadable save is its own line
			} else {
				r.Record, r.Server, r.Rows = true, sc.Server, len(sc.Panes)
				for _, row := range sc.Panes {
					switch row.Mark.State {
					case StatePending:
						r.Pending++
					case StateCrashed:
						r.Crashed++
					default:
						r.Active++
					}
				}
				r.key = runKey(sc)
			}
		} else {
			r.key = "-"
		}
		r.Last = st == last
		if n := len(out); n > 0 && out[n-1].key == r.key && r.Err == nil {
			out[n-1].Oldest = st
			out[n-1].Count++
			out[n-1].Last = out[n-1].Last || r.Last
			continue
		}
		if r.Record {
			r.Sparse = s.SparseOf(st, r.Rows, r.Server, idx)
		}
		out = append(out, r)
	}
	return out
}

// Procedures are the two whole-layout procedures --list prints (CS-TMUX-048;
// plan 11 § 5 as corrected by 12 § 7), with the dir and stamp filled in.
// Procedure A ends by re-sourcing the config, which puts back the interval it
// sets; the live value is never echoed (the operator has set it to 0 by then).
func Procedures(dir, stamp string) []string {
	last := shq(filepath.Join(dir, "last"))
	ln := "ln -sf " + shq(StateFileName(stamp)) + " " + last
	return []string{
		"A, in this tmux server (recommended):",
		"    tmux set -g @continuum-save-interval 0     # so no save moves last meanwhile",
		"    " + ln,
		"    # then prefix + C-r: resurrect creates the missing windows and panes",
		"    tmux source-file ~/.tmux.conf              # puts back the interval your config sets",
		"B, a fresh tmux server (with continuum's systemd unit):",
		"    systemctl --user stop tmux.service         # its shutdown save moves last now",
		"    " + ln,
		"    systemctl --user start tmux.service        # continuum restores the repointed last",
		"  without the unit: tmux set -g @continuum-save-interval 0, wait a few seconds,",
		"    tmux kill-server, " + ln + ", then start tmux",
		"A reboot never restores a chosen save: its shutdown save moves last again.",
	}
}
