package cascade

// Env override notice. Spec: spec/config-cascade.feature (CS-CASC-021..029).
//
// Env files stack as root-first `docker run --env-file` flags, so the most
// local file defining a key wins (CS-CASC-010). The precedence is right but
// was silent: a stale project env shadowed every upstream refresh of a token.
// This names the shadowed keys at launch. Env files hold secrets, so only key
// NAMES are ever reported — values (from files or the launcher's environment)
// never enter the result.

import (
	"fmt"
	"io"
	"strings"
)

// LookupEnv reports whether the launcher's environment sets a key, as
// os.LookupEnv does. docker resolves a bare KEY line against it.
type LookupEnv func(key string) (string, bool)

// EnvOverrideGroup is keys that one winning file uses to override the same
// set of upstream files.
type EnvOverrideGroup struct {
	Keys []string // in the winner's file order
	// Overridden lists the upstream files that define every one of Keys,
	// nearest-first.
	Overridden []string
}

// EnvOverride is everything one env file wins over more-upstream files.
type EnvOverride struct {
	Winner string // the most-local file defining the keys (the one docker uses)
	Groups []EnvOverrideGroup
}

// Line renders the override as the launcher prints it: one line per winning
// file, one "; "-separated segment per distinct overridden-file set.
func (o EnvOverride) Line() string {
	segs := make([]string, len(o.Groups))
	for i, g := range o.Groups {
		segs[i] = fmt.Sprintf("%s in %s overrides %s",
			strings.Join(g.Keys, ", "), o.Winner, strings.Join(g.Overridden, ", "))
	}
	return "Env override: " + strings.Join(segs, "; ")
}

// EnvOverrides reports, for a root-first cascade of env files, every key
// defined in more than one file, grouped by the most-local file defining it.
// Results are most-local winner first. A bare KEY line defines KEY only when
// lookup finds it (nil lookup: never). Unreadable files and keys docker
// rejects are skipped; a key defined twice in one file is not a cross-file
// override.
func EnvOverrides(files []string, lookup LookupEnv) []EnvOverride {
	// defs[key] = indexes (root-first, deduped) of files defining key.
	defs := map[string][]int{}
	order := make([][]string, len(files)) // keys per file, file order, deduped
	for i, f := range files {
		assigns, err := readEnvAssignments(f)
		if err != nil {
			continue
		}
		for _, a := range assigns {
			if !validEnvKey(a.Key) {
				continue
			}
			if !a.HasValue {
				if lookup == nil {
					continue
				}
				if _, set := lookup(a.Key); !set {
					continue // docker drops an unresolvable bare key
				}
			}
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
		o := EnvOverride{Winner: files[i]}
		groupOf := map[string]int{} // overridden-set signature -> group index
		for _, k := range order[i] {
			d := defs[k]
			if len(d) < 2 || d[len(d)-1] != i {
				continue // single definition, or a more-local file wins
			}
			var overridden []string
			for j := len(d) - 2; j >= 0; j-- {
				overridden = append(overridden, files[d[j]])
			}
			sig := strings.Join(overridden, "\x00")
			gi, ok := groupOf[sig]
			if !ok {
				gi = len(o.Groups)
				groupOf[sig] = gi
				o.Groups = append(o.Groups, EnvOverrideGroup{Overridden: overridden})
			}
			o.Groups[gi].Keys = append(o.Groups[gi].Keys, k)
		}
		if len(o.Groups) > 0 {
			out = append(out, o)
		}
	}
	return out
}

// PrintEnvOverrides prints one line per winning env file (CS-CASC-021..029).
// Prints nothing when no key is defined in two files.
func PrintEnvOverrides(w io.Writer, files []string, lookup LookupEnv) {
	for _, o := range EnvOverrides(files, lookup) {
		fmt.Fprintln(w, o.Line())
	}
}
