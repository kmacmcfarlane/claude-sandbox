package main

// Client presence (CS-LNCH-204, CS-DIR-029; plan detached-sandboxes F1): the
// launcher's own side — a lease held while a session client lives, and the
// prune of what dead clients left. The reader (clients.Present) is for the
// session-end helper and the listing.

import (
	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// takeLease takes a client lease of kind on container id (CS-LNCH-204), as
// soon as the container is known: a new container right after its create, an
// attach or a join as soon as it is chosen. nil — and nothing written —
// inside a sandbox (its state root is container-local), for an id that is
// not 64 hex, or when the lease cannot be taken: the launch goes on, and the
// process scan still sees the docker client. The caller releases it on every
// return path (Release is nil-safe and idempotent).
func takeLease(env *Env, id string, kind clients.Kind) *clients.Lease {
	if hostdirs.InSandbox(env.Getenv) || !clients.IsFullID(id) {
		return nil
	}
	pane, _ := tmuxpane.FromEnv(env.Getenv)
	l, err := clients.Take(env.stateDir(), id, kind, env.now(), pane)
	if err != nil {
		return nil
	}
	return l
}

// pruneClients removes what dead clients left under the state root
// (CS-DIR-029), from the launch's first discovery under the launch lock:
// every row (and every exited row docker is still removing) keeps its
// directory. A failed discovery removes nothing; failures are silent; inside
// a sandbox nothing is pruned.
func pruneClients(env *Env, found, removing []sessions.Session, derr error) {
	if hostdirs.InSandbox(env.Getenv) {
		return
	}
	var ids []string
	for _, s := range append(append([]sessions.Session(nil), found...), removing...) {
		ids = append(ids, s.ID)
	}
	_ = clients.Prune(env.stateDir(), ids, derr, env.now())
}
