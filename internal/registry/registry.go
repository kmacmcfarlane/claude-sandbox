// Package registry reads Claude Code's peer registry — the records
// <config dir>/sessions/<pid>.json that every claude writes — for the two
// readers that must not trust it: the tmux save hook (CS-TMUX-033/034) and
// the resume guard (CS-SESS-066/067). Records are sandbox-writable: a session
// can plant a FIFO, a symlink, a huge file or a crafted name there. So the
// directory is opened once without following a symlink, at most MaxEntries
// entries are listed, every record is opened through that directory fd with
// O_NOFOLLOW|O_NONBLOCK|O_NOCTTY (a FIFO never blocks, a tty never becomes a
// controlling terminal), and only a regular file of at most MaxRecordSize
// whose JSON pid is its file name's and whose sessionId is a canonical UUID
// counts. Names are cleaned (CleanName) and name sources whitelisted
// (NameSources) here, once, for every reader.
//
// The package reads and classifies; policy is the caller's. A bad record is
// an error wrapping ErrMalformed, and additionally ErrPartial when a partial
// write could explain it (unparsable JSON, an oversized file): the save hook
// treats any error as a miss, the resume guard retries ErrPartial and fails
// closed on the rest. It lives in its own package, importing nothing of the
// repo's, so both tmuxpane and resumeguard (which imports launch and
// sessions) can use it without a cycle.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// MaxRecordSize caps a registry record; a larger file is malformed (plan
// sandbox-reboot-restore 06 § 6).
const MaxRecordSize = 64 << 10

// MaxEntries caps the directory entries read from one registry directory; a
// directory holding more counts as unreadable (CS-SESS-066/067, CS-TMUX-034).
const MaxEntries = 10000

// MaxNameLen is the longest name kept, in characters (06 § 6).
const MaxNameLen = 200

// NameSources is the registry's name source set in Claude Code 2.1.284 (plan
// 09 finding 2): the registry reader there whitelists exactly these. Only
// "user" is a name the user gave (/rename, --name).
var NameSources = map[string]bool{
	"user": true, "peer": true, "derived": true, "collision": true, "auto": true, "hook": true,
}

// ErrMalformed marks a record that is not a small regular file holding the
// JSON of its own pid and a canonical conversation id.
var ErrMalformed = errors.New("malformed registry record")

// ErrPartial additionally marks the malformed records a partial write could
// explain — unparsable JSON, an oversized file — the only ones worth reading
// again (CS-SESS-067). A symlink, a non-regular file, a pid mismatch or a
// non-UUID sessionId is final (structural).
var ErrPartial = errors.New("possibly a partial write")

