package imagebuild

// The detached build-cache budget check (CS-IMG-041..043). The check itself
// is WarnCacheBudget; this file is how its report crosses from the checker a
// launch leaves behind to the launch after it: one result file, written by at
// most one checker at a time, read once and removed.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// CacheBudgetFile is the result file, under the cache dir
	// (~/.cache/claude-sandbox).
	CacheBudgetFile = "cache-budget.json"
	// CacheBudgetLockFile serializes checkers: a flock, so a checker that
	// dies releases it.
	CacheBudgetLockFile = "cache-budget.lock"
)

// CacheBudgetResult is the result file's content.
type CacheBudgetResult struct {
	// CheckedAt is when the checker ran, i.e. just after the build.
	CheckedAt time.Time `json:"checkedAt"`
	// Report is WarnCacheBudget's text, empty when there is nothing to say
	// (healthy, or docker's output could not be parsed).
	Report string `json:"report"`
}

// ErrCacheBudgetBusy is returned by RunCacheBudgetCheck when another checker
// holds the lock. It is not a failure: that checker is producing the result.
var ErrCacheBudgetBusy = errors.New("another cache-budget check is running")

// RunCacheBudgetCheck is the checker (CS-IMG-042): under a non-blocking lock
// on dir/cache-budget.lock it runs the check and writes dir/cache-budget.json
// atomically. On contention it returns ErrCacheBudgetBusy at once, having run
// nothing and written nothing. now is the clock; nil means time.Now.
func RunCacheBudgetCheck(o Options, dir string, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	release, err := tryLock(filepath.Join(dir, CacheBudgetLockFile))
	if err != nil {
		return err
	}
	defer release()

	var report bytes.Buffer
	WarnCacheBudget(o, &report)
	data, err := json.MarshalIndent(CacheBudgetResult{CheckedAt: now(), Report: report.String()}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, CacheBudgetFile), append(data, '\n'))
}

// ConsumeCacheBudget prints the previous checker's report to w, once, and
// removes the result file (CS-IMG-043). No docker call. The file is claimed by
// a rename first, so two launches racing for it cannot both print it, and a
// checker that writes a new one meanwhile is not lost. An empty report or an
// unreadable file prints nothing; the file goes either way.
func ConsumeCacheBudget(dir string, w io.Writer) {
	path := filepath.Join(dir, CacheBudgetFile)
	claimed := path + "." + strconv.Itoa(os.Getpid()) + ".consumed"
	if err := os.Rename(path, claimed); err != nil {
		return
	}
	data, err := os.ReadFile(claimed)
	os.Remove(claimed)
	if err != nil {
		return
	}
	var r CacheBudgetResult
	if json.Unmarshal(data, &r) != nil || strings.TrimSpace(r.Report) == "" {
		return
	}
	fmt.Fprintf(w, "\nBuild-cache check after the image build of %s:\n", r.CheckedAt.Local().Format("2006-01-02 15:04"))
	io.WriteString(w, strings.TrimLeft(r.Report, "\n"))
}

// tryLock takes an exclusive flock on path without waiting.
func tryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrCacheBudgetBusy
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// writeAtomic writes data to a temp file beside path and renames it over
// path, so a reader never sees a partial file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
