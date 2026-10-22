package clients

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Invocation is a docker CLI command line read as a client of containers
// (CS-SESS-098).
type Invocation struct {
	// Kind: start -a = primary, attach = attach, exec = join.
	Kind Kind
	// Containers are the container arguments: attach's and exec's first
	// positional, every positional of start.
	Containers []string
	// Conservative is set when an option the parser does not know came
	// before the container argument: the process then counts for any
	// container it names anywhere (Words).
	Conservative bool
	// Words are every argument after argv[0].
	Words []string
}

// flagSpec is one docker option: takesValue, or a bool.
type flagSpec struct {
	long, short string
	value       bool
}

// globalFlags are docker's own options before the subcommand.
var globalFlags = []flagSpec{
	{"config", "", true}, {"context", "c", true}, {"host", "H", true},
	{"log-level", "l", true}, {"tlscacert", "", true}, {"tlscert", "", true},
	{"tlskey", "", true}, {"debug", "D", false}, {"tls", "", false},
	{"tlsverify", "", false}, {"help", "h", false}, {"version", "v", false},
}

// subFlags are each client subcommand's options (docker 29). --sig-proxy is
// a bool ("--sig-proxy=false"), never followed by a value.
var subFlags = map[string][]flagSpec{
	"attach": {{"detach-keys", "", true}, {"no-stdin", "", false}, {"sig-proxy", "", false}, {"help", "", false}},
	"start": {{"detach-keys", "", true}, {"checkpoint", "", true}, {"checkpoint-dir", "", true},
		{"attach", "a", false}, {"interactive", "i", false}, {"help", "", false}},
	"exec": {{"detach-keys", "", true}, {"env", "e", true}, {"env-file", "", true},
		{"user", "u", true}, {"workdir", "w", true}, {"detach", "d", false},
		{"interactive", "i", false}, {"tty", "t", false}, {"privileged", "", false}, {"help", "", false}},
}

func lookup(specs []flagSpec, name string, short bool) (flagSpec, bool) {
	for _, s := range specs {
		if (short && s.short == name) || (!short && s.long == name) {
			return s, true
		}
	}
	return flagSpec{}, false
}

// optParser walks options the way pflag does. set records a bool flag's
// value by its long name.
type optParser struct {
	specs   []flagSpec
	unknown bool
	set     map[string]bool
}

// option consumes the option at args[i] and returns how many words it took
// (1, or 2 with a separate value).
func (p *optParser) option(args []string, i int) int {
	a := args[i]
	if strings.HasPrefix(a, "--") {
		name, val, hasVal := strings.Cut(a[2:], "=")
		s, ok := lookup(p.specs, name, false)
		if !ok {
			p.unknown = true
			return 1
		}
		if s.value {
			if hasVal {
				return 1
			}
			return 2
		}
		b := true
		if hasVal {
			v, err := strconv.ParseBool(val)
			if err != nil {
				p.unknown = true
				return 1
			}
			b = v
		}
		p.set[s.long] = b
		return 1
	}
	// A short cluster: bools, the last one maybe taking a value (the rest
	// of the word, or the next word).
	cl := a[1:]
	for j := 0; j < len(cl); j++ {
		s, ok := lookup(p.specs, cl[j:j+1], true)
		if !ok {
			p.unknown = true
			return 1
		}
		rest := cl[j+1:]
		if s.value {
			if rest == "" {
				return 2
			}
			return 1
		}
		if strings.HasPrefix(rest, "=") {
			v, err := strconv.ParseBool(rest[1:])
			if err != nil {
				p.unknown = true
				return 1
			}
			p.set[s.long] = v
			return 1
		}
		p.set[s.long] = true
	}
	return 1
}

// isOption reports whether a word is an option (not "-" or a positional).
func isOption(a string) bool { return len(a) > 1 && a[0] == '-' }

// ParseInvocation reads argv as a docker CLI client command (CS-SESS-098):
// argv[0]'s basename is docker or com.docker.cli; after the global options
// the subcommand is attach, start (with -a) or exec (without -d), or
// "container attach|start|exec"; the container argument is the first
// positional after the subcommand's own options (start: every positional).
// ok is false for anything else — "docker events", "docker inspect", a
// start without -a, a detached exec.
func ParseInvocation(argv []string) (inv Invocation, ok bool) {
	if len(argv) < 2 {
		return inv, false
	}
	if b := filepath.Base(argv[0]); b != "docker" && b != "com.docker.cli" {
		return inv, false
	}
	args := argv[1:]
	inv.Words = args
	// Global options. Docker's root command stops at its first positional.
	g := &optParser{specs: globalFlags, set: map[string]bool{}}
	i := 0
	for i < len(args) && isOption(args[i]) && args[i] != "--" {
		i += g.option(args, i)
	}
	if i < len(args) && args[i] == "--" {
		i++
	}
	if g.unknown {
		// An unknown global option may have taken a value or not: the
		// subcommand is the first client subcommand word from here.
		for i < len(args) && args[i] != "container" && subFlags[args[i]] == nil {
			i++
		}
	}
	if i >= len(args) {
		return inv, false
	}
	sub := args[i]
	i++
	if sub == "container" {
		if i >= len(args) {
			return inv, false
		}
		sub = args[i]
		i++
	}
	specs, known := subFlags[sub]
	if !known {
		return inv, false
	}
	p := &optParser{specs: specs, set: map[string]bool{}, unknown: g.unknown}
	// exec stops at its container (what follows is the command); attach and
	// start parse options anywhere (pflag's interspersed default).
	interspersed := sub != "exec"
	var pos []string
	for i < len(args) {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if isOption(a) && (interspersed || len(pos) == 0) {
			i += p.option(args, i)
			continue
		}
		pos = append(pos, a)
		if !interspersed {
			break
		}
		i++
	}
	switch sub {
	case "attach":
		inv.Kind = KindAttach
		if len(pos) > 0 {
			inv.Containers = pos[:1]
		}
	case "start":
		if !p.set["attach"] && !p.unknown {
			return inv, false
		}
		inv.Kind = KindPrimary
		inv.Containers = pos
	case "exec":
		if p.set["detach"] && !p.unknown {
			return inv, false
		}
		inv.Kind = KindJoin
		if len(pos) > 0 {
			inv.Containers = pos[:1]
		}
	}
	inv.Conservative = p.unknown
	if len(inv.Containers) == 0 && !inv.Conservative {
		return inv, false
	}
	return inv, true
}

