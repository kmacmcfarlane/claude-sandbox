package clients

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Source says where a client was seen.
type Source string

const (
	// SourceLease: a held lease, the launcher's own word.
	SourceLease Source = "lease"
	// SourceScan: a docker CLI process found in /proc.
	SourceScan Source = "scan"
)

// Client is one live client of a container.
type Client struct {
	Kind   Kind
	Source Source
	// Since is the lease's start (SourceLease).
	Since time.Time
	// TmuxPane is the lease holder's pane, "" for none.
	TmuxPane string
	// Lease is the lease file (SourceLease).
	Lease string
	// Proc is the scanned process (SourceScan).
	Proc *Proc
	// Child is, on a lease row, the scanned docker client whose parent is
	// the lease's holder: the launcher's own session child, folded into
	// its lease so one client is listed once (CS-SESS-098). nil otherwise.
	Child *Proc
}

// Presence is Present's answer.
type Presence struct {
	Clients []Client
	// Err is set when the answer is "cannot tell": the lease directory or
	// /proc could not be read. Clients still lists what was seen.
	Err error
}

// CannotTell reports whether the answer is incomplete.
func (p Presence) CannotTell() bool { return p.Err != nil }

// Terminal is the clients of the container's own terminal: primary and
// attach leases, and scanned "start -a" and "attach" processes. A lease or
// process whose kind is unknown counts here too (conservative).
func (p Presence) Terminal() []Client {
	var out []Client
	for _, c := range p.Clients {
		if c.Kind.Terminal() || c.Kind == "" {
			out = append(out, c)
		}
	}
	return out
}

// Joins is the join clients (a join lease, a scanned exec).
func (p Presence) Joins() []Client {
	var out []Client
	for _, c := range p.Clients {
		if c.Kind == KindJoin {
			out = append(out, c)
		}
	}
	return out
}

// Options are Present's seams.
type Options struct {
	// StateRoot holds the leases; "" panics under go test.
	StateRoot string
	// ProcRoot is /proc; "" panics under go test.
	ProcRoot string
	// UID is the invoking user; a negative value means os.Getuid.
	UID int
}

// Present returns container c's live clients (CS-SESS-098): every held
// lease, then every docker CLI process of the invoking user that is a
// client of c. all is every discovered container (for docker's
// unique-prefix rule). It removes nothing.
//
// A scanned process whose parent pid is the pid a held lease's file name
// carries ("<pid>.<rand>.lease") is that launcher's own session child: it is
// folded into the lease row (Client.Child) instead of being listed again —
// one launcher, one client. At most one process folds into a lease; a second
// child of the same launcher, and every process of any other parent, stays
// its own row, so a real second client is never hidden.
func Present(o Options, c Target, all []Target) Presence {
	var p Presence
	var errs []error
	holders := map[int]int{} // a held lease's holder pid -> its row
	if IsFullID(c.ID) {
		leases, err := ReadLeases(o.StateRoot, c.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("leases: %w", err))
		}
		for _, l := range leases {
			if !l.Held {
				continue
			}
			if pid := LeaseHolder(l.Path); pid > 0 {
				if _, dup := holders[pid]; !dup {
					holders[pid] = len(p.Clients)
				}
			}
			p.Clients = append(p.Clients, Client{Kind: l.Record.Kind, Source: SourceLease,
				Since: time.UnixMilli(l.Record.Since), TmuxPane: l.Record.TmuxPane, Lease: l.Path})
		}
	}
	uid := o.UID
	if uid < 0 {
		uid = os.Getuid()
	}
	procs, err := Scan(o.ProcRoot, uid)
	if err != nil {
		errs = append(errs, fmt.Errorf("process scan: %w", err))
	}
	for i := range procs {
		if !procs[i].Of(c, all) {
			continue
		}
		if row, ok := holders[procs[i].PPID]; ok && procs[i].PPID > 0 && p.Clients[row].Child == nil {
			lr := &p.Clients[row]
			lr.Child = &procs[i]
			if lr.Kind == "" {
				// The lease's record was unreadable: the process says what
				// it is.
				lr.Kind = procs[i].Inv.Kind
			}
			continue
		}
		p.Clients = append(p.Clients, Client{Kind: procs[i].Inv.Kind, Source: SourceScan, Proc: &procs[i]})
	}
	p.Err = errors.Join(errs...)
	return p
}

// LeaseHolder is the holder pid a lease's file name carries
// ("<pid>.<rand>.lease"), 0 when the name carries none.
func LeaseHolder(path string) int {
	head, _, ok := strings.Cut(filepath.Base(path), ".")
	if !ok {
		return 0
	}
	pid, err := strconv.Atoi(head)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
