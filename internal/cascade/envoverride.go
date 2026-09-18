package cascade

// Env override notice. Spec: spec/config-cascade.feature (CS-CASC-021..025).
//
// Env files stack as root-first `docker run --env-file` flags, so the most
// local file defining a key wins (CS-CASC-010). The precedence is right but
// was silent: a stale project env shadowed every upstream refresh of a token.
// This names the shadowed keys at launch. Env files hold secrets, so only key
// NAMES are ever reported — values are never read into the result.

import (
	"fmt"
	"io"
	"strings"
)

// EnvOverride groups the keys one env file wins over more-upstream files.
type EnvOverride struct {
	Winner string   // the most-local file defining Keys (the one docker uses)
	Keys   []string // in the winner's file order
	// Overridden lists the upstream files that also define at least one of
	// Keys, nearest-first.
	Overridden []string
}

// Line renders the override as the launcher prints it.
func (o EnvOverride) Line() string {
	return fmt.Sprintf("Env override: %s in %s overrides %s",
		strings.Join(o.Keys, ", "), o.Winner, strings.Join(o.Overridden, ", "))
}

// EnvOverrides reports, for a root-first cascade of env files, every key
// defined in more than one file, grouped by the most-local file defining it.
// Results are most-local winner first. Unreadable files are skipped; a key
// assigned twice in one file is not a cross-file override.
func EnvOverrides(files []string) []EnvOverride {
	// defs[key] = indexes (root-first, deduped) of files assigning key.
	defs := map[string][]int{}
	order := make([][]string, len(files)) // keys per file, file order, deduped
	for i, f := range files {
		assigns, err := readEnvAssignments(f)
		if err != nil {
			continue
		}
		for _, a := range assigns {
			d := defs[a.Key]
			if len(d) > 0 && d[len(d)-1] == i {
				continue
			}
			defs[a.Key] = append(d, i)
			order[i] = append(order[i], a.Key)
		}
	}

	var out []EnvOverride
	for i := len(files) - 1; i >= 0; i-- {
		var keys []string
		shadowed := map[int]bool{}
		for _, k := range order[i] {
			d := defs[k]
			if len(d) < 2 || d[len(d)-1] != i {
				continue // single definition, or a more-local file wins
			}
			keys = append(keys, k)
			for _, j := range d[:len(d)-1] {
				shadowed[j] = true
			}
		}
		if len(keys) == 0 {
			continue
		}
		o := EnvOverride{Winner: files[i], Keys: keys}
		for j := i - 1; j >= 0; j-- {
			if shadowed[j] {
				o.Overridden = append(o.Overridden, files[j])
			}
		}
		out = append(out, o)
	}
	return out
}

// PrintEnvOverrides prints one line per winning env file (CS-CASC-021..025).
// Prints nothing when no key is defined in two files.
func PrintEnvOverrides(w io.Writer, files []string) {
	for _, o := range EnvOverrides(files) {
		fmt.Fprintln(w, o.Line())
	}
}
