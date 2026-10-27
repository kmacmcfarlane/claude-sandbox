package imagebuild

// The Dockerfile frontend check (CS-IMG-079). CS-IMG-078 keeps env files
// out of a child build context only through the generated
// Dockerfile.dockerignore beside the private Dockerfile, and only a frontend
// that reads a Dockerfile-specific ignore file honours it. The frontend is
// whatever the Dockerfile's "syntax" parser directive names; docker/dockerfile
// 1.0 predates Dockerfile-specific ignore files and sends the whole context
// (verified in real builds: the 1.0, 1.0-experimental and 1.0.0-experimental
// pins send .claude-sandbox/env, 1 and 1.1+ do not).

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// directiveRes match one parser directive line: "#", optional non-line-breaking
// white space, a key, "=", the value (white space around both trimmed).
// Leading white space before "#" is accepted too: detecting more directives
// can only refuse more, never send more.
var directiveRes = []*regexp.Regexp{
	regexp.MustCompile(`^[ \t]*#[ \t]*([a-zA-Z][a-zA-Z0-9]*)[ \t]*=[ \t]*(.+?)[ \t]*$`),
	regexp.MustCompile(`^[ \t]*//[ \t]*([a-zA-Z][a-zA-Z0-9]*)[ \t]*=[ \t]*(.+?)[ \t]*$`),
}

// knownDirectives are the parser directives BuildKit recognises. An unknown
// key ends the directive block: BuildKit treats it as a comment.
var knownDirectives = map[string]bool{"syntax": true, "escape": true, "check": true}

// SyntaxDirective returns the value of a Dockerfile's "syntax" parser
// directive, "" when it has none. It follows the Dockerfile reference's
// "Parser directives" rules: directives are comments of the form
// "# directive=value" at the very top; keys are not case-sensitive; non
// line-breaking white space is permitted around the key, the "=" and the
// value; once a comment, an empty line or an instruction has been processed
// no further line is a directive, and an unrecognised key is such a comment.
// A single directive may be used only once, so the first syntax line counts.
// It also skips a UTF-8 BOM and a "#!" first line and accepts "//"-prefixed
// directives, forms BuildKit's syntax detection handles too; reading more
// lines as directives can only refuse more builds, never send an env file.
func SyntaxDirective(content []byte) string {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		lines = lines[1:]
	}
	for _, re := range directiveRes {
		for _, line := range lines {
			m := re.FindStringSubmatch(line)
			if m == nil || !knownDirectives[strings.ToLower(m[1])] {
				break
			}
			if strings.ToLower(m[1]) == "syntax" {
				return m[2]
			}
		}
	}
	return ""
}

// frontendKind classifies a syntax directive's frontend image.
type frontendKind int

const (
	// frontendReadsIgnore: no directive (the builder's own frontend), or
	// docker/dockerfile at a version that reads Dockerfile.dockerignore.
	frontendReadsIgnore frontendKind = iota
	// frontendOld: docker/dockerfile 1.0, which does not.
	frontendOld
	// frontendOther: another image, or docker/dockerfile pinned by digest
	// alone; whether it reads the file is unknown.
	frontendOther
)

// classifyFrontend names the frontend a syntax directive selects. The
// docker.io registry and the docker/ namespace may be written out
// ("docker.io/docker/dockerfile", "index.docker.io/docker/dockerfile").
func classifyFrontend(directive string) frontendKind {
	if directive == "" {
		return frontendReadsIgnore
	}
	ref := directive
	for _, reg := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, reg)
	}
	const name = "docker/dockerfile"
	if !strings.HasPrefix(ref, name) {
		return frontendOther
	}
	rest := ref[len(name):]
	digest := ""
	if i := strings.Index(rest, "@"); i >= 0 {
		rest, digest = rest[:i], rest[i:]
	}
	switch {
	case rest == "":
		if digest != "" {
			return frontendOther // a digest alone names no version
		}
		return frontendReadsIgnore // no tag: latest
	case !strings.HasPrefix(rest, ":"):
		return frontendOther // docker/dockerfile-upstream and the like
	}
	tag := rest[1:]
	if tag == "1.0" || strings.HasPrefix(tag, "1.0.") || strings.HasPrefix(tag, "1.0-") {
		return frontendOld
	}
	return frontendReadsIgnore
}

