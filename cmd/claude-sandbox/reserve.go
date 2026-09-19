package main

// Race-free launch reservation (CS-SESS-048..054, CS-LNCH-057). The instance
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
	"path/filepath"
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

// minReclaimAge is how old a never-started ralph container must be before a
// launch reclaims its fixed name (CS-SESS-053). The lock does not cover the
// gap between a launcher's create and its exec of docker start, so a younger
// holder may be a live launch about to start; removing it would be worse than
// failing. Ten seconds still covers a person retrying after a failed start.
const minReclaimAge = 10 * time.Second

// unlockedWarning is printed when the launch proceeds without the lock. It is
// explicit about the gap: docker refuses a duplicate NAME, so the create
// conflict retry still keeps nouns unique, but nothing refuses a duplicate
// pid class, so a concurrent launch can pick the same one.
const unlockedWarning = "Warning: could not take the launch lock (%s); launching without it. " +
	"Container names stay unique (docker refuses a duplicate), but a launch running at the same moment " +
	"may get the same pid class, and one of the two sessions would then be missing from /peers.\n"

// acquireLaunchLock takes the host launch lock and returns its release. A lock
// that cannot be taken warns and degrades to an unserialized launch rather
// than blocking one (the discovery-failure precedent).
func acquireLaunchLock(env *Env, home string) func() {
	lock := env.Lock
	if lock == nil {
		if home == "" {
			// A relative lock path would lock per working directory, i.e. not at all.
			fmt.Fprintf(env.Err, unlockedWarning, "no home directory")
			return func() {}
		}
		lock = launch.FileLock{Path: launch.LaunchLockPath(home)}
	}
	release, err := lock.Acquire()
	if err != nil {
		fmt.Fprintf(env.Err, unlockedWarning, err)
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
func reserveContainer(env *Env, in launch.Inputs, wt worktreeChoice, ralph bool) (plan *launch.Plan, err error) {
	release := acquireLaunchLock(env, in.Home)
	defer release()

	// One shadow directory across attempts, so a retry rewrites the same files
	// instead of leaving a temp directory per attempt behind. Made under the
	// lock (CS-LNCH-080): a launch holding the lock then never sees another
	// launch's directory before that launch's create, and so cannot sweep it.
	if in.TempDir == "" {
		d, derr := launch.NewShadowDir(env.TempRoot)
		if derr != nil {
			return nil, derr
		}
		in.TempDir = d
		// CS-LNCH-083: a launch that fails before its container exists leaves
		// nothing behind. A successful reservation keeps the directory: the
		// container mounts it, and a later launch's sweep removes it.
		defer func() {
			if err != nil {
				os.RemoveAll(d)
			}
		}()
	}

	instance := in.Instance
	var lost []string // nouns a create conflict proved taken
	reclaimed := false
	for attempt := 1; ; attempt++ {
		found := discoverForReservation(env)
		if attempt == 1 {
			// After discovery, so the directories of the stale reservations it
			// just removed are already unreferenced.
			pruneShadowDirs(env, in.TempDir)
		}
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
		plan, err = launch.Build(in)
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
			// A fixed name cannot be re-picked. If the holder never started — a
			// reservation whose "docker start" failed (no TTY, a mount or OCI
			// error), which the launcher cannot clean up after exec'ing — it is
			// reclaimed at once rather than blocking ralph until the 60 s stale
			// cleanup. Once only: a second conflict is a real concurrent owner.
			// Only a holder old enough not to be a concurrent launch still
			// between its create and its start (see minReclaimAge).
			state, created := sessions.Inspect(env.Runner, plan.ContainerName)
			old := !created.IsZero() && env.now().Sub(created) > minReclaimAge
			if !reclaimed && state == sessions.StateCreated && old &&
				sessions.RemoveReservation(env.Runner, plan.ContainerName) == nil {
				reclaimed = true
				fmt.Fprintf(env.Err, "Removed %s: an earlier launch created it but it never started.\n", plan.ContainerName)
				continue
			}
			return nil, exitErr(2, "Error: a ralph container (%s) already exists for this project. "+
				"If it is running, stop it or wait for it to finish; if a ralph launch just failed to start, "+
				"its never-started container is reclaimed automatically once it is %s old, so retry in a few seconds.",
				plan.ContainerName, minReclaimAge)
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
	// Uncounted: session counts are not needed to pick, and counting would run
	// one docker top per running sandbox inside the critical section.
	found, err := sessions.DiscoverAllUncounted(env.Runner)
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
// launch's stale cleanup, so it is removed here first (CS-LNCH-057).
//
// The shadow directory goes with it (CS-LNCH-083), but only once the
// reservation is gone: a container that still exists may still mount it.
func startReserved(env *Env, plan *launch.Plan) error {
	err := plan.Start(env.Runner)
	if err != nil && sessions.RemoveReservation(env.Runner, plan.ContainerName) == nil && plan.ShadowDir != "" {
		os.RemoveAll(plan.ShadowDir)
	}
	return err
}

// pruneShadowDirs removes the shadow directories of earlier launches that no
// container uses any more (CS-LNCH-081). It runs under the launch lock and
// never blocks the launch: every failure is one warning (CS-LNCH-082).
func pruneShadowDirs(env *Env, own string) {
	root := env.TempRoot
	if root == "" {
		root = filepath.Dir(own)
	}
	if _, err := launch.PruneShadowDirs(env.Runner, root, os.Getuid(), env.now(), launch.ShadowDirMinAge, own); err != nil {
		fmt.Fprintf(env.Err, "Warning: could not clean up old shadow directories under %s: %v\n", root, err)
	}
}
