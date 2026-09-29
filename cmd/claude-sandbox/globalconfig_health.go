package main

// The launcher's global-config health check (CS-GCFG-001..015): the work is
// globalcfg.CheckHealth's; this wires the Env seams in and keeps it to host
// launches.

import (
	"testing"

	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// checkGlobalConfig runs the health check on a host launch and returns its
// result (nil inside a sandbox, CS-GCFG-001). prev is the check before the
// session, so the check after it does not repeat the same findings
// (CS-GCFG-014). Warn-only: every message goes to env.Err, which is stderr
// for a headless launch too.
func checkGlobalConfig(env *Env, prev *globalcfg.Health) *globalcfg.Health {
	// SkipGlobalConfigCheck is honoured under go test only: a stray setting
	// in a real build can never turn the check off.
	if (env.SkipGlobalConfigCheck && testing.Testing()) || hostdirs.InSandbox(env.Getenv) {
		return nil
	}
	_, _, _, home := hostIdentity(env.Getenv)
	return globalcfg.CheckHealth(globalcfg.HealthOptions{
		Home:      home,
		StateRoot: env.stateDir(),
		Getenv:    env.Getenv,
		Err:       env.Err,
		Now:       env.now,
		Previous:  prev,
	})
}
