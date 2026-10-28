package main

import (
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
	out = append(out,
		launch.ConfiguredLabelValue{What: "the project directory's path", Value: projectDir},
		launch.ConfiguredLabelValue{What: "CLAUDE_CONFIG_DIR", Source: "environment", Value: env.Getenv("CLAUDE_CONFIG_DIR")},
	)
	return out
}
