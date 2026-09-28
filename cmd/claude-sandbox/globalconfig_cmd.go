package main

// "claude-sandbox global-config migrate|revert|accept" (CS-GCFG-041..055):
// the host commands that switch ~/.claude.json between the legacy and the
// linked layout with checks, and record a baseline. The work is
// internal/globalcfg's; this file wires the Env seams in and maps a refusal
// to exit 1.

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// exitRefused is a global-config command that refused or aborted.
const exitRefused = 1

func newGlobalConfigCmd(env *Env) *cobra.Command {
	gc := &cobra.Command{
		Use:           "global-config",
		Short:         "Switch ~/.claude.json between the legacy and the linked layout (host only)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exitErr(2, "Error: unknown global-config command %q (migrate, revert, accept)", args[0])
			}
			fmt.Fprintln(env.Out, cmd.UsageString())
			return exitErr(2, "Error: global-config needs a command: migrate, revert or accept")
		},
	}
	swap := func(name, short string, run func(globalcfg.SwapOptions) error) *cobra.Command {
		var force bool
		c := &cobra.Command{
			Use:           name,
			Short:         short,
			Args:          cobra.NoArgs,
			SilenceUsage:  true,
			SilenceErrors: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				_, _, _, home := hostIdentity(env.Getenv)
				o := globalcfg.SwapOptions{
					Home:         home,
					ConfigDirEnv: env.Getenv("CLAUDE_CONFIG_DIR"),
					InSandbox:    hostdirs.InSandbox(env.Getenv),
					Force:        force,
					Runner:       env.Runner,
					Out:          env.Out,
					Err:          env.Err,
					UID:          os.Getuid(),
				}
				if !o.InSandbox {
					o.StateRoot = env.stateDir()
				}
				return globalConfigResult(name, run(o))
			},
		}
		c.Flags().BoolVar(&force, "force", false, "Go on although containers still use the global config (they keep a stale view; relaunch them)")
		return c
	}
	accept := &cobra.Command{
		Use:           "accept",
		Short:         "Record the current global config as the baseline snapshot",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, _, home := hostIdentity(env.Getenv)
			o := globalcfg.AcceptOptions{
				Home:      home,
				Getenv:    env.Getenv,
				InSandbox: hostdirs.InSandbox(env.Getenv),
				Out:       env.Out,
			}
			if !o.InSandbox {
				o.StateRoot = env.stateDir()
			}
			return globalConfigResult("accept", globalcfg.Accept(o))
		},
	}
	gc.AddCommand(
		swap("migrate", "Make ~/.claude.json a link to ~/.claude/.claude.json (the linked layout)", globalcfg.Migrate),
		swap("revert", "Make ~/.claude.json a regular file again (the legacy layout)", globalcfg.Revert),
		accept,
	)
	return gc
}

// globalConfigResult maps a command's error to exit 1 with one Error line.
func globalConfigResult(name string, err error) error {
	if err == nil {
		return nil
	}
	var r *globalcfg.Refused
	if errors.As(err, &r) {
		return exitErr(exitRefused, "Error: global-config %s: %s", name, r.Msg)
	}
	return exitErr(exitRefused, "Error: global-config %s: %v", name, err)
}
