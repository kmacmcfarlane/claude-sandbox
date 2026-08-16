// Package cascade merges the .claude-sandbox/config.yaml cascade and resolves
// cascade-wide values. Merge semantics (spec/config-cascade.feature, CS-CASC):
// files are merged root-first; scalars and maps from more-local files win
// key-by-key; arrays append; `mounts` entries with the same host+container are
// overridden by the most local definition instead of duplicated.
package cascade

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
)

// PrintReport prints the cascade report (root → project; later overrides
// earlier) listing each contributing .claude-sandbox/ level and its files.
// Prints nothing when no level contributes. Used by both launch and init
// (CS-LNCH-024, CS-INIT-019).
func PrintReport(w io.Writer, project string) {
	type line struct {
		dir   string
		files []string
	}
	var lines []line
	for _, lvl := range paths.SandboxLevels(project) {
		sb := paths.SandboxDir(lvl)
		var has []string
		if fileExists(filepath.Join(sb, "config.yaml")) {
			has = append(has, "config.yaml")
		}
		if fileExists(filepath.Join(sb, "env")) {
			has = append(has, "env")
		}
		if fileExists(filepath.Join(sb, "Dockerfile")) {
			has = append(has, "Dockerfile (nearest wins)")
		}
		if len(has) > 0 {
			lines = append(lines, line{dir: sb, files: has})
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "Sandbox config cascade (root → project; later overrides earlier):")
	for _, l := range lines {
		fmt.Fprintf(w, "  %s/  →  ", l.dir)
		for i, f := range l.files {
			if i > 0 {
				fmt.Fprint(w, " ")
			}
			fmt.Fprint(w, f)
		}
		fmt.Fprintln(w)
	}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Mount is one extra volume mount.
type Mount struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
	Writable  bool   `yaml:"writable"`
}

// HostAccessEntry toggles one host resource.
type HostAccessEntry struct {
	Enabled *bool `yaml:"enabled"`
}

// HostAccess groups the mountable host resources.
type HostAccess struct {
	SSH          HostAccessEntry `yaml:"ssh"`
	Git          HostAccessEntry `yaml:"git"`
	DockerSocket HostAccessEntry `yaml:"dockerSocket"`
	AWS          HostAccessEntry `yaml:"aws"`
}

// Config is the effective merged configuration.
type Config struct {
	Model              string     `yaml:"model"`
	MemoryLimit        string     `yaml:"memoryLimit"`
	DisableUpdateCheck bool       `yaml:"disableUpdateCheck"`
	TrackInHost        *bool      `yaml:"trackInHost"`
	BaseOnly           bool       `yaml:"baseOnly"`
	DockerfileDir      string     `yaml:"dockerfileDir"`
	Dockerfile         string     `yaml:"dockerfile"`
	HostAccess         HostAccess `yaml:"hostAccess"`
	Mounts             []Mount    `yaml:"mounts"`

	// DetachKeys overrides the key sequence that detaches from an attached
	// session. Empty means the built-in default; see defaultDetachKeys.
	DetachKeys string `yaml:"detachKeys"`
}

// Load parses and deep-merges the config files (root-first order, as returned
// by paths.CollectUp). A nil/empty file list yields a zero Config.
func Load(files []string) (*Config, error) {
	merged := map[string]any{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("cascade: reading %s: %w", f, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("cascade: parsing %s: %w", f, err)
		}
		if doc == nil {
			continue // fully-commented sparse file
		}
		merged = mergeMaps(merged, doc)
	}
	if mounts, ok := merged["mounts"].([]any); ok {
		merged["mounts"] = dedupeMounts(mounts)
	}
	out, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(out, cfg); err != nil {
		return nil, fmt.Errorf("cascade: invalid merged config (%v): %w", files, err)
	}
	return cfg, nil
}

// mergeMaps merges src over dst: maps recurse, slices append, scalars replace.
func mergeMaps(dst, src map[string]any) map[string]any {
	for k, sv := range src {
		dv, exists := dst[k]
		if !exists {
			dst[k] = sv
			continue
		}
		switch svt := sv.(type) {
		case map[string]any:
			if dvt, ok := dv.(map[string]any); ok {
				dst[k] = mergeMaps(dvt, svt)
				continue
			}
		case []any:
			if dvt, ok := dv.([]any); ok {
				dst[k] = append(dvt, svt...)
				continue
			}
		}
		dst[k] = sv
	}
	return dst
}

// dedupeMounts keeps, for each host+container pair, only the LAST (most
// local) entry, at its last position — matching the yq
// `reverse | unique_by(host+"|"+container) | reverse` pipeline.
func dedupeMounts(mounts []any) []any {
	type key struct{ host, container string }
	keyOf := func(m any) key {
		mm, _ := m.(map[string]any)
		h, _ := mm["host"].(string)
		c, _ := mm["container"].(string)
		return key{h, c}
	}
	seen := map[key]bool{}
	var revKept []any
	for i := len(mounts) - 1; i >= 0; i-- {
		k := keyOf(mounts[i])
		if seen[k] {
			continue
		}
		seen[k] = true
		revKept = append(revKept, mounts[i])
	}
	out := make([]any, 0, len(revKept))
	for i := len(revKept) - 1; i >= 0; i-- {
		out = append(out, revKept[i])
	}
	return out
}

// Validate checks structural requirements of the merged config.
func (c *Config) Validate(files []string) error {
	for i, m := range c.Mounts {
		if m.Host == "" || m.Container == "" {
			return fmt.Errorf("mounts[%d] in the merged sandbox config requires both 'host' and 'container' fields (cascade: %v)", i, files)
		}
	}
	return nil
}

var trackRe = regexp.MustCompile(`(?m)^[ \t]*trackInHost:[ \t]*(true|false)([ \t].*)?$`)

// TrackInHost resolves the cascade-wide trackInHost with a line scan (not a
// YAML parse) so it works on files in any state: the most-local file with an
// explicit uncommented setting wins; default false. files are root-first.
func TrackInHost(files []string) bool {
	val := false
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		ms := trackRe.FindAllStringSubmatch(string(raw), -1)
		if len(ms) > 0 {
			val = ms[len(ms)-1][1] == "true"
		}
	}
	return val
}

// TrackInHostExplicit reports whether any of the files explicitly sets
// trackInHost, and the resolved value when so.
func TrackInHostExplicit(files []string) (value, isSet bool) {
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		ms := trackRe.FindAllStringSubmatch(string(raw), -1)
		if len(ms) > 0 {
			isSet = true
			value = ms[len(ms)-1][1] == "true"
		}
	}
	return value, isSet
}

// TrackInHostSource returns the most-local file that explicitly sets
// trackInHost ("" when none). files are root-first.
func TrackInHostSource(files []string) string {
	src := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if trackRe.MatchString(string(raw)) {
			src = f
		}
	}
	return src
}