// Record is the part of a registry record the readers take. Name is already
// cleaned (CleanName), and NameSource is kept only when Name is non-empty and
// the source is in NameSources.
type Record struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	// StartedAt is unix ms.
	StartedAt int64 `json:"startedAt"`
	// ProcStart is field 22 of /proc/<pid>/stat (starttime) as decimal text.
	ProcStart string `json:"procStart"`
	// PIDDomain is "linux::pid:[<inode>]": the pid namespace the pid is in.
	PIDDomain  string `json:"pidDomain"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
}

// recordName is a record's file name; the capture is its pid.
var recordName = regexp.MustCompile(`^([0-9]{1,10})\.json$`)

// uuidRE is a canonical conversation id.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether id is a canonical conversation id.
func IsUUID(id string) bool { return uuidRE.MatchString(id) }

// Dir is an opened registry directory.
type Dir struct {
	path string
	f    *os.File
}

// OpenDir opens a registry directory with O_DIRECTORY|O_NOFOLLOW: a symlink
// or a non-directory is an error. A missing directory is an error satisfying
// os.ErrNotExist, which callers read as "no records".
func OpenDir(path string) (*Dir, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &Dir{path: path, f: os.NewFile(uintptr(fd), path)}, nil
}

// Path is the directory's path as opened.
func (d *Dir) Path() string { return d.path }

// Close closes the directory.
func (d *Dir) Close() error { return d.f.Close() }

// Entry is one "<pid>.json" name in a registry directory.
type Entry struct {
	Name string
	PID  int
}

// Entries lists the record names in the directory; other names are ignored.
// More than MaxEntries entries of any kind is an error.
func (d *Dir) Entries() ([]Entry, error) {
	names, err := d.f.Readdirnames(MaxEntries + 1)
	if err != nil && !(errors.Is(err, io.EOF) && len(names) == 0) {
		return nil, &os.PathError{Op: "readdir", Path: d.path, Err: err}
	}
	if len(names) > MaxEntries {
		return nil, fmt.Errorf("%s: more than %d entries", d.path, MaxEntries)
	}
	var out []Entry
	for _, n := range names {
		m := recordName.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: n, PID: pid})
	}
	return out, nil
}

// Read opens one record through the directory fd and validates it. A record
// that vanished returns an error satisfying os.ErrNotExist; any other failure
// wraps ErrMalformed, and ErrPartial too when a partial write could explain
// it.
func (d *Dir) Read(e Entry) (Record, error) {
	fd, err := openat(int(d.f.Fd()), d.path, e.Name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY|syscall.O_CLOEXEC)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, &os.PathError{Op: "open", Path: e.Name, Err: err}
		}
		// ELOOP (a symlink) and the rest: not a record a reader can trust.
		return Record{}, fmt.Errorf("%w: %s: %v", ErrMalformed, e.Name, err)
	}
	f := os.NewFile(uintptr(fd), e.Name)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("%w: %s: %v", ErrMalformed, e.Name, err)
	}
	if !st.Mode().IsRegular() {
		return Record{}, fmt.Errorf("%w: %s: not a regular file", ErrMalformed, e.Name)
	}
	if st.Size() > MaxRecordSize {
		return Record{}, fmt.Errorf("%w (%w): %s: larger than %d bytes", ErrMalformed, ErrPartial, e.Name, MaxRecordSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxRecordSize+1))
	if err != nil {
		return Record{}, fmt.Errorf("%w: %s: %v", ErrMalformed, e.Name, err)
	}
	if len(data) > MaxRecordSize {
		return Record{}, fmt.Errorf("%w (%w): %s: larger than %d bytes", ErrMalformed, ErrPartial, e.Name, MaxRecordSize)
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, fmt.Errorf("%w (%w): %s: %v", ErrMalformed, ErrPartial, e.Name, err)
	}
	if r.PID != e.PID {
		return Record{}, fmt.Errorf("%w: %s: pid %d does not match its name", ErrMalformed, e.Name, r.PID)
	}
	if !IsUUID(r.SessionID) {
		return Record{}, fmt.Errorf("%w: %s: sessionId is not a conversation id", ErrMalformed, e.Name)
	}
	r.Name = CleanName(r.Name)
	if r.Name == "" || !NameSources[r.NameSource] {
		r.NameSource = ""
	}
	return r, nil
}

// Result is one record's outcome in ReadAll: Record is valid when Err is nil.
type Result struct {
	Entry  Entry
	Record Record
	Err    error
}

// ReadAll opens dir, lists it and reads every record once. err is for the
// directory itself (missing, a symlink, not a directory, unreadable, too many
// entries); each record's own outcome is in its Result.
func ReadAll(dir string) ([]Result, error) {
	d, err := OpenDir(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	es, err := d.Entries()
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(es))
	for _, e := range es {
		r, err := d.Read(e)
		out = append(out, Result{Entry: e, Record: r, Err: err})
	}
	return out, nil
}

// CleanName is a registry name as the readers keep it (CS-TMUX-034):
// Printable, and "" when the result is longer than MaxNameLen characters or
// starts with "-" (it could read as a flag).
func CleanName(s string) string {
	s = Printable(s)
	if utf8.RuneCountInString(s) > MaxNameLen || strings.HasPrefix(s, "-") {
		return ""
	}
	return s
}

// Printable turns control characters into spaces, drops format characters
// (Cf: bidi overrides, zero-width joiners), private-use (Co) and surrogate
// (Cs) code points, and collapses whitespace, for text read from a mark or a
// sandbox-writable registry record and shown to the operator (the
// notify-webhook precedent; CS-TMUX-034).
func Printable(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
