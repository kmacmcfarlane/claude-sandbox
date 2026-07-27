package execx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Fake is a scripted, recording Runner for tests.
//
// Behavior is scripted with On(pattern, ...): the first stub whose pattern
// matches "name arg1 arg2 ..." (substring match) is used. Unmatched commands
// succeed with empty output.
type Fake struct {
	mu    sync.Mutex
	Calls []Cmd
	// Execed records the final Exec hand-off, if any.
	Execed *Cmd
	stubs  []stub
}

type stub struct {
	pattern string
	stdout  string
	err     error
	fn      func(c Cmd) (string, error)
}

// On scripts a response: commands whose "name args..." string contains
// pattern produce stdout and err.
func (f *Fake) On(pattern, stdout string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stubs = append(f.stubs, stub{pattern: pattern, stdout: stdout, err: err})
}

// OnFunc scripts a dynamic response.
func (f *Fake) OnFunc(pattern string, fn func(c Cmd) (string, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stubs = append(f.stubs, stub{pattern: pattern, fn: fn})
}

// Fail is a convenience for a stub that fails with the given exit code.
func Fail(code int) error { return &CodeError{Code: code, Msg: fmt.Sprintf("exit %d", code)} }

func (f *Fake) match(c Cmd) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := c.Name + " " + strings.Join(c.Args, " ")
	for _, s := range f.stubs {
		if strings.Contains(line, s.pattern) {
			if s.fn != nil {
				return s.fn(c)
			}
			return s.stdout, s.err
		}
	}
	return "", nil
}

func (f *Fake) record(c Cmd) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
}

func (f *Fake) Run(c Cmd) error {
	f.record(c)
	out, err := f.match(c)
	if c.Stdout != nil && out != "" {
		io.WriteString(c.Stdout, out)
	}
	return err
}

func (f *Fake) Output(c Cmd) (string, error) {
	f.record(c)
	return f.match(c)
}

type fakeProcess struct {
	err  error
	done chan struct{}
	once sync.Once
}

func (p *fakeProcess) Signal(sig os.Signal) error { p.stop(); return nil }
func (p *fakeProcess) stop()                      { p.once.Do(func() { close(p.done) }) }
func (p *fakeProcess) Wait() error                { <-p.done; return p.err }
func (p *fakeProcess) Pid() int                   { return 4242 }

func (f *Fake) Start(c Cmd) (Process, error) {
	f.record(c)
	out, err := f.match(c)
	if c.Stdout != nil && out != "" {
		io.WriteString(c.Stdout, out)
	}
	p := &fakeProcess{err: err, done: make(chan struct{})}
	p.stop() // scripted commands complete immediately
	return p, nil
}

func (f *Fake) Exec(c Cmd) error {
	f.record(c)
	f.Execed = &c
	return nil
}

// CommandLines renders each recorded call as a single string, for assertions.
func (f *Fake) CommandLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		lines = append(lines, c.Name+" "+strings.Join(c.Args, " "))
	}
	return lines
}