// Target is a container as docker names it.
type Target struct {
	// ID is the 64-hex id; Name is the container name (no leading "/").
	ID, Name string
}

// Names reports whether word names c the way docker resolves it: the full
// id, then a name (a leading "/" allowed), then a hex prefix of the id that
// is no other container's prefix (docker's unique-prefix rule, any length).
// all is every discovered container, c included; a word that is another
// container's id or name names that one.
func Names(word string, c Target, all []Target) bool {
	if word == "" {
		return false
	}
	if c.ID != "" && word == c.ID {
		return true
	}
	name := strings.TrimPrefix(word, "/")
	for _, o := range all {
		if o.ID != c.ID && (word == o.ID || (o.Name != "" && name == o.Name)) {
			return false
		}
	}
	if c.Name != "" && name == c.Name {
		return true
	}
	if c.ID == "" || !isHex(word) || !strings.HasPrefix(c.ID, word) {
		return false
	}
	for _, o := range all {
		if o.ID != c.ID && strings.HasPrefix(o.ID, word) {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return s != ""
}

// Proc is a host docker CLI process the scan found.
type Proc struct {
	PID  int
	Argv []string
	Inv  Invocation
	// StartTicks is /proc/<pid>/stat's starttime (clock ticks since boot),
	// 0 when unreadable: with BootTime it tells a client started after a
	// moment from one started before it (F2's reattach rule).
	StartTicks uint64
}

// Of reports whether p is a client of c (all: every discovered container).
func (p Proc) Of(c Target, all []Target) bool {
	words := p.Inv.Containers
	if p.Inv.Conservative {
		words = p.Inv.Words
	}
	for _, w := range words {
		if Names(w, c, all) {
			return true
		}
	}
	return false
}

// maxCmdline bounds a cmdline read.
const maxCmdline = 256 << 10

// Scan lists the docker CLI client processes of the invoking user (uid) in
// procRoot (CS-SESS-098): one pass over <proc>/*/cmdline. A process of
// another real uid (the Uid line of <proc>/<pid>/status) is ignored, and an
// entry that cannot be read is skipped. A failed listing is an error:
// "cannot tell". procRoot "" panics under go test (the real /proc).
func Scan(procRoot string, uid int) ([]Proc, error) {
	root := procRootOf(procRoot)
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		argv := readCmdline(filepath.Join(dir, "cmdline"))
		inv, ok := ParseInvocation(argv)
		if !ok {
			continue
		}
		if u, ok := realUID(filepath.Join(dir, "status")); !ok || u != uid {
			continue
		}
		out = append(out, Proc{PID: pid, Argv: argv, Inv: inv, StartTicks: startTicks(filepath.Join(dir, "stat"))})
	}
	return out, nil
}

func procRootOf(procRoot string) string {
	if procRoot != "" {
		return procRoot
	}
	if testing.Testing() {
		panic("clients: ProcRoot unset under go test (would read the real /proc); pass a scratch directory")
	}
	return "/proc"
}

// readSmall reads at most limit bytes of a regular-or-proc file, never
// following a link.
func readSmall(p string, limit int64) []byte {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	return b
}

func readCmdline(p string) []string {
	b := readSmall(p, maxCmdline)
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	return strings.Split(string(b), "\x00")
}

// realUID reads the first (real) uid of a status file's Uid line.
func realUID(p string) (int, bool) {
	sc := bufio.NewScanner(bytes.NewReader(readSmall(p, 64<<10)))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return 0, false
			}
			u, err := strconv.Atoi(f[0])
			return u, err == nil
		}
	}
	return 0, false
}

// startTicks reads field 22 of a stat file (after the last ')').
func startTicks(p string) uint64 {
	s := string(readSmall(p, 4096))
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); starttime is field 22.
	if len(f) < 20 {
		return 0
	}
	n, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ClockTicks is USER_HZ, the unit of starttime: 100 on every Linux ABI Go
// supports.
const ClockTicks = 100

// BootTime reads the host's boot time (btime in <proc>/stat).
func BootTime(procRoot string) (time.Time, error) {
	sc := bufio.NewScanner(bytes.NewReader(readSmall(filepath.Join(procRootOf(procRoot), "stat"), 1<<20)))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(n, 0), nil
		}
	}
	return time.Time{}, errors.New("no btime in stat")
}

// Started is when p started, from the boot time; zero when unknown. Its
// resolution is btime's whole second.
func (p Proc) Started(boot time.Time) time.Time {
	if p.StartTicks == 0 || boot.IsZero() {
		return time.Time{}
	}
	return boot.Add(time.Duration(p.StartTicks) * time.Second / ClockTicks)
}
