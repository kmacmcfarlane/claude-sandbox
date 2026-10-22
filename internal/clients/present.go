package clients

import (
	"errors"
	"fmt"
	"os"
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
func Present(o Options, c Target, all []Target) Presence {
	var p Presence
	var errs []error
	if IsFullID(c.ID) {
		leases, err := ReadLeases(o.StateRoot, c.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("leases: %w", err))
		}
		for _, l := range leases {
			if !l.Held {
				continue
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
		if procs[i].Of(c, all) {
			p.Clients = append(p.Clients, Client{Kind: procs[i].Inv.Kind, Source: SourceScan, Proc: &procs[i]})
		}
	}
	p.Err = errors.Join(errs...)
	return p
}
