package cascade

// Env file linting. Spec: spec/config-cascade.feature (CS-CASC-013..020).
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
)

// EnvWarningKind identifies what the linter found.
type EnvWarningKind string

const (
	// EnvWarningQuoted: the value is wrapped in matching quotes.
	EnvWarningQuoted EnvWarningKind = "quoted"
	// EnvWarningCarriageReturn: the value carries a CRLF carriage return.
	EnvWarningCarriageReturn EnvWarningKind = "carriage-return"
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
	case EnvWarningCarriageReturn:
		return []string{
			fmt.Sprintf("WARNING: %s:%d: value for %s ends with a carriage return (CRLF line endings).", w.File, w.Line, w.Key),
			"         docker --env-file keeps it as part of the value. Convert the file to LF.",
		}
	case EnvWarningQuoted:
		return []string{
			fmt.Sprintf("WARNING: %s:%d: value for %s is wrapped in %c quotes.", w.File, w.Line, w.Key, w.Quote),
			"         docker --env-file does not strip quotes, so the quotes become part of",
			"         the value (a secret will fail auth while still looking set). Remove them.",
		}
	}
	return nil
}

// LintEnvFile reports quoting and CRLF problems in one env file. Findings are
// returned in file order; a line can produce both kinds (the carriage return
// is stripped before the quote check, so quotes are still seen as the first
// and last characters).
func LintEnvFile(path string) ([]EnvWarning, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Split manually rather than with bufio.Scanner: ScanLines strips a
	// trailing '\r', which is exactly what this linter needs to see.
	lines := strings.Split(string(raw), "\n")
	// A trailing newline yields a final empty element that is not a real line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	var warnings []EnvWarning
	for i, line := range lines {
		lineno := i + 1
		// Only KEY=VALUE assignments are linted; blanks, comments and
		// non-assignment lines are skipped (but still counted).
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.HasSuffix(value, "\r") {
			warnings = append(warnings, EnvWarning{
				File: path, Line: lineno, Key: key, Kind: EnvWarningCarriageReturn,
			})
			value = strings.TrimSuffix(value, "\r")
		}
		if len(value) < 2 {
			continue // a lone quote cannot be a matching pair
		}
		if first, last := value[0], value[len(value)-1]; first == last && (first == '"' || first == '\'') {
			warnings = append(warnings, EnvWarning{
				File: path, Line: lineno, Key: key, Kind: EnvWarningQuoted, Quote: first,
			})
		}
	}
	return warnings, nil
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
