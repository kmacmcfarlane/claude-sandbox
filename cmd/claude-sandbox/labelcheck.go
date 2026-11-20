package main

import (
	"fmt"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// checkCLIModel refuses a command-line --model that could not be a label
// value (CS-LNCH-225): the model becomes the claude-sandbox.model label and
// claude's own --model, and no model name holds a control or bidi
// character. The value is never echoed.
func checkCLIModel(v string) error {
	if why := launch.LabelValueProblem(v); why != "" {
		return exitErr(2, "Error: --model holds %s; a model name never does.", why)
	}
	return nil
}

// configuredLabelValues lists the launch values that become container labels
// and that the operator configures, each with its source (CS-LNCH-224): the
// model (the command line, else the config.yaml that set it), memoryLimit,
// the project directory and CLAUDE_CONFIG_DIR. The values the launcher
// derives are left to Build's backstop (CS-LNCH-226).
func configuredLabelValues(env *Env, f *launchFlags, cfg *cascade.Config, snap []cascade.ConfigFile, projectDir string) []launch.ConfiguredLabelValue {
	keySource := func(key string) string {
		if src := cascade.KeySourceOf(snap, key); src != "" {
			return src
		}
		return "the sandbox config"
	}
	var out []launch.ConfiguredLabelValue
	if f.Model != "" {
		out = append(out, launch.ConfiguredLabelValue{What: "--model", Source: "command line", Value: f.Model})
	} else if cfg.Model != "" {
		out = append(out, launch.ConfiguredLabelValue{What: "model", Source: keySource("model"), Value: cfg.Model})
	}
	if cfg.MemoryLimit != "" {
		out = append(out, launch.ConfiguredLabelValue{What: "memoryLimit", Source: keySource("memoryLimit"), Value: cfg.MemoryLimit})
	}
	// Cascade mounts are not labels, but docker prints their sources in
	// {{.Mounts}}, which discovery reads (CS-LNCH-224): each is named with
	// the config.yaml whose own mounts list holds it.
	for i, m := range cfg.Mounts {
		for _, side := range []struct{ what, v string }{{"host", m.Host}, {"container", m.Container}} {
			if launch.LabelValueProblem(side.v) == "" {
				continue
			}
			out = append(out, launch.ConfiguredLabelValue{
				What:   fmt.Sprintf("mounts[%d] %s", i, side.what),
				Source: mountSource(snap, m), Value: side.v,
			})
		}
	}
	out = append(out,
		launch.ConfiguredLabelValue{What: "the project directory's path", Value: projectDir},
		launch.ConfiguredLabelValue{What: "CLAUDE_CONFIG_DIR", Source: "environment", Value: env.Getenv("CLAUDE_CONFIG_DIR")},
	)
	return out
}

// mountSource names the most-local config.yaml whose own mounts list holds
// m (mounts append across the cascade, so the key's last setter may not be
// it); "the sandbox config" when none parses to it.
func mountSource(snap []cascade.ConfigFile, m cascade.Mount) string {
	src := "the sandbox config"
	for _, f := range snap {
		one, err := cascade.LoadSnapshot([]cascade.ConfigFile{f})
		if err != nil {
			continue
		}
		for _, o := range one.Mounts {
			if o.Host == m.Host && o.Container == m.Container {
				src = f.Path
			}
		}
	}
	return src
}

// checkHomeMounts refuses a cascade mount whose container side is the
// container's home or an ancestor of it (CS-LNCH-241): the entrypoint
// relocates the image's build-time home there, so a read-only bind fails
// every move and a read-write one takes the files into the host's home. The
// container home is the launcher's HOME (hostIdentity), which the launch
// hands the entrypoint as HOST_HOME.
func checkHomeMounts(env *Env, cfg *cascade.Config, snap []cascade.ConfigFile) error {
	_, _, _, home := hostIdentity(env.Getenv)
	for i, m := range cfg.Mounts {
		if !launch.CoversHome(m.Container, home) {
			continue
		}
		return exitErr(2, "Error: mounts[%d] (%s) mounts over the container's home (container: %s; the home is %s).\n"+
			"       The entrypoint moves the image's home files there, so a mount at or above it breaks the session.\n"+
			"       For a view of the home, mount it under another container path, e.g. \"container: /mnt/host-home\".",
			i, mountSource(snap, m), m.Container, home)
	}
	return nil
}
