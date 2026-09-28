package globalcfg

// "claude-sandbox global-config accept" (CS-GCFG-054): record the current
// global config as the baseline snapshot. The launcher's health check
// (CS-GCFG-001..015, health.go) compares launches against the newest
// snapshot-* in the same store, through the same ResolveGlobalFile,
// OpenStore and Summarize.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// CustomOAuthEnv selects another global file name in Claude Code
// (".claude-custom-oauth.json"); out of scope for the store.
const CustomOAuthEnv = "CLAUDE_CODE_CUSTOM_OAUTH_URL"

// ResolveGlobalFile is the global file Claude Code uses, as it resolves it:
// <config dir>/.config.json when that exists, else
// $CLAUDE_CONFIG_DIR/.claude.json when CLAUDE_CONFIG_DIR is set, else
// $HOME/.claude.json. The path is lexical (the store key); reading it follows
// a link.
func ResolveGlobalFile(home string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if v := getenv(CustomOAuthEnv); v != "" {
		return "", fmt.Errorf("%s is set: Claude Code then uses another global file, which this command does not handle", CustomOAuthEnv)
	}
	cd := getenv("CLAUDE_CONFIG_DIR")
	if SuspiciousConfigDir(cd) {
		return "", fmt.Errorf("CLAUDE_CONFIG_DIR=%q is not a plain absolute path, so the file Claude Code uses depends on its working directory", cd)
	}
	cfgDir, base := ConfigDir(home), home
	if cd != "" {
		cfgDir, base = filepath.Clean(cd), filepath.Clean(cd)
	}
	if _, err := os.Lstat(filepath.Join(cfgDir, LegacyConfigName)); err == nil {
		return filepath.Join(cfgDir, LegacyConfigName), nil
	}
	return filepath.Join(filepath.Clean(base), FileName), nil
}

// Summary is what accept prints about a config: key names and counts only.
type Summary struct {
	Keys       []string
	Projects   int
	OAuth      bool
	Onboarding bool
}

// Summarize reads the key names and counts of a global config object.
func Summarize(data []byte) (Summary, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return Summary{}, errors.New("does not parse as a JSON object")
	}
	s := Summary{}
	for k := range m {
		s.Keys = append(s.Keys, k)
	}
	sort.Strings(s.Keys)
	var projects map[string]json.RawMessage
	if json.Unmarshal(m["projects"], &projects) == nil {
		s.Projects = len(projects)
	}
	if v, ok := m["oauthAccount"]; ok && strings.TrimSpace(string(v)) != "null" {
		s.OAuth = true
	}
	var onboarded bool
	if json.Unmarshal(m["hasCompletedOnboarding"], &onboarded) == nil {
		s.Onboarding = onboarded
	}
	return s, nil
}

// Describe renders cur against prev (nil: no earlier snapshot) as indented
// lines of key names and counts — never a value.
func (cur Summary) Describe(prev *Summary) string {
	present := func(b bool) string {
		if b {
			return "present"
		}
		return "absent"
	}
	var b strings.Builder
	if prev == nil {
		fmt.Fprintf(&b, "  (no earlier snapshot)\n")
		fmt.Fprintf(&b, "  keys: %d\n  projects: %d\n  oauthAccount: %s\n  hasCompletedOnboarding: %t\n",
			len(cur.Keys), cur.Projects, present(cur.OAuth), cur.Onboarding)
		return b.String()
	}
	old := map[string]bool{}
	for _, k := range prev.Keys {
		old[k] = true
	}
	now := map[string]bool{}
	var added, removed []string
	for _, k := range cur.Keys {
		now[k] = true
		if !old[k] {
			added = append(added, k)
		}
	}
	for _, k := range prev.Keys {
		if !now[k] {
			removed = append(removed, k)
		}
	}
	fmt.Fprintf(&b, "  keys: %d (was %d)", len(cur.Keys), len(prev.Keys))
	if len(added) > 0 {
		fmt.Fprintf(&b, "; added: %s", strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		fmt.Fprintf(&b, "; removed: %s", strings.Join(removed, ", "))
	}
	fmt.Fprintf(&b, "\n  projects: %d (was %d)\n", cur.Projects, prev.Projects)
	fmt.Fprintf(&b, "  oauthAccount: %s (was %s)\n", present(cur.OAuth), present(prev.OAuth))
	fmt.Fprintf(&b, "  hasCompletedOnboarding: %t (was %t)\n", cur.Onboarding, prev.Onboarding)
	return b.String()
}

// AcceptOptions are accept's inputs and seams.
type AcceptOptions struct {
	Home, StateRoot string
	Getenv          func(string) string
	InSandbox       bool
	Out             io.Writer
	Now             func() time.Time
	DirOps          *hostdirs.Ops
}

// Accept is "global-config accept" (CS-GCFG-054).
func Accept(o AcceptOptions) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.InSandbox {
		return refuse("refused inside a sandbox: $HOME and the state directory here are the container's own. Run it on the host.")
	}
	if o.Home == "" || !filepath.IsAbs(o.Home) {
		return refuse("HOME is not an absolute path (%q)", o.Home)
	}
	if testing.Testing() && isRealHome(o.Home) {
		panic(fmt.Sprintf("globalcfg: a test would read %s under the real home; use a scratch HOME", filepath.Join(o.Home, FileName)))
	}
	file, err := ResolveGlobalFile(o.Home, o.Getenv)
	if err != nil {
		return refuse("%v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return refuse("%s: %v", file, unwrapPath(err))
	}
	cur, err := Summarize(data)
	if err != nil {
		return refuse("%s %v; restore it before accepting it as the baseline (Claude Code keeps copies in its backups/ directory).", file, err)
	}
	store, err := OpenStore(o.StateRoot, file, o.DirOps)
	if err != nil {
		return refuse("the state directory: %v", err)
	}
	var prev *Summary
	if p := store.Newest(SnapshotPrefix); p != "" {
		if b, rerr := os.ReadFile(p); rerr == nil {
			if s, serr := Summarize(b); serr == nil {
				prev = &s
			}
		}
	}
	snap, err := store.Write(SnapshotPrefix, data, o.Now())
	if err != nil {
		return fmt.Errorf("writing the snapshot: %v", err)
	}
	store.Prune(SnapshotPrefix, KeepSnapshots)
	fmt.Fprintf(o.Out, "Accepted %s as the global-config baseline: %s\n", file, snap)
	fmt.Fprint(o.Out, cur.Describe(prev))
	return nil
}
