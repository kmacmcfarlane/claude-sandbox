package resumeguard

// Hardened reads of Claude Code's peer registry (CS-SESS-066; plan
// sandbox-reboot-restore 06 § 6, 07 § 7). Records are sandbox-writable: a
// session can plant a FIFO, a symlink or a huge file there. So the directory
// is opened once without following a symlink, every record is opened through
// that directory fd without following one and without blocking, and only a
// small regular file whose JSON matches its own name counts. No read can hang
// while the launch lock is held.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"syscall"
)

// MaxRecordSize caps a registry record; a larger file is malformed.
const MaxRecordSize = 64 << 10

// Record is the part of a registry record (<registry>/<pid>.json, written by
// Claude Code) the guard reads.
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

// ErrMalformed marks a record that is not a small regular file holding the
// JSON of its own pid and a canonical conversation id.
var ErrMalformed = errors.New("malformed registry record")

// ErrPartial additionally marks the malformed records a partial write could
// explain — unparsable JSON, an oversized file — the only ones worth reading
// again (CS-SESS-067). A symlink, a non-regular file, a pid mismatch or a
// non-UUID sessionId is final.
var ErrPartial = errors.New("possibly a partial write")

// MaxEntries caps the directory entries read from one registry directory
// (CS-SESS-066); a directory holding more counts as unreadable.
const MaxEntries = 10000

// recordName is a record's file name; the capture is its pid.
var recordName = regexp.MustCompile(`^([0-9]{1,10})\.json$`)

// uuidRE is a canonical conversation id.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether id is a canonical conversation id.
func IsUUID(id string) bool { return uuidRE.MatchString(id) }

// registryDir is an opened registry directory.
type registryDir struct {
	path string
	f    *os.File
}

// openDir opens a registry directory without following a symlink. A missing
// directory is (nil, nil): it holds no records. Anything else that is not a
// directory, or cannot be opened, is an error.
func openDir(path string) (*registryDir, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &registryDir{path: path, f: os.NewFile(uintptr(fd), path)}, nil
}

func (d *registryDir) close() { d.f.Close() }

// entry is one "<pid>.json" name in a registry directory.
type entry struct {
	name string
	pid  int
}

// entries lists the record names in the directory.
func (d *registryDir) entries() ([]entry, error) {
	names, err := d.f.Readdirnames(MaxEntries + 1)
	if err != nil && !(errors.Is(err, io.EOF) && len(names) == 0) {
		return nil, &os.PathError{Op: "readdir", Path: d.path, Err: err}
	}
	if len(names) > MaxEntries {
		return nil, fmt.Errorf("%s: more than %d entries", d.path, MaxEntries)
	}
	var out []entry
	for _, n := range names {
		m := recordName.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out = append(out, entry{name: n, pid: pid})
	}
	return out, nil
}

// read opens one record through the directory fd: no symlink, no blocking
// open (a FIFO), a regular file of at most MaxRecordSize, whose JSON pid is
// the file name's and whose sessionId is a canonical UUID. A record that
// vanished returns an error satisfying os.ErrNotExist.
func (d *registryDir) read(e entry) (Record, error) {
	fd, err := openat(int(d.f.Fd()), d.path, e.name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY|syscall.O_CLOEXEC)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, &os.PathError{Op: "open", Path: e.name, Err: err}
		}
		// ELOOP (a symlink) and the rest: not a record the guard can trust.
		return Record{}, fmt.Errorf("%w: %s: %v", ErrMalformed, e.name, err)
	}
	f := os.NewFile(uintptr(fd), e.name)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("%w: %s: %v", ErrMalformed, e.name, err)
	}
	if !st.Mode().IsRegular() {
		return Record{}, fmt.Errorf("%w: %s: not a regular file", ErrMalformed, e.name)
	}
	if st.Size() > MaxRecordSize {
		return Record{}, fmt.Errorf("%w (%w): %s: larger than %d bytes", ErrMalformed, ErrPartial, e.name, MaxRecordSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxRecordSize+1))
	if err != nil {
		return Record{}, fmt.Errorf("%w: %s: %v", ErrMalformed, e.name, err)
	}
	if len(data) > MaxRecordSize {
		return Record{}, fmt.Errorf("%w (%w): %s: larger than %d bytes", ErrMalformed, ErrPartial, e.name, MaxRecordSize)
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, fmt.Errorf("%w (%w): %s: %v", ErrMalformed, ErrPartial, e.name, err)
	}
	if r.PID != e.pid {
		return Record{}, fmt.Errorf("%w: %s: pid %d does not match its name", ErrMalformed, e.name, r.PID)
	}
	if !IsUUID(r.SessionID) {
		return Record{}, fmt.Errorf("%w: %s: sessionId is not a conversation id", ErrMalformed, e.name)
	}
	return r, nil
}
