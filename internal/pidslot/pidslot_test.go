package pidslot_test

import (
	"bytes"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/pidslot"
)

// counter simulates the namespace pid counter: every fork consumes one pid.
type counter struct {
	last  int
	forks int
}

func (c *counter) read() (int, error) { return c.last, nil }
func (c *counter) fork() error        { c.last++; c.forks++; return nil }

type fixture struct {
	c      *counter
	env    map[string]string
	stderr *bytes.Buffer
	execs  [][]string
	path   map[string]string
	ops    pidslot.Ops
}

func newFixture(last int) *fixture {
	f := &fixture{c: &counter{last: last}, env: map[string]string{}, stderr: &bytes.Buffer{},
		path: map[string]string{"tini": "/usr/bin/tini", "claude": "/usr/local/bin/claude"}}
	f.ops = pidslot.Ops{
		ReadLastPID: f.c.read,
		Fork:        f.c.fork,
		Exec: func(p string, argv []string) error {
			f.execs = append(f.execs, append([]string{p}, argv...))
			return nil
		},
		LookPath: func(n string) (string, error) {
			if p, ok := f.path[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Getenv: func(k string) string { return f.env[k] },
		Stderr: f.stderr,
	}
	return f
}

var _ = Describe("pid classes (CS-PID)", func() {
	It("CS-PID-001: burns until the counter is class-1 mod 256, then execs tini -s -- cmd", func() {
		f := newFixture(7)
		f.env[pidslot.EnvVar] = "150"
		Expect(pidslot.Run(f.ops, []string{"claude", "--model", "x"})).To(Equal(0))
		Expect(f.c.last % pidslot.Modulus).To(Equal(149))
		Expect(f.c.forks).To(Equal(142))
		Expect(f.execs).To(HaveLen(1))
		Expect(f.execs[0]).To(Equal([]string{"/usr/bin/tini", "tini", "-s", "--", "claude", "--model", "x"}))
		Expect(f.stderr.String()).To(BeEmpty())
	})

	It("CS-PID-001: forks nothing when the counter already sits on the residue", func() {
		f := newFixture(255 + 256*3) // ≡ 255 → class 0 needs 255
		f.env[pidslot.EnvVar] = "0"
		n, err := pidslot.Burn(f.ops, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeZero())
	})

	It("CS-PID-001: wraps across the modulus boundary", func() {
		f := newFixture(300) // 300 % 256 = 44; class 10 wants 9 → 221 forks
		n, err := pidslot.Burn(f.ops, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(221))
		Expect(f.c.last % pidslot.Modulus).To(Equal(9))
	})

	It("CS-PID-002: a counter that does not parse degrades to a direct exec with a warning", func() {
		f := newFixture(0)
		f.env[pidslot.EnvVar] = "5"
		f.ops.ReadLastPID = func() (int, error) { return 0, errors.New("garbage") }
		Expect(pidslot.Run(f.ops, []string{"claude"})).To(Equal(0))
		Expect(f.execs).To(Equal([][]string{{"/usr/local/bin/claude", "claude"}}))
		Expect(f.stderr.String()).To(ContainSubstring("Warning: pid class not applied"))
	})

	DescribeTable("CS-PID-003: every failure warns and execs the command directly",
		func(prepare func(f *fixture), reason string) {
			f := newFixture(7)
			f.env[pidslot.EnvVar] = "3"
			prepare(f)
			Expect(pidslot.Run(f.ops, []string{"claude"})).To(Equal(0))
			Expect(f.execs).To(Equal([][]string{{"/usr/local/bin/claude", "claude"}}))
			Expect(f.stderr.String()).To(ContainSubstring("Warning: pid class not applied (" + reason))
		},
		Entry("class unset", func(f *fixture) { delete(f.env, pidslot.EnvVar) }, pidslot.EnvVar+" is unset"),
		Entry("class out of range", func(f *fixture) { f.env[pidslot.EnvVar] = "256" }, pidslot.EnvVar+" is unset"),
		Entry("class not a number", func(f *fixture) { f.env[pidslot.EnvVar] = "otter" }, pidslot.EnvVar+" is unset"),
		Entry("tini missing", func(f *fixture) { delete(f.path, "tini") }, "tini not found"),
		Entry("burn never converges", func(f *fixture) {
			f.ops.Fork = func() error { return nil } // counter never moves
		}, "pid counter never reached"),
		Entry("fork fails", func(f *fixture) { f.ops.Fork = func() error { return errors.New("EAGAIN") } }, "forking"),
	)

	It("CS-PID-003: the burn is capped at MaxBurn forks", func() {
		f := newFixture(7)
		calls := 0
		f.ops.Fork = func() error { calls++; return nil }
		_, err := pidslot.Burn(f.ops, 3)
		Expect(err).To(HaveOccurred())
		Expect(calls).To(Equal(pidslot.MaxBurn))
	})

	It("CS-PID-003: a command that cannot be exec'd at all is the only non-zero exit", func() {
		f := newFixture(7)
		f.env[pidslot.EnvVar] = "3"
		f.ops.Exec = func(string, []string) error { return errors.New("ENOEXEC") }
		Expect(pidslot.Run(f.ops, []string{"claude"})).To(Equal(126))
		delete(f.path, "claude")
		Expect(pidslot.Run(f.ops, []string{"claude"})).To(Equal(127))
		Expect(pidslot.Run(f.ops, nil)).To(Equal(2))
	})

	It("CS-PID-006: BurnFromEnv burns for a parent that forks itself, and is silent when unset", func() {
		f := newFixture(7)
		pidslot.BurnFromEnv(f.ops)
		Expect(f.c.forks).To(BeZero())
		Expect(f.stderr.String()).To(BeEmpty())

		f.env[pidslot.EnvVar] = "20"
		pidslot.BurnFromEnv(f.ops)
		Expect(f.c.last % pidslot.Modulus).To(Equal(19))

		f.ops.Fork = func() error { return errors.New("EAGAIN") }
		f.env[pidslot.EnvVar] = "21"
		pidslot.BurnFromEnv(f.ops)
		Expect(strings.Count(f.stderr.String(), "Warning: pid class 21 not applied")).To(Equal(1))
	})

	It("ParseClass accepts only integers in [0,256)", func() {
		for _, bad := range []string{"", " ", "-1", "256", "1.5", "x"} {
			_, ok := pidslot.ParseClass(bad)
			Expect(ok).To(BeFalse(), bad)
		}
		k, ok := pidslot.ParseClass(" 255 ")
		Expect(ok).To(BeTrue())
		Expect(k).To(Equal(255))
	})
})
