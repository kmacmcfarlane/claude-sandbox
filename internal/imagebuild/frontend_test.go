package imagebuild_test

// Spec: spec/image-build.feature CS-IMG-079 — the Dockerfile frontend check
// that guards CS-IMG-078's generated Dockerfile.dockerignore: the syntax
// parser directive is read as BuildKit reads it, an old docker/dockerfile
// pin is refused while an env file is in the context, any other frontend
// is warned about, and with no env file nothing happens.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

var _ = Describe("the syntax parser directive (CS-IMG-079)", func() {
	DescribeTable("CS-IMG-079: SyntaxDirectives reads the directive block as BuildKit does, over-reading where versions differ",
		func(content string, want ...string) {
			got := imagebuild.SyntaxDirectives([]byte(content))
			if len(want) == 0 {
				Expect(got).To(BeEmpty())
				return
			}
			Expect(got).To(Equal(want))
		},
		Entry("the usual form", "# syntax=docker/dockerfile:1.0\nFROM x\n", "docker/dockerfile:1.0"),
		Entry("no space", "#syntax=docker/dockerfile:1.0\nFROM x\n", "docker/dockerfile:1.0"),
		Entry("spaces around the key and the =", "# syntax = docker/dockerfile:1.0 \nFROM x\n", "docker/dockerfile:1.0"),
		Entry("a tab", "#\tsyntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("NBSP after the marker", "#\u00a0syntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("VT after the marker", "#\vsyntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("FF after the marker", "#\fsyntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("a trailing FF on the value", "# syntax=docker/dockerfile:1.0\f\n", "docker/dockerfile:1.0"),
		Entry("the value cut at its first space", "# syntax=docker/dockerfile:1.0 x\n", "docker/dockerfile:1.0"),
		Entry("the value cut before a trailing comment", "# syntax=docker/dockerfile:1.0-experimental # comment\n", "docker/dockerfile:1.0-experimental"),
		Entry("the key in any case", "#  SyNtAx=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("after another known directive", "# escape=`\n# syntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("after a check directive", "# check=skip=JSONArgsRecommended\n# syntax=a/b\n", "a/b"),
		Entry("CRLF line ends", "# syntax=docker/dockerfile:1.0\r\nFROM x\r\n", "docker/dockerfile:1.0"),
		Entry("a UTF-8 BOM", "\xef\xbb\xbf# syntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("a #! first line", "#!/usr/bin/env buildctl\n# syntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("a // directive", "//syntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("past an unknown key (BuildKit before v0.11)", "# author=me\n# syntax=docker/dockerfile:1.0\n", "docker/dockerfile:1.0"),
		Entry("every one of two", "# syntax=docker/dockerfile:1.1\n# syntax=docker/dockerfile:1.0\n",
			"docker/dockerfile:1.1", "docker/dockerfile:1.0"),
		Entry("none", "FROM x\n"),
		Entry("empty", ""),
		Entry("after an instruction: a comment", "FROM x\n# syntax=docker/dockerfile:1.0\n"),
		Entry("after a plain comment: a comment", "# my image\n# syntax=docker/dockerfile:1.0\n"),
		Entry("after an empty line: a comment", "\n# syntax=docker/dockerfile:1.0\n"),
		Entry("no value", "# syntax=\n"),
	)
})

var _ = Describe("the Dockerfile frontend check (CS-IMG-079)", func() {
	var (
		fake        *execx.Fake
		out, errw   *bytes.Buffer
		o           imagebuild.Options
		proj, df    string
		builds      int
		envFile     string
		dockerfileB string
	)

	BeforeEach(func() {
		fake = &execx.Fake{}
		fake.On("docker image inspect", "", execx.Fail(1)) // no image: build
		builds = 0
		fake.OnFunc("docker build -t ", func(execx.Cmd) (string, error) { builds++; return "", nil })
		out, errw = &bytes.Buffer{}, &bytes.Buffer{}
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(base, "proj")
		df = filepath.Join(proj, ".claude-sandbox", "Dockerfile")
		envFile = filepath.Join(proj, ".claude-sandbox", "env")
		scratch := filepath.Join(base, "scratch")
		Expect(os.MkdirAll(scratch, 0o755)).To(Succeed())
		o = imagebuild.Options{Runner: fake, Out: out, Err: errw, TempRoot: scratch}
		dockerfileB = "FROM claude-sandbox\nCOPY . /src\n"
	})

	write := func(p, s string) {
		Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
		Expect(os.WriteFile(p, []byte(s), 0o644)).To(Succeed())
	}
	// ensureContent builds a child from df holding content, with context.
	ensureContent := func(content, context string) error {
		write(df, content)
		spec := imagebuild.ChildSpec{Use: true, Dockerfile: df, Context: context, ImageName: "claude-sandbox-proj", Content: []byte(content)}
		_, _, err := imagebuild.EnsureChild(o, spec, false, false)
		return err
	}
	// ensure builds a child from df with the given directive line (if any)
	// and context.
	ensure := func(directive, context string) error {
		content := dockerfileB
		if directive != "" {
			content = "# syntax=" + directive + "\n" + content
		}
		return ensureContent(content, context)
	}
	refused := func(err error) {
		var ce *execx.CodeError
		Expect(errors.As(err, &ce)).To(BeTrue(), "an exit-coded error")
		Expect(ce.Code).To(Equal(2))
		Expect(builds).To(Equal(0), "nothing was built")
	}

	Context("with .claude-sandbox/env in the context: directive blocks BuildKit reads in ways the first reader missed", func() {
		BeforeEach(func() { write(envFile, "TOKEN=secret\n") })

		DescribeTable("CS-IMG-079: an old pin anywhere in the over-read block is refused",
			func(header string) {
				refused(ensureContent(header+dockerfileB, proj))
			},
			Entry("the value followed by frontend arguments", "# syntax=docker/dockerfile:1.0 x\n"),
			Entry("the value followed by a comment", "# syntax=docker/dockerfile:1.0-experimental # comment\n"),
			Entry("NBSP after the marker", "#\u00a0syntax=docker/dockerfile:1.0\n"),
			Entry("VT after the marker", "#\vsyntax=docker/dockerfile:1.0\n"),
			Entry("FF after the marker", "#\fsyntax=docker/dockerfile:1.0\n"),
			Entry("a trailing FF on the value", "# syntax=docker/dockerfile:1.0\f\n"),
			Entry("after an unknown key", "# author=me\n# syntax=docker/dockerfile:1.0\n"),
			Entry("the second of two syntax lines", "# syntax=docker/dockerfile:1.1\n# syntax=docker/dockerfile:1.0\n"),
			Entry("after another frontend's syntax line", "# syntax=registry.example.com/f:2\n# syntax=docker/dockerfile:1.0\n"),
		)

		It("CS-IMG-079: two syntax lines with no old pin but another frontend warn once", func() {
			Expect(ensureContent("# syntax=docker/dockerfile:1.1\n# syntax=registry.example.com/f:2\n"+dockerfileB, proj)).To(Succeed())
			Expect(builds).To(Equal(1))
			Expect(strings.Count(errw.String(), "WARNING:")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring(`"registry.example.com/f:2"`))
		})
	})

	Context("paths in the messages", func() {
		It("CS-IMG-079: a control character in the env file's path or the context's never reaches the terminal", func() {
			ctx := filepath.Join(proj, "ctx\x1b[31m")
			nested := filepath.Join(ctx, "sub\x1b]0;t\x07", ".claude-sandbox", "env")
			write(nested, "TOKEN=x\n")
			err := ensureContent("# syntax=docker/dockerfile:1.0\n"+dockerfileB, ctx)
			refused(err)
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
			Expect(err.Error()).NotTo(ContainSubstring("\x07"))

			builds = 0
			Expect(ensureContent("# syntax=registry.example.com/f:2\n"+dockerfileB, ctx)).To(Succeed())
			Expect(errw.String()).To(ContainSubstring("WARNING:"))
			Expect(errw.String()).NotTo(ContainSubstring("\x1b"))
			Expect(errw.String()).NotTo(ContainSubstring("\x07"))
		})
	})

	Context("with .claude-sandbox/env in the context", func() {
		BeforeEach(func() { write(envFile, "TOKEN=secret\n") })

		DescribeTable("CS-IMG-079: the docker/dockerfile 1.0 frontend is refused before the build, exit 2",
			func(directive string) {
				err := ensure(directive, proj)
				refused(err)
				msg := err.Error()
				Expect(msg).To(ContainSubstring(df))
				Expect(msg).To(ContainSubstring(`"` + directive + `"`))
				Expect(msg).To(ContainSubstring(envFile))
				Expect(msg).To(ContainSubstring("raise the pin to docker/dockerfile:1.1 or later, or drop the syntax line"))
				Expect(msg).To(ContainSubstring("baseOnly: true, or a dockerfileDir"))
				Expect(strings.ToLower(msg)).NotTo(ContainSubstring("exclude it"), "no .dockerignore remedy is offered")
			},
			Entry("1.0", "docker/dockerfile:1.0"),
			Entry("1.0-experimental", "docker/dockerfile:1.0-experimental"),
			Entry("1.0.0-experimental", "docker/dockerfile:1.0.0-experimental"),
			Entry("1.0.2", "docker/dockerfile:1.0.2"),
			Entry("with the docker.io registry", "docker.io/docker/dockerfile:1.0"),
			Entry("with the index registry", "index.docker.io/docker/dockerfile:1.0-experimental"),
			Entry("with a digest beside the tag", "docker/dockerfile:1.0@sha256:0123456789abcdef"),
		)

		DescribeTable("CS-IMG-079: a frontend that reads Dockerfile.dockerignore builds, silently",
			func(directive string) {
				Expect(ensure(directive, proj)).To(Succeed())
				Expect(builds).To(Equal(1))
				Expect(errw.String()).To(BeEmpty())
			},
			Entry("no directive", ""),
			Entry("1", "docker/dockerfile:1"),
			Entry("1.1", "docker/dockerfile:1.1"),
			Entry("1.10", "docker/dockerfile:1.10"),
			Entry("1.1-experimental", "docker/dockerfile:1.1-experimental"),
			Entry("no tag", "docker/dockerfile"),
			Entry("docker.io, 1.7", "docker.io/docker/dockerfile:1.7"),
		)

		DescribeTable("CS-IMG-079: any other frontend gets one WARNING line, and the build goes on",
			func(directive string) {
				Expect(ensure(directive, proj)).To(Succeed())
				Expect(builds).To(Equal(1))
				lines := strings.Split(strings.TrimRight(errw.String(), "\n"), "\n")
				Expect(lines).To(HaveLen(1))
				Expect(lines[0]).To(HavePrefix("WARNING: "))
				Expect(lines[0]).To(ContainSubstring(`"` + directive + `"`))
				Expect(lines[0]).To(ContainSubstring(".claude-sandbox/env files are kept out of the build context only if that frontend honours Dockerfile.dockerignore"))
			},
			Entry("another registry", "registry.example.com/team/frontend:2"),
			Entry("docker/dockerfile-upstream", "docker/dockerfile-upstream:master"),
			Entry("docker/dockerfile by digest alone", "docker/dockerfile@sha256:0123456789abcdef"),
		)

		It("CS-IMG-079: a control character in another frontend's name never reaches the terminal", func() {
			Expect(ensure("evil/frontend\x1b]0;title\x07:1", proj)).To(Succeed())
			Expect(errw.String()).NotTo(ContainSubstring("\x1b"))
			Expect(errw.String()).NotTo(ContainSubstring("\x07"))
		})

		It("CS-IMG-079: the frontend is never changed: the build argv carries no frontend or syntax override", func() {
			Expect(ensure("registry.example.com/team/frontend:2", proj)).To(Succeed())
			for _, l := range fake.CommandLines() {
				if strings.HasPrefix(l, "docker build ") {
					Expect(l).NotTo(ContainSubstring("BUILDKIT_SYNTAX"))
					Expect(l).NotTo(ContainSubstring("--build-arg"))
				}
			}
		})
	})

	Context("where the env file lies", func() {
		It("CS-IMG-079: a nested project's .claude-sandbox/env counts", func() {
			nested := filepath.Join(proj, "sub", "repo", ".claude-sandbox", "env")
			write(nested, "TOKEN=x\n")
			err := ensure("docker/dockerfile:1.0", proj)
			refused(err)
			Expect(err.Error()).To(ContainSubstring(nested))
		})

		It("CS-IMG-079: the root env of a context that is a .claude-sandbox dir counts", func() {
			write(envFile, "TOKEN=x\n")
			refused(ensure("docker/dockerfile:1.0", filepath.Join(proj, ".claude-sandbox")))
		})

		It("CS-IMG-079: a cascade env file under the context counts wherever it lies", func() {
			odd := filepath.Join(proj, "secrets", "env")
			write(odd, "TOKEN=x\n")
			o.EnvFiles = []string{odd}
			refused(ensure("docker/dockerfile:1.0", proj))
		})

		It("CS-IMG-079: a context too large to check is refused, naming why", func() {
			defer imagebuild.SetEnvWalkMax(3)()
			for _, n := range []string{"a", "b", "c", "d", "e"} {
				write(filepath.Join(proj, n), "x\n")
			}
			err := ensure("docker/dockerfile:1.0", proj)
			refused(err)
			Expect(err.Error()).To(ContainSubstring("more than 3 entries"))
		})
	})

	Context("with no env file in the context", func() {
		It("CS-IMG-079: the old frontend builds, and nothing is printed", func() {
			// An env file above the context (a cascade one) is not sent.
			above := filepath.Join(filepath.Dir(proj), ".claude-sandbox", "env")
			write(above, "TOKEN=x\n")
			o.EnvFiles = []string{above}
			Expect(ensure("docker/dockerfile:1.0", proj)).To(Succeed())
			Expect(builds).To(Equal(1))
			Expect(errw.String()).To(BeEmpty())
			Expect(out.String()).NotTo(ContainSubstring("syntax"))
		})

		It("CS-IMG-079: another frontend builds, and nothing is printed", func() {
			Expect(ensure("registry.example.com/team/frontend:2", proj)).To(Succeed())
			Expect(builds).To(Equal(1))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-079: an env.example is not an env file", func() {
			write(filepath.Join(proj, ".claude-sandbox", "env.example"), "TOKEN=\n")
			Expect(ensure("docker/dockerfile:1.0", proj)).To(Succeed())
		})
	})
})
