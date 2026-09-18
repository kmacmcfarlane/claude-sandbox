package main

// Race-free launch reservation (CS-SESS-048..054, CS-LNCH-056). The instance
// noun and the pid class are chosen from what discovery sees, and a container
// becomes visible to discovery only once it exists. So the choice and the
// "docker create" that makes it visible run inside one short, host-wide
// critical section; the container is then started with "docker start -ai"
// outside it. Image builds happen before the lock is taken, never under it.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// staleReservationAge is how old a created-but-never-started container must be
// before a launch treats it as an orphan (CS-SESS-052). Create-to-start takes
// milliseconds, so this leaves orders of magnitude of slack.
const staleReservationAge = 60 * time.Second

// maxCreateAttempts bounds the re-pick-and-retry on a create name conflict
// (CS-SESS-053). Launchers holding the lock cannot conflict with each other, so
// needing more than a couple of attempts means something outside the launcher
// keeps taking names, and failing loudly beats looping.
const maxCreateAttempts = 3

// acquireLaunchLock takes the host launch lock and returns its release. A lock
// that cannot be taken warns and degrades to an unserialized launch rather
// than blocking one (the discovery-failure precedent); the create conflict
// retry still keeps names unique.
func acquireLaunchLock(env *Env, home string) func() {
	lock := env.Lock
	if lock == nil {
		if home == "" {
			// A relative lock path would lock per working directory, i.e. not at all.
			fmt.Fprintln(env.Err, "Warning: could not take the launch lock (no home directory); launching without it.")
			return func() {}
		}
		lock = launch.FileLock{Path: launch.LaunchLockPath(home)}
	}
	release, err := lock.Acquire()
	if err != nil {
		fmt.Fprintf(env.Err, "Warning: could not take the launch lock (%v); launching without it.\n", err)
		return func() {}
	}
	return release
}

func (env *Env) now() time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

// reserveContainer runs the critical section and returns the plan of the
// container it created. in carries everything but the per-session picks;
// in.Instance is the noun picked before the image build (CS-SESS-054).
func reserveContainer(env *Env, in launch.Inputs, wt worktreeChoice, ralph bool) (*launch.Plan, error) {
	// One shadow directory across attempts, so a retry rewrites the same files
	// instead of leaving a temp directory per attempt behind.
	if in.TempDir == "" {
		d, err := os.MkdirTemp("", "claude-sandbox")
		if err != nil {
			return nil, err
		}
		in.TempDir = d
	}

	release := acquireLaunchLock(env, in.Home)
	defer release()

	instance := in.Instance
	var lost []string // nouns a create conflict proved taken
	for attempt := 1; ; attempt++ {
		found := discoverForReservation(env)
		project := sessions.ForProject(found, in.ProjectDir)

		if !ralph {
			taken := append(append(launch.ExistingWorktrees(wt.Root), sessions.Instances(project)...), lost...)
			if slices.Contains(taken, instance) {
				picked := sessions.PickNoun(taken, nil)
				if attempt == 1 {
					fmt.Fprintf(env.Out, "Instance '%s' was taken by a concurrent launch; using '%s'.\n", instance, picked)
				}
				instance = picked
				if name := wt.nameFor(instance, false); name != in.Worktree {
					if b := wt.banner(name); b != "" {
						fmt.Fprintln(env.Out, b)
					}
				}
			}
			in.Instance = instance
		}
		in.Worktree = wt.nameFor(instance, ralph)
		in.PIDClass = pidClassFrom(found)

		if attempt > 1 {
			// Build's warnings and banners were printed on the first attempt.
			in.Out, in.Err = io.Discard, io.Discard
		}
		plan, err := launch.Build(in)
		if err != nil {
			return nil, err
		}
		err = plan.Reserve(env.Runner, in.ProjectDir, env.Err)
		if err == nil {
			return plan, nil
		}
		if !errors.Is(err, launch.ErrNameConflict) {
			return nil, exitErr(2, "Error: %v", err)
		}
		if ralph {
			return nil, exitErr(2, "Error: a ralph container (%s) already exists for this project; stop it or wait for it to finish.", plan.ContainerName)
		}
		if attempt >= maxCreateAttempts {
			return nil, exitErr(2, "Error: container name %s is already in use; gave up after %d attempts to reserve a free instance name.", plan.ContainerName, attempt)
		}
		fmt.Fprintf(env.Err, "Container name %s was taken during launch; picking another instance.\n", plan.ContainerName)
		lost = append(lost, instance)
	}
}

// discoverForReservation lists every sandbox container on the host, created
// reservations included (CS-SESS-050), and removes the stale reservations
// among them (CS-SESS-052). Discovery failing must not block a launch: the
// picks then fall back to random, as they always have.
func discoverForReservation(env *Env) []sessions.Session {
	found, err := sessions.DiscoverAll(env.Runner)
	if err != nil {
		return nil
	}
	stale := sessions.Stale(found, env.now(), staleReservationAge)
	if len(stale) == 0 {
		return found
	}
	removed := map[string]bool{}
	for _, s := range stale {
		if sessions.RemoveReservation(env.Runner, s.Name) == nil {
			fmt.Fprintf(env.Err, "Removed stale reservation %s (created but never started).\n", s.Name)
			removed[s.Name] = true
		}
	}
	return slices.DeleteFunc(found, func(s sessions.Session) bool { return removed[s.Name] })
}

// pidClassFrom picks the pid class for a container about to be launched
// (CS-PID-004, CS-LNCH-039), from every sandbox on the host, not just this
// project's: the peer registry in ~/.claude is shared by all of them. Ralph
// gets one too.
func pidClassFrom(found []sessions.Session) string {
	return strconv.Itoa(sessions.PickClass(sessions.Classes(found), nil))
}

// startReserved hands the process over to "docker start -ai". Exec returns only
// on failure, and the reservation would then sit as an orphan until the next
// launch's stale cleanup, so it is removed here first (CS-LNCH-056).
func startReserved(env *Env, plan *launch.Plan) error {
	err := plan.Start(env.Runner)
	if err != nil {
		sessions.RemoveReservation(env.Runner, plan.ContainerName)
	}
	return err
}
