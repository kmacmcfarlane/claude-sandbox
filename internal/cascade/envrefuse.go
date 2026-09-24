package cascade

// Refused env keys. Spec: spec/config-cascade.feature CS-CASC-042..045,
// spec/launch.feature CS-LNCH-129..131.
//
// Every --env-file line becomes the environment of the container's first
// process, entrypoint.sh, which runs as ROOT (CS-IMG-067). That script drops
// LD_PRELOAD and friends on its first lines, but glibc's dynamic loader and
// bash act on a fixed set of variables BEFORE the script's first line runs —
// the loader maps LD_PRELOAD/LD_AUDIT objects and reads LD_LIBRARY_PATH,
// GCONV_PATH, LOCPATH and GLIBC_TUNABLES into the entrypoint's own bash; bash
// sources BASH_ENV — so the script's own unset cannot undo a .so already
// mapped into it. Env files are session-writable (the project tree is mounted
// read-write), so an in-sandbox agent could otherwise plant one of these keys
// and have code run as root on the next launch.
//
// The launcher REFUSES a launch whose cascade env files define any such key,
// rather than stripping them into a filtered copy: a rewrite would silently
// drop a line the operator can no longer see, and nothing in an env file
// legitimately needs these keys (a session sets LD_LIBRARY_PATH in its own
// shell rc; an image sets it with ENV in the child Dockerfile). Detection uses
// the same docker-faithful reader as the linter and the override notice
// (readEnvAssignments), so a BOM, indentation or a CRLF ending cannot hide a
// line docker would honour.

import "strings"

// refusedExactKeys are the non-LD_ variables a root-run bash or glibc acts on
// before the entrypoint fixes the environment. Each justified in CS-CASC-042:
//   - GLIBC_TUNABLES — parsed by the loader before main (CVE-2023-4911).
//   - GCONV_PATH — libc loads charset-conversion .so modules from it lazily.
//   - LOCPATH — libc reads locale data from it, as root.
//   - BASH_ENV — sourced by any non-privileged bash a root-run tool spawns
//     (the entrypoint's own bash -p ignores it, but tools it runs need not).
//
// Deliberately NOT refused: ENV (bash -p ignores it and sh reads it only when
// interactive, while ENV=... is a common dotenv key); MALLOC_* (allocator
// diagnostics load no code and write no file); PATH (the entrypoint fixes it,
// CS-IMG-067).
var refusedExactKeys = map[string]bool{
	"GLIBC_TUNABLES": true,
	"GCONV_PATH":     true,
	"LOCPATH":        true,
	"BASH_ENV":       true,
}

// IsRefusedEnvKey reports whether key is one the dynamic loader or a root-run
// bash acts on before the entrypoint can neutralise it (CS-CASC-042). Every
// LD_* name is a loader control, so the whole prefix is refused (the prefix is
// anchored at the start and case-sensitive, as the loader is): "LD_PRELOAD",
// "LD_AUDIT", "LD_LIBRARY_PATH", "LD_DEBUG_OUTPUT", "LD_PROFILE" and any future
// LD_ name. "LD" alone and "MY_LD_PRELOAD" are not loader controls.
func IsRefusedEnvKey(key string) bool {
	if strings.HasPrefix(key, "LD_") {
		return true
	}
	return refusedExactKeys[key]
}

// EnvRefusal is one refused key located in an env file.
type EnvRefusal struct {
	File string
	Line int // 1-based, counting every line
	Key  string
}

// RefusedEnvKeys reports, for a root-first cascade of env files, every line
// that defines a key IsRefusedEnvKey rejects, in cascade order then file order
// (CS-CASC-045). It reads files with readEnvAssignments, so a BOM, indentation
// and one trailing '\r' are handled as docker handles them (CS-CASC-043).
//
// Unlike EnvFilesDefine, a BARE refused key (no '=') is always a finding
// (CS-CASC-044): docker would pass it through from the launcher's own
// environment, its effect then depends on the host at each launch, and it has
// no legitimate use inside the container — so it is refused whether or not the
// host sets it. This is the one place detection does not follow docker's
// "a bare key defines nothing unless the host sets it" rule, on purpose: fail
// closed. Keys docker itself rejects (empty, or containing a blank) never
// match. Unreadable files yield no finding — the launch fails on them
// elsewhere.
func RefusedEnvKeys(files []string) []EnvRefusal {
	var out []EnvRefusal
	for _, f := range files {
		assigns, err := readEnvAssignments(f)
		if err != nil {
			continue
		}
		for _, a := range assigns {
			if !validEnvKey(a.Key) {
				continue
			}
			if IsRefusedEnvKey(a.Key) {
				out = append(out, EnvRefusal{File: f, Line: a.Line, Key: a.Key})
			}
		}
	}
	return out
}
