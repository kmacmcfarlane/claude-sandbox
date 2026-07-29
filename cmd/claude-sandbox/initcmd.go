package main

import (
	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/initcmd"
)

// newInitCmd builds the init / init-ralph subcommand (CS-INIT, CS-INITR).
// Only its own options are accepted — launcher and claude flags are rejected
// by cobra's strict parsing (CS-INIT-002).
func newInitCmd(env *Env, ralph bool) *cobra.Command {
	name := "init"
	short := "Bootstrap .claude-sandbox/ in the project (config, env, gitignore, sidecar)"
	if ralph {
		name = "init-ralph"
		short = "Like init, plus seed ralph agent/ + scripts/ scaffolding"
	}
	var trackYes, trackNo, giYes, giNo, cpYes, cpNo, yes bool
	cmd := &cobra.Command{
		Use:           name,
		Short:         short,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			f := initcmd.Flags{Ralph: ralph, Yes: yes}
			f.TrackInHost = triState(trackYes, trackNo)
			f.Gitignore = triState(giYes, giNo)
			f.CopyParentDockerfile = triState(cpYes, cpNo)
			project, err := resolveProjectDir(env.Getenv)
			if err != nil {
				return err
			}
			return initcmd.Run(project, f, initcmd.Deps{
				Runner: env.Runner, Prompter: env.Prompter, Out: env.Out, Err: env.Err,
			})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&trackYes, "track-in-host", false, "set trackInHost: true (skip the interactive prompt)")
	fl.BoolVar(&trackNo, "no-track-in-host", false, "set trackInHost: false (skip the interactive prompt)")
	fl.BoolVar(&giYes, "gitignore", false, "add .gitignore entries without prompting")
	fl.BoolVar(&giNo, "no-gitignore", false, "skip .gitignore updates without prompting")
	fl.BoolVar(&cpYes, "copy-parent-dockerfile", false, "seed Dockerfile.example from a parent Dockerfile without prompting")
	fl.BoolVar(&cpNo, "no-copy-parent-dockerfile", false, "seed the generic Dockerfile.example without prompting")
	fl.BoolVar(&yes, "yes", false, "accept every prompt's default (non-interactive)")
	return cmd
}

func triState(yes, no bool) *bool {
	switch {
	case yes:
		t := true
		return &t
	case no:
		f := false
		return &f
	default:
		return nil
	}
}
