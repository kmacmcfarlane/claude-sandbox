package imagebuild

// The Dockerfile frontend check (CS-IMG-079). CS-IMG-078 keeps env files
// out of a child build context only through the generated
// Dockerfile.dockerignore beside the private Dockerfile, and only a frontend
// that reads a Dockerfile-specific ignore file honours it. The frontend is
// whatever the Dockerfile's "syntax" parser directive names; docker/dockerfile
// 0.9 and 1.0 predate Dockerfile-specific ignore files and send the whole
// context (verified in real builds: the 0.9, 1.0, 1.0-experimental and
// 1.0.0-experimental pins send .claude-sandbox/env, 1 and 1.1+ do not).
//
// The reading follows BuildKit's parser.DetectSyntax (frontend/dockerfile/
// parser/directives.go; dockerfile2llb/directives.go before v0.11) and the
// gateway frontend's image resolution (frontend/gateway/gateway.go), reading
// wider wherever versions differ or the image a name reaches is uncertain.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// directiveRe is BuildKit's parser-directive pattern, applied after the
// comment marker is cut and the white space after it trimmed.
var directiveRe = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)

// directiveLine reads one line as a parser directive behind marker ("#" or
// "//"), as BuildKit does: cut the marker (blanks before it are tolerated —
// reading wider only refuses more), trim ANY white space after it
// (unicode.IsSpace), then directiveRe. ok is false for a line of another
// shape.
func directiveLine(line, marker string) (key, value string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimLeft(line, " \t"), marker)
	if !found {
		return "", "", false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	m := directiveRe.FindStringSubmatch(rest)
	if m == nil {
		return "", "", false
	}
	return strings.ToLower(m[1]), m[2], true
}

// SyntaxDirectives returns the frontend image of every "syntax" parser
// directive at the top of a Dockerfile, in order; none when it has none.
// It follows the Dockerfile reference's "Parser directives" rules —
// directives are comments "# directive=value" at the very top, keys are not
// case-sensitive, white space is permitted around the key, the "=" and the
// value, and the block ends at the first line of another shape (an
// instruction, an empty line, a plain comment) — but reads WIDER than any
// one BuildKit version, since reading more can only refuse more builds,
// never send an env file:
//   - it goes on past unknown "# key=value" keys (BuildKit before v0.11 did,
//     and there the LAST syntax line wins), and returns every syntax value,
//     so a caller can refuse when any of them is an old frontend;
//   - it skips a UTF-8 BOM and a "#!" first line, takes CRLF line ends, and
//     reads "//" directives too;
//   - it reads the JSON form BuildKit v0.11 and later fall back to when
//     neither comment form names a syntax (parser.DetectSyntax): a
//     Dockerfile that is one JSON object with a "syntax" string member.
//     Every member whose key equals "syntax" ignoring case counts (v0.11 to
//     v0.13 decode into a struct field, which matches keys case-insensitively;
//     later versions match the key exactly).
//
// Each value is cut at its first white space: BuildKit takes the frontend
// image only up to the first space, the rest being the frontend's command
// line.
func SyntaxDirectives(content []byte) []string {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		lines = lines[1:]
	}
	var out []string
	add := func(value string) {
		if f := strings.FieldsFunc(value, unicode.IsSpace); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	for _, marker := range []string{"#", "//"} {
		for _, line := range lines {
			key, value, ok := directiveLine(line, marker)
			if !ok {
				break
			}
			if key == "syntax" {
				add(value)
			}
		}
	}
	for _, v := range jsonSyntax(content) {
		add(v)
	}
	return out
}

