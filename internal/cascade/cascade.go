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
	PrintReportChain(w, paths.Chain(project, ""))
}

// PrintReportChain is PrintReport over an explicit search chain (most-local
// first, paths.Chain) — a linked worktree's includes its main checkout
// (CS-CASC-032).
func PrintReportChain(w io.Writer, chain []string) {
	type line struct {
		dir   string
		files []string
	}
	var lines []line
	for _, lvl := range paths.SandboxLevelsChain(chain) {
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
	// PackageCaches keeps go/npm/pip downloads on the host under
	// ~/.cache/claude-sandbox (CS-LNCH-035..037).
	PackageCaches HostAccessEntry `yaml:"packageCaches"`
}

// Config is the effective merged configuration.
type Config struct {
	Model              string `yaml:"model"`
	MemoryLimit        string `yaml:"memoryLimit"`
	DisableUpdateCheck bool   `yaml:"disableUpdateCheck"`
	// Dangerous passes --dangerously-skip-permissions to claude/ralph, like
	// the --dangerous flag or CLAUDE_SANDBOX_DANGEROUS=1 (CS-LNCH-038).
	Dangerous bool `yaml:"dangerous"`
	// Worktree turns claude's --worktree mode on or off for every launch
	// (CS-LNCH-041/042). A pointer, because the default differs by launch
	// kind (off interactive, on ralph) and an explicit value must be
	// distinguishable from unset. Excluded from the JSON form
	// the config-drift fingerprint hashes: it is a per-session choice, like
	// the model (CS-LNCH-044).
	Worktree *bool `yaml:"worktree" json:"-"`
	// SharedPeerRegistry bridges Claude Code's peer registry and messaging
	// sockets across containers whose CLAUDE_CONFIG_DIR differs
	// (CS-LNCH-049..055). Opt-in, default off: the config-dir split is
	// usually a deliberate work/personal boundary. A pointer, because
	// resolution is tri-state (CS-LNCH-052) — a falsy
	// CLAUDE_SANDBOX_SHARED_PEER_REGISTRY must be able to turn an upstream
	// "true" off for one session, which an OR shape cannot express.
	// json:"-" like Worktree, but for the opposite reason: the APPLIED
	// value (CS-LNCH-054/055) is hashed explicitly by the drift fingerprint, so hashing the
	// key here as well would only make an unset key and an explicit
	// "false" — which launch identically — look like drift against each other.
	SharedPeerRegistry *bool      `yaml:"sharedPeerRegistry" json:"-"`
	TrackInHost        *bool      `yaml:"trackInHost"`
	BaseOnly           bool       `yaml:"baseOnly"`
	DockerfileDir      string     `yaml:"dockerfileDir"`
	Dockerfile         string     `yaml:"dockerfile"`
	HostAccess         HostAccess `yaml:"hostAccess"`
	Mounts             []Mount    `yaml:"mounts"`

	// DetachKeys overrides the key sequence that detaches from an attached
	// session. Empty means the built-in default; see defaultDetachKeys.
	DetachKeys string `yaml:"detachKeys"`

	// OOMScoreAdj is the container's oom_score_adj (CS-LNCH-112), which makes
	// sandbox processes the kernel's preferred victims when the HOST runs out
	// of memory. A pointer, because an explicit 0 (docker's default) must be
	// distinguishable from unset (the launcher's default). json:"-" because
	// the APPLIED value is hashed explicitly by the drift fingerprint, so an
	// unset key and an explicit default hash alike.
	OOMScoreAdj *int `yaml:"oomScoreAdj" json:"-"`
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

// MemoryLimitSource returns the most-local config file that sets memoryLimit
// ("" when none does), for the OOM report and the container's labels
// (CS-CASC-036). files are root-first, as Load takes them. A file that sets
// the key to an empty value still counts — it is what the merge took — and
// the caller then reports the default.
func MemoryLimitSource(files []string) string {
	return KeySource(files, "memoryLimit")
}

// KeySource returns the most-local config file that sets the top-level key
// ("" when none does). The cascade keeps no per-key provenance, so the few
// callers that must name a key's file (memoryLimit's OOM report, CS-CASC-036;
// an invalid oomScoreAdj, CS-LNCH-112) re-read the files for that one key.
func KeySource(files []string, key string) string {
	src := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc map[string]any
		if yaml.Unmarshal(raw, &doc) != nil {
			continue
		}
		if _, ok := doc[key]; ok {
			src = f
		}
	}
	return src
}