// envWalkMax bounds the context walk for env files: past it the context is
// taken to hold one (fail closed for the old frontend).
var envWalkMax = 200000

// errWalkLimit stops the walk at envWalkMax.
var errWalkLimit = errors.New("walk limit")

// envFileInContext returns an env file the launch's rules would exclude from
// the context (CS-IMG-078) that exists there: any .claude-sandbox/env below
// the context, its root "env" when the context is a .claude-sandbox
// directory, or a cascade env file under it. The walk is over the resolved
// context, as BuildKit sends it, never following a symlink; unreadable
// directories are skipped. limited is true when the walk stopped at
// envWalkMax without finding one.
func envFileInContext(context string, envFiles []string) (found string, limited bool) {
	ctxs := contextForms(context)
	cascade := map[string]bool{}
	for _, rel := range cascadeRels(ctxs, envFiles) {
		cascade[rel] = true
	}
	sbx := false
	for _, c := range ctxs {
		if filepath.Base(c) == paths.SandboxDir("") {
			sbx = true
		}
	}
	root := ctxs[len(ctxs)-1] // the resolved form when it differs
	count := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		count++
		if count > envWalkMax {
			return errWalkLimit
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		parts := strings.Split(rel, "/")
		n := len(parts)
		envAt := parts[n-1] == paths.Env &&
			(n >= 2 && parts[n-2] == paths.SandboxDir("") || n == 1 && sbx)
		if envAt || cascade[rel] {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, errWalkLimit) {
		return "", true
	}
	return found, false
}

// checkFrontend runs CS-IMG-079 before a child build: with an env file in
// the context, an old docker/dockerfile pin is refused (exit 2) and any
// other frontend gets one warning; without one nothing is printed. The
// frontend itself is never changed.
func checkFrontend(o Options, spec ChildSpec) error {
	directive := SyntaxDirective(spec.Content)
	kind := classifyFrontend(directive)
	if kind == frontendReadsIgnore {
		return nil
	}
	found, limited := envFileInContext(spec.Context, o.EnvFiles)
	if found == "" && !limited {
		return nil
	}
	shown := registry.Printable(directive)
	what := found
	if limited {
		what = fmt.Sprintf("any .claude-sandbox/env (the context has more than %d entries to check)", envWalkMax)
	}
	if kind == frontendOther {
		fmt.Fprintf(o.Err, "WARNING: %s selects the frontend %q (# syntax=); .claude-sandbox/env files are kept out of the build context only if that frontend honours Dockerfile.dockerignore (context %s holds %s).\n",
			spec.Dockerfile, shown, spec.Context, what)
		return nil
	}
	return &execx.CodeError{Code: 2, Msg: fmt.Sprintf(
		"Error: %s pins the Dockerfile frontend %q (# syntax=), which does not read Dockerfile.dockerignore,\n"+
			"  so the build would send %s from the build context %s to BuildKit. Not building.\n"+
			"  Fix one of:\n"+
			"    - raise the pin to docker/dockerfile:1.1 or later, or drop the syntax line;\n"+
			"    - build elsewhere: baseOnly: true, or a dockerfileDir whose build context holds no env file.",
		spec.Dockerfile, shown, what, spec.Context)}
}

// contextForms is the context made absolute, then its symlink-resolved form
// when that differs.
func contextForms(context string) []string {
	if abs, err := filepath.Abs(context); err == nil {
		context = abs
	}
	ctxs := []string{filepath.Clean(context)}
	if r, err := filepath.EvalSymlinks(context); err == nil && r != ctxs[0] {
		ctxs = append(ctxs, r)
	}
	return ctxs
}

// cascadeRels are the slash-separated paths, relative to a context form, of
// every cascade env file under it, lexically or with either side resolved.
func cascadeRels(ctxs, envFiles []string) []string {
	var out []string
	for _, e := range envFiles {
		if abs, err := filepath.Abs(e); err == nil {
			e = abs
		}
		forms := []string{filepath.Clean(e)}
		if r, err := filepath.EvalSymlinks(e); err == nil && r != forms[0] {
			forms = append(forms, r)
		}
		for _, c := range ctxs {
			for _, f := range forms {
				rel, err := filepath.Rel(c, f)
				if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
					continue
				}
				out = append(out, filepath.ToSlash(rel))
			}
		}
	}
	return out
}
