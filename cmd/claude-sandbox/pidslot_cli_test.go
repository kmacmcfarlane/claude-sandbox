package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/pidslot"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// classRow is a docker ps row for another project carrying a pid class.
func classRow(name, class string) string {
	return strings.Join([]string{name, "Up 1h", "/elsewhere", "claude", "otter", "v1", "", "", "", class}, psSep)
}

var _ = Describe("pid classes (CS-PID, CS-LNCH-039)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-039: every launch carries a pid class label and env var, ralph included", func() {
		for _, args := range [][]string{{}, {"--ralph"}} {
			g := newCLIFixture()
			Expect(g.run(args...)).To(Equal(0), g.errw.String())
			line := g.execLine()
			Expect(line).To(MatchRegexp(`--label claude-sandbox\.pidclass=(\d+)`))
			m := regexpFind(line, `--label claude-sandbox\.pidclass=(\d+)`)
			Expect(line).To(ContainSubstring("-e CLAUDE_SANDBOX_PID_CLASS=" + m + " "))
			k, err := strconv.Atoi(m)
			Expect(err).NotTo(HaveOccurred())
			Expect(k).To(SatisfyAll(BeNumerically(">=", 0), BeNumerically("<", pidslot.Modulus)))
		}
	})

	It("CS-PID-004: the class is allocated against every sandbox on the host, not just this project", func() {
		rows := make([]string, 0, 255)
		for k := 0; k < 255; k++ {
			rows = append(rows, classRow("other-"+strconv.Itoa(k), strconv.Itoa(k)))
		}
		f.fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(newPIDClass(f.env)).To(Equal("255"))
		// The ps call must not be filtered to this project.
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "docker ps") {
				Expect(l).To(ContainSubstring("label=" + sessions.LabelProject + " "))
				Expect(l).NotTo(ContainSubstring("label=" + sessions.LabelProject + "="))
			}
		}
	})

	It("CS-PID-004: discovery failing still yields a class and never blocks the launch", func() {
		f.fake.On("docker ps", "", execx.Fail(1))
		k, err := strconv.Atoi(newPIDClass(f.env))
		Expect(err).NotTo(HaveOccurred())
		Expect(k).To(SatisfyAll(BeNumerically(">=", 0), BeNumerically("<", pidslot.Modulus)))
	})

	Describe("the pidslot subcommand", func() {
		var (
			last  int
			forks int
			execs [][]string
			env   map[string]string
			errw  *bytes.Buffer
		)
		BeforeEach(func() {
			last, forks, execs = 7, 0, nil
			env = map[string]string{}
			errw = &bytes.Buffer{}
			f.env.Err = errw
			f.env.PidslotOps = &pidslot.Ops{
				ReadLastPID: func() (int, error) { return last, nil },
				Fork:        func() error { last++; forks++; return nil },
				Exec: func(p string, argv []string) error {
					execs = append(execs, append([]string{p}, argv...))
					return nil
				},
				LookPath: func(n string) (string, error) {
					if n == "tini" || n == "claude" {
						return "/usr/bin/" + n, nil
					}
					return "", errors.New("not found")
				},
				Getenv: func(k string) string { return env[k] },
				Stderr: errw,
			}
		})

		It("CS-PID-001: lands the command on the class via tini", func() {
			env[pidslot.EnvVar] = "150"
			Expect(f.run("pidslot", "--", "claude", "--model", "opus")).To(Equal(0), errw.String())
			Expect(forks).To(Equal(142))
			Expect(execs).To(Equal([][]string{{"/usr/bin/tini", "tini", "-s", "--", "claude", "--model", "opus"}}))
		})

		It("CS-PID-003: with no class it warns and execs the command directly", func() {
			Expect(f.run("pidslot", "--", "claude")).To(Equal(0))
			Expect(forks).To(BeZero())
			Expect(execs).To(Equal([][]string{{"/usr/bin/claude", "claude"}}))
			Expect(errw.String()).To(ContainSubstring("Warning: pid class not applied"))
		})

		It("CS-PID-003: a missing command is the only failure", func() {
			Expect(f.run("pidslot")).To(Equal(2))
		})
	})

	It("CS-PID-007: the entrypoint hands off to the helper and the base image installs tini", func() {
		root := filepath.Join("..", "..")
		ep, err := os.ReadFile(filepath.Join(root, "entrypoint.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(ep))).To(HaveSuffix(`exec gosu "$TARGET_USER" /opt/claude-sandbox/bin/claude-sandbox pidslot -- "$@"`))
		df, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(df)).To(MatchRegexp(`(?m)^\s+tini\s*\\?$`))
	})
})

// regexpFind returns the first capture group of pattern in s.
func regexpFind(s, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	Expect(m).To(HaveLen(2), "pattern %q not found in %q", pattern, s)
	return m[1]
}