// jsonSyntax returns the "syntax" string members of a Dockerfile that is one
// JSON object, BuildKit's last-resort directive form. BuildKit decodes the
// bytes after a "#!" first line (v0.11 to v0.13 without stripping a UTF-8
// BOM, later versions with; content arrives BOM-stripped, and a BOM fails
// to decode anyway), so the content is tried whole and without such a line.
// Keys match ignoring case, with the Unicode folding encoding/json's struct
// fields use; values in sorted key order.
func jsonSyntax(content []byte) []string {
	candidates := [][]byte{content}
	if first, rest, _ := bytes.Cut(content, []byte("\n")); bytes.HasPrefix(first, []byte("#!")) {
		candidates = append(candidates, rest)
	}
	var out []string
	for _, c := range candidates {
		var m map[string]json.RawMessage
		if json.Unmarshal(c, &m) != nil {
			continue
		}
		var keys []string
		for k := range m {
			if strings.EqualFold(k, "syntax") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			var v string
			if json.Unmarshal(m[k], &v) == nil {
				out = append(out, v)
			}
		}
	}
	return out
}

// frontendKind classifies a syntax directive's frontend image.
type frontendKind int

const (
	// frontendReadsIgnore: no directive (the builder's own frontend), or
	// Docker Hub's docker/dockerfile at a version that reads
	// Dockerfile.dockerignore.
	frontendReadsIgnore frontendKind = iota
	// frontendOld: docker/dockerfile 0.9 or 1.0, which does not.
	frontendOld
	// frontendOther: another image, docker/dockerfile pinned by any digest
	// not listed as old, or at a tag not known to read the file; whether it
	// reads the file is unknown.
	frontendOther
)

// hubRepos are the spellings of Docker Hub's docker/dockerfile and
// docker/dockerfile-upstream whose tags can be trusted to name what Docker
// publishes; the value is the repository. Any other registry or path is
// another frontend unless it names an old one.
var hubRepos = map[string]string{}

func init() {
	for _, repo := range []string{"docker/dockerfile", "docker/dockerfile-upstream"} {
		for _, reg := range []string{"", "docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
			hubRepos[reg+repo] = repo
		}
	}
}

// modernTagRe is the allowlist of docker/dockerfile tags that read
// Dockerfile.dockerignore (the 1.1 frontend added Dockerfile-specific
// ignore files; verified in real builds for 1, 1.1 and later). The shape:
// "1", or "1.N" / "1.N.x" with N >= 1, optionally followed by "-labs" or
// "-experimental"; the floating "latest", "labs" and "experimental". On
// Docker Hub (October 2026) these are 1, 1-labs, 1-experimental, 1.1 to
// 1.N with their .x patch, -labs and -experimental forms, latest and labs
// (both current 1.x), and experimental and 1-experimental (both last pushed
// in 2020, a 1.2-era image). Release candidates ("-rcN") and anything else
// are left out: they are unknown, so they warn.
var modernTagRe = regexp.MustCompile(`^(1(\.[1-9][0-9]*(\.[0-9]+)?)?(-(labs|experimental))?|latest|labs|experimental)$`)

// upstreamTagRe adds docker/dockerfile-upstream's floating master tags
// (master, master-labs and master-experimental, the last pushed in 2020
// as a 1.2-era image).
var upstreamTagRe = regexp.MustCompile(`^master(-(labs|experimental))?$`)

// isOldTag reports a tag of the 0.9 or 1.0 family, the docker/dockerfile
// frontends that predate Dockerfile-specific ignore files (verified in real
// builds: 0.9, 1.0, 1.0-experimental and 1.0.0-experimental send
// .claude-sandbox/env, 1 and 1.1 and later do not). Case is ignored, which
// only refuses more.
func isOldTag(tag string) bool {
	t := strings.ToLower(tag)
	for _, v := range []string{"0", "1.0"} {
		if t == v || strings.HasPrefix(t, v+".") || strings.HasPrefix(t, v+"-") {
			return true
		}
	}
	return false
}

// splitImageRef splits an image reference as the reference grammar does:
// the digest after the first "@" (its "sha256:" cut, lower-cased), the tag
// after the last ":" that follows the last "/".
func splitImageRef(ref string) (name, tag, digest string) {
	name, digest, _ = strings.Cut(ref, "@")
	digest = strings.TrimPrefix(strings.ToLower(digest), "sha256:")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	return name, tag, digest
}

