package cascade

// Env file linting. Spec: spec/config-cascade.feature (CS-CASC-013..020).
// readEnvAssignments is also the reader behind the override notice
// (envoverride.go, CS-CASC-021..029).
//
// `docker run --env-file` performs NO quote stripping and no variable
// expansion: every character after '=' is part of the value. Most other
// env-file loaders (compose env_file, direnv, python-dotenv, shell `source`)
// DO strip matching quotes, so quoting a secret is a habit that works
// everywhere else and fails silently here — presence checks pass, the length
// looks plausible, and the service answers with a misleading 403/404.
//
// Warn instead of rewriting, so Docker's semantics stay intact for anyone
// relying on literal quotes.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// EnvWarningKind identifies what the linter found.
type EnvWarningKind string

const (
	// EnvWarningQuoted: the value is wrapped in matching quotes.
	EnvWarningQuoted EnvWarningKind = "quoted"
)

// EnvWarning is one lint finding, located in the file.
type EnvWarning struct {
	File string
	Line int // 1-based, counting every line including comments and blanks
	Key  string
	Kind EnvWarningKind
	// Quote is the offending quote character, for EnvWarningQuoted.
	Quote byte
}

// Lines renders the warning as the launcher prints it to stderr.
func (w EnvWarning) Lines() []string {
	switch w.Kind {
	case EnvWarningQuoted:
		return []string{
			fmt.Sprintf("WARNING: %s:%d: value for %s is wrapped in %c quotes.", w.File, w.Line, w.Key, w.Quote),
			"         docker --env-file does not strip quotes, so the quotes become part of",
			"         the value (a secret will fail auth while still looking set). Remove them.",
		}
	}
	return nil
}

// LintEnvFile reports quote-wrapped values in one env file, in file order.
// CRLF line endings are not a finding: the reader drops one trailing '\r' per
// line as docker does, so a quoted value in a CRLF file is still seen with the
// quotes as its first and last characters (CS-CASC-016/017).
func LintEnvFile(path string) ([]EnvWarning, error) {
	assigns, err := readEnvAssignments(path)
	if err != nil {
		return nil, err
	}
	var warnings []EnvWarning
	for _, a := range assigns {
		if !a.HasValue {
			continue // bare KEY: no value in the file to lint
		}
		value := a.Value
		if len(value) < 2 {
			continue // a lone quote cannot be a matching pair
		}
		if first, last := value[0], value[len(value)-1]; first == last && (first == '"' || first == '\'') {
			warnings = append(warnings, EnvWarning{
				File: path, Line: a.Line, Key: a.Key, Kind: EnvWarningQuoted, Quote: first,
			})
		}
	}
	return warnings, nil
}

// envAssignment is one KEY=VALUE or bare KEY line of an env file, value as
// docker passes it (one trailing '\r' already dropped from the line).
type envAssignment struct {
	Line  int // 1-based, counting every line including comments and blanks
	Key   string
	Value string
	// HasValue is false for a bare KEY line: docker's pass-through of the
	// launcher's own environment (set only when that environment has KEY).
	HasValue bool
}

// utf8BOM is dropped from the first line, as docker does.
const utf8BOM = "\xEF\xBB\xBF"

// readEnvAssignments is the single env-file reader shared by the linter and
// the override notice. It follows docker's --env-file parsing: a UTF-8 BOM
// on the first line is dropped, leading whitespace is trimmed, blank and '#'
// comment lines are skipped (but still counted), and the key runs to the
// first '='. Before any of that, exactly ONE trailing '\r' is dropped from
// every line, as docker's line scanner (bufio.ScanLines) does, so CRLF files
// read like LF ones (CS-CASC-016): "KEY=x\r" is value x, and a bare "KEY\r"
// is key KEY, resolved against the launcher's environment (CS-CASC-027). A
// second '\r' survives, as in docker: "KEY\r\r" is key "KEY\r", which
// docker cannot resolve (CS-CASC-028). It does not reject keys docker would
// (empty, containing blanks); callers that care filter with validEnvKey.
func readEnvAssignments(path string) ([]envAssignment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Split on '\n' and drop one '\r' per line below: the same result as
	// bufio.ScanLines, without its 64 KiB line limit.
	lines := strings.Split(string(raw), "\n")
	// A trailing newline yields a final empty element that is not a real line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var out []envAssignment
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r") // exactly one, as docker drops it
		if i == 0 {
			line = strings.TrimPrefix(line, utf8BOM)
		}
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		out = append(out, envAssignment{Line: i + 1, Key: key, Value: value, HasValue: ok})
	}
	return out, nil
}

// validEnvKey reports whether docker accepts key as a variable name: non-empty
// and free of blanks (docker fails the whole run otherwise).
func validEnvKey(key string) bool {
	return key != "" && !strings.ContainsAny(key, " \t")
}

// EnvFilesDefine reports whether docker, given files as --env-file flags, would
// set key in the container (CS-LNCH-108). Files are read by readEnvAssignments,
// so a BOM, leading whitespace and one trailing '\r' are handled as docker
// handles them. A bare KEY line counts only when lookup finds KEY in the
// launcher's environment (nil lookup: never), because docker passes that value
// through and drops the line otherwise — the override notice's rule
// (CS-CASC-027/028). Keys docker rejects never match; unreadable files are
// skipped.
func EnvFilesDefine(files []string, key string, lookup LookupEnv) bool {
	if !validEnvKey(key) {
		return false
	}
	for _, f := range files {
		assigns, err := readEnvAssignments(f)
		if err != nil {
			continue
		}
		for _, a := range assigns {
			if a.Key != key {
				continue
			}
			if a.HasValue {
				return true
			}
			if lookup != nil {
				if _, set := lookup(key); set {
					return true
				}
			}
		}
	}
	return false
}

// LintEnvFiles lints every file in the cascade and prints the findings.
// Warn-only: unreadable files and findings alike never block the launch.
func LintEnvFiles(w io.Writer, files []string) {
	for _, f := range files {
		warnings, err := LintEnvFile(f)
		if err != nil {
			continue
		}
		for _, warning := range warnings {
			for _, l := range warning.Lines() {
				fmt.Fprintln(w, l)
			}
		}
	}
}
