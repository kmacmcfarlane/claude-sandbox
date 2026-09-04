package main

import (
	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/pidslot"
)

// newPidslotCmd is the in-container helper `claude-sandbox pidslot -- <cmd...>`
// (CS-PID-001..003): it lands <cmd> on the container's pid class and is what
// the entrypoint and `docker exec` joins run instead of claude directly.
func newPidslotCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:                "pidslot -- <cmd...>",
		Short:              "Land a command on this container's pid class (in-container helper)",
		Hidden:             true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			ops := pidslot.Real()
			ops.Getenv = env.Getenv
			ops.Stderr = env.Err
			if env.PidslotOps != nil {
				ops = *env.PidslotOps
			}
			if rc := pidslot.Run(ops, args); rc != 0 {
				return exitErr(rc, "")
			}
			return nil
		},
	}
}