// classifyFrontend names the frontend a syntax directive selects.
//
// Old: an image whose last path component is "dockerfile" or
// "dockerfile-upstream" (case ignored) under ANY registry and path — BuildKit
// pulls Docker Hub's image through spellings the reference library does not
// normalise (registry.hub.docker.com/…, registry-1.docker.io:443/…, both
// verified) and through pull-through mirrors (mirror.gcr.io/docker/…) — with
// a 0.9 or 1.0 tag, or with a digest of an old image whatever the tag: the
// digest decides what is pulled.
//
// Reads the ignore file (an allowlist): a hubRepos spelling with NO digest
// and either no tag (latest) or a tag modernTagRe (or, for
// docker/dockerfile-upstream, upstreamTagRe) accepts, exactly, in its case.
//
// Other: everything else. That includes any digest not listed as old, even
// beside a modern tag: the digest decides what is pulled, and
// oldFrontendDigests covers only the manifests Docker Hub tags today, so an
// old manifest that lost its tag cannot be told from a modern one. A
// digest-pinned docker/dockerfile therefore gets one WARNING per child build
// while an env file is in the context.
func classifyFrontend(directive string) frontendKind {
	if directive == "" {
		return frontendReadsIgnore
	}
	name, tag, digest := splitImageRef(directive)
	last := strings.ToLower(path.Base(name))
	if (last == "dockerfile" || last == "dockerfile-upstream") &&
		(isOldTag(tag) || oldFrontendDigests[digest]) {
		return frontendOld
	}
	repo, hub := hubRepos[name]
	switch {
	case !hub || digest != "" || strings.Contains(directive, "@") ||
		tag == "" && strings.HasSuffix(directive, ":"):
		return frontendOther
	case tag == "" || modernTagRe.MatchString(tag):
		return frontendReadsIgnore
	case repo == "docker/dockerfile-upstream" && upstreamTagRe.MatchString(tag):
		return frontendReadsIgnore
	}
	return frontendOther
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
	// Every syntax value counts (SyntaxDirectives over-reads): any old pin
	// refuses; otherwise any other frontend warns.
	kind, directive := frontendReadsIgnore, ""
	for _, d := range SyntaxDirectives(spec.Content) {
		switch k := classifyFrontend(d); {
		case k == frontendOld:
			kind, directive = k, d
		case k == frontendOther && kind == frontendReadsIgnore:
			kind, directive = k, d
		}
		if kind == frontendOld {
			break
		}
	}
	if kind == frontendReadsIgnore {
		return nil
	}
	found, limited := envFileInContext(spec.Context, o.EnvFiles)
	if found == "" && !limited {
		return nil
	}
	// Paths and the directive come from session-writable files and names:
	// printed through registry.Printable, never raw.
	shown := registry.Printable(directive)
	dockerfile := registry.Printable(spec.Dockerfile)
	context := registry.Printable(spec.Context)
	what := registry.Printable(found)
	if limited {
		what = fmt.Sprintf("any .claude-sandbox/env (the context has more than %d entries to check)", envWalkMax)
	}
	if kind == frontendOther {
		fmt.Fprintf(o.Err, "WARNING: %s selects the frontend %q (# syntax=); .claude-sandbox/env files are kept out of the build context only if that frontend honours Dockerfile.dockerignore (context %s holds %s).\n",
			dockerfile, shown, context, what)
		return nil
	}
	return &execx.CodeError{Code: 2, Msg: fmt.Sprintf(
		"Error: %s pins the Dockerfile frontend %q (# syntax=), which does not read Dockerfile.dockerignore,\n"+
			"  so the build would send %s from the build context %s to BuildKit. Not building.\n"+
			"  Fix one of:\n"+
			"    - raise the pin to docker/dockerfile:1.1 or later, or drop the syntax line;\n"+
			"    - build elsewhere: baseOnly: true, or a dockerfileDir whose build context holds no env file.",
		dockerfile, shown, what, context)}
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
