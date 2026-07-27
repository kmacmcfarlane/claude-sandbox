package ralphloop

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// pipelineTracker lets an interrupt handler terminate the in-flight
// iteration (CS-RLP-017).
type pipelineTracker struct {
	mu    sync.Mutex
	procs []*exec.Cmd
}

func (t *pipelineTracker) set(procs []*exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.procs = procs
}

func (t *pipelineTracker) signal(sig syscall.Signal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.procs {
		if c.Process != nil {
			// Negative pid: whole process group.
			syscall.Kill(-c.Process.Pid, sig)
		}
	}
}

var tracker pipelineTracker

// Terminate TERMs the current iteration's process tree (interrupt path).
func Terminate() { tracker.signal(syscall.SIGTERM) }

// runIterationReal launches claude piped through the logstream stages
// (CS-RLP-011..015) and returns the pipeline exit code (pipefail semantics,
// 124 on hard timeout).
func (l *Loop) runIterationReal(iter int, resume bool) int {
	promptBytes, err := l.promptData()
	if err != nil {
		fmt.Fprintln(l.Err, err)
		return 1
	}
	args := l.claudeArgs(resume)
	fmt.Fprintf(l.Out, "Launching claude %s\n", strings.Join(args, " "))

	stderrF, err := os.Create(l.StderrFile)
	if err != nil {
		fmt.Fprintln(l.Err, err)
		return 1
	}
	defer stderrF.Close()

	rawLog := fmt.Sprintf("%s_%s_iter%d", l.RawLogBase, time.Now().Format("20060102150405"), iter)
	ls := filepath.Join(l.RepoRoot, "logstream")

	cmds := []*exec.Cmd{exec.Command(l.ClaudeBin, args...)}
	if !l.Interactive {
		stages := [][]string{
			{"node", filepath.Join(ls, "raw-json-logger.js"), "--out", rawLog},
			{"node", filepath.Join(ls, "run-logger.js"), "--log-file", l.RunlogFile,
				"--iteration", strconv.Itoa(iter), "--quota-status-file", l.QuotaFile},
			{"node", filepath.Join(ls, "exit-on-result.js")},
		}
		if l.watchdogMinutes() > 0 {
			stages = append(stages, []string{"node", filepath.Join(ls, "activity-watchdog.js"),
				"--timeout", strconv.Itoa(l.watchdogMinutes()), "--marker-file", l.MarkerFile})
		}
		stages = append(stages, []string{"node", filepath.Join(ls, "console-output.js")})
		for _, s := range stages {
			cmds = append(cmds, exec.Command(s[0], s[1:]...))
		}
	}

	// Wire the pipe chain: prompt -> claude -> stages... -> l.Out.
	cmds[0].Stdin = bytes.NewReader(promptBytes)
	for i := 0; i < len(cmds)-1; i++ {
		pipe, perr := cmds[i].StdoutPipe()
		if perr != nil {
			fmt.Fprintln(l.Err, perr)
			return 1
		}
		cmds[i+1].Stdin = pipe
	}
	cmds[len(cmds)-1].Stdout = l.Out
	for _, c := range cmds {
		c.Stderr = stderrF
		c.Dir = l.WorkDir
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	for i, c := range cmds {
		if serr := c.Start(); serr != nil {
			fmt.Fprintln(l.Err, serr)
			for _, prev := range cmds[:i] {
				syscall.Kill(-prev.Process.Pid, syscall.SIGKILL)
				prev.Wait()
			}
			return 1
		}
	}
	tracker.set(cmds)
	defer tracker.set(nil)

	// Hard iteration timeout: TERM the tree, KILL 30s later (CS-RLP-015).
	var timedOut atomic.Bool
	timer := time.AfterFunc(time.Duration(l.IterationTimeout)*time.Second, func() {
		timedOut.Store(true)
		tracker.signal(syscall.SIGTERM)
		time.AfterFunc(30*time.Second, func() { tracker.signal(syscall.SIGKILL) })
	})
	defer timer.Stop()

	// pipefail: the rightmost non-zero exit wins.
	rc := 0
	for _, c := range cmds {
		if werr := c.Wait(); werr != nil {
			code := 1
			if ee, ok := werr.(*exec.ExitError); ok {
				code = ee.ExitCode()
				if code < 0 {
					code = 143 // killed by signal
				}
			}
			rc = code
		}
	}
	if timedOut.Load() {
		return 124
	}
	return rc
}
