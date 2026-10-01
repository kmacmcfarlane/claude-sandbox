package main

// "claude-sandbox tmux save <state-file>" (CS-TMUX-030..040, plan
// sandbox-reboot-restore F3): the tmux-resurrect post-save-layout hook,
//
//	set -g @resurrect-hook-post-save-layout 'claude-sandbox tmux save'
//
// The work is internal/tmuxpane's Save; this file wires the Env seams in and
// keeps the hook silent: it never writes stdout or stderr (a manual save runs
// it through run-shell, which would show any output) and always exits 0.
// Problems go to <cache root>/tmux-save.log.
//
// "claude-sandbox tmux restore" (CS-TMUX-045..051, F4a) is the read-only half
// of the restore: --list and --dry-run [--all] [--from]. It writes nothing,
// takes no lock and starts nothing; the acting restore is F4b's.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// tmuxSaveLog is the hook's log, in the cache root (CS-TMUX-030).
const tmuxSaveLog = "tmux-save.log"

// tmuxSaveLogMax is the size past which the log is emptied before a write.
const tmuxSaveLogMax = 64 << 10

func newTmuxCmd(env *Env) *cobra.Command {
	t := &cobra.Command{
		Use:           "tmux",
		Short:         "tmux-resurrect integration: the save hook and the restore (host only)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exitErr(2, "Error: unknown tmux command %q (save, restore)", args[0])
			}
			fmt.Fprintln(env.Out, cmd.UsageString())
			return exitErr(2, "Error: tmux needs a command: save or restore")
		},
	}
	t.AddCommand(&cobra.Command{
		Use:   "save STATE-FILE",
		Short: "tmux-resurrect post-save-layout hook: record each sandbox pane's conversation beside the save",
		Long: "Run by tmux-resurrect after it writes a save, with the state file's path:\n" +
			"  set -g @resurrect-hook-post-save-layout 'claude-sandbox tmux save'\n" +
			"Never prints and always exits 0; problems go to ~/.cache/claude-sandbox/" + tmuxSaveLog + ".",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Any argument is the state file's path, never a flag: an error
		// message would reach the operator's screen on a manual save.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runTmuxSave(env, args)
			return nil
		},
	})
	t.AddCommand(newTmuxRestoreCmd(env))
	return t
}

// runTmuxSave is the hook (CS-TMUX-030): every outcome is exit 0 and silent.
func runTmuxSave(env *Env, args []string) {
	if hostdirs.InSandbox(env.Getenv) {
		return // a sandbox has no tmux socket, and its cache is not the host's
	}
	var logged []string
	logf := func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
	defer func() {
		if r := recover(); r != nil {
			logf("panic: %v", r)
		}
		writeTmuxSaveLog(env, logged)
	}()
	if len(args) != 1 {
		logf("expected one argument (the state file), got %d", len(args))
		return
	}
	_, _, _, home := hostIdentity(env.Getenv)
	if _, err := tmuxpane.Save(args[0], tmuxpane.SaveOptions{
		Runner:       env.Runner,
		Now:          env.now,
		Home:         home,
		ConfigDirEnv: env.Getenv("CLAUDE_CONFIG_DIR"),
		Logf:         logf,
	}); err != nil {
		logf("%v", err)
	}
}

// writeTmuxSaveLog appends the run's problems to the hook's log, 0600, never
// through a symlink, emptying it first once it has grown past 64 KiB. A
// failure to log is dropped: the hook must stay silent.
func writeTmuxSaveLog(env *Env, lines []string) {
	if len(lines) == 0 {
		return
	}
	dir := env.cacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, tmuxSaveLog)
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND | syscall.O_NOFOLLOW
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > tmuxSaveLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return
	}
	stamp := env.now().UTC().Format("2006-01-02T15:04:05Z")
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s tmux save: %s\n", stamp, strings.ReplaceAll(l, "\n", " "))
	}
	f.WriteString(b.String())
}

// resurrectDir resolves Env.ResurrectDir (CS-TMUX-045): resurrect's own rule
// unless a test set it. Like cacheDir it panics under go test when unset, so
// no test reads the operator's ~/.local/share/tmux/resurrect.
func (e *Env) resurrectDir() string {
	if e.ResurrectDir != "" {
		return e.ResurrectDir
	}
	if testing.Testing() {
		panic("a test resolved the real resurrect dir; set Env.ResurrectDir to a scratch directory such as GinkgoT().TempDir()")
	}
	_, _, _, home := hostIdentity(e.Getenv)
	return tmuxpane.ResolveResurrectDir(tmuxpane.DirOptions{
		Runner: e.Runner, Home: home, Getenv: e.Getenv, Hostname: os.Hostname,
	})
}

// restoreOpts are the restore's flags.
type restoreOpts struct {
	list, all, dryRun bool
	from              string
}

func newTmuxRestoreCmd(env *Env) *cobra.Command {
	var o restoreOpts
	c := &cobra.Command{
		Use:   "restore [--list [--all] | --dry-run [--all] [--from SAVE]]",
		Short: "Restore sandbox panes after a tmux restart (read-only so far: --list, --dry-run)",
		Long: "List the tmux-resurrect saves (--list), or show what a restore would do in this pane\n" +
			"(--dry-run) or in every pane of a save (--dry-run --all). SAVE is last (the default),\n" +
			"previous (what the previous tmux server ended with), a stamp such as 20260929T120000,\n" +
			"or the name of a save in the resurrect dir. Nothing is written, locked or started.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTmuxRestore(env, o, cmd.Flags().Changed("from"))
		},
	}
	f := c.Flags()
	f.BoolVar(&o.list, "list", false, "list the saves, newest first, as runs of saves holding the same sandbox sessions")
	f.BoolVar(&o.all, "all", false, "with --list: 30 days instead of 7; with --dry-run: every pane of the save")
	f.BoolVar(&o.dryRun, "dry-run", false, "show what a restore would do, without doing it")
	f.StringVar(&o.from, "from", "", "the save to read: last, previous, a stamp, or a save's file name")
	_ = c.RegisterFlagCompletionFunc("from", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		out := []string{"last", "previous"}
		if s, err := tmuxpane.OpenSaves(env.resurrectDir()); err == nil {
			for i, st := range s.Stamps {
				if i == 200 {
					break
				}
				out = append(out, st)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
	return c
}

// runTmuxRestore checks the form (CS-TMUX-051 row 1, CS-TMUX-049) and runs
// it. Every refusal is exit 2 with one line.
func runTmuxRestore(env *Env, o restoreOpts, fromSet bool) error {
	if hostdirs.InSandbox(env.Getenv) {
		return exitErr(2, "Error: tmux restore runs on the host only (a sandbox has no tmux server)")
	}
	switch {
	case o.list && (o.dryRun || fromSet):
		return exitErr(2, "Error: --list takes only --all")
	case o.list:
		return runRestoreList(env, o.all)
	case !o.dryRun:
		return exitErr(2, "Error: restoring is not built yet; use --dry-run to see what it would do, or --list")
	case o.all:
		return runRestoreDryRunAll(env, o.from)
	}
	return runRestoreDryRun(env, o.from)
}

// restoreHome is the invoking user's home and the shell's CLAUDE_CONFIG_DIR.
func restoreHome(env *Env) (home, configDirEnv string) {
	_, _, _, home = hostIdentity(env.Getenv)
	return home, env.Getenv("CLAUDE_CONFIG_DIR")
}

// openRestoreSaves opens the resurrect dir and its lifetimes index (a missing
// or unreadable index is a scan, CS-TMUX-047).
func openRestoreSaves(env *Env) (*tmuxpane.Saves, []tmuxpane.Lifetime, error) {
	dir := env.resurrectDir()
	saves, err := tmuxpane.OpenSaves(dir)
	if err != nil {
		return nil, nil, exitErr(2, "Error: cannot read the resurrect dir %s: %v", dir, err)
	}
	idx, _ := tmuxpane.ReadLifetimes(dir)
	return saves, idx, nil
}

// resolveFrom maps --from to a stamp (CS-TMUX-049): exit 2 for anything that
// names no save.
func resolveFrom(saves *tmuxpane.Saves, from string, running *tmuxpane.Server, idx []tmuxpane.Lifetime) (string, error) {
	st, err := saves.Resolve(from, running, idx)
	if errors.Is(err, tmuxpane.ErrNoPrevious) {
		return "", exitErr(2, "Error: %v; list the saves with: claude-sandbox tmux restore --list", err)
	}
	if err != nil {
		return "", exitErr(2, "Error: %v", err)
	}
	return st, nil
}

// fromLabel names where a save came from in the dry-run's header.
func fromLabel(from string) string {
	switch from {
	case "", "last":
		return " (last)"
	case "previous":
		return " (previous)"
	}
	return ""
}

// restoreProbes are the dry-run's read-only probes (CS-TMUX-051), with the
// resume guard run over one discovery.
func restoreProbes(env *Env, self string, panes []tmuxpane.PaneInfo) *tmuxpane.ReadProbes {
	home, cde := restoreHome(env)
	var (
		discovered bool
		found      []sessions.Session
		derr       error
	)
	guard := func(m tmuxpane.Mark) tmuxpane.GuardResult {
		if !discovered {
			discovered = true
			// Uncounted: the guard needs states, classes and labels, not a
			// "docker top" per sandbox; bounded, so a hung daemon fails the
			// guard closed ("cannot tell") instead of hanging the dry run.
			found, derr = sessions.DiscoverAllUncounted(tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout})
		}
		cfg := m.ConfigDir
		if cfg == "" {
			cfg = cde
			if !filepath.IsAbs(cfg) {
				cfg = filepath.Join(home, ".claude")
			}
		}
		id := strings.ToLower(m.Conversation)
		v := resumeguard.Check{
			ID: id, Sessions: found, DiscoveryErr: derr, Runner: env.Runner,
			Home: home, ConfigDir: cfg, ProcRoot: env.ProcRoot,
		}.Run()
		g := tmuxpane.GuardResult{Open: v.Open, HostPID: v.HostPID, Reason: v.Reason}
		if h := v.Holder; h != nil {
			g.Holder = h.Name
			if h.Instance != "" {
				g.Holder = "'" + h.Instance + "' (" + h.Name + ")"
				if h.Mode != sessions.ModeRalph && h.Mode != sessions.ModeHeadless {
					g.AttachCommand = attachCommand(h.Project, home, h.Instance)
				}
			}
		}
		return g
	}
	return &tmuxpane.ReadProbes{Runner: env.Runner, Home: home, ConfigDirEnv: cde, Self: self, GuardFunc: guard, Panes: panes}
}

// printDecision prints one decision: its line, what a restore would do, the
// notes of a resume and the manual command.
func printDecision(w io.Writer, indent string, d tmuxpane.Decision) {
	fmt.Fprintf(w, "%sdecision (row %d): %s\n", indent, d.Row, d.Line)
	fmt.Fprintf(w, "%swould: %s\n", indent, d.Would())
	for _, n := range d.Notes {
		fmt.Fprintf(w, "%snote: %s\n", indent, n)
	}
	if d.Manual != "" {
		fmt.Fprintf(w, "%smanual: %s\n", indent, d.Manual)
	}
}

// rowLabel names a row in --all's listing: name, noun and conversation, only
// once the row passed CS-TMUX-046's checks (nothing unchecked is printed).
func rowLabel(m tmuxpane.Mark) string {
	if tmuxpane.ValidateRow(m) != "" {
		return "(a row that cannot be used)"
	}
	conv := m.Conversation
	if conv == "" {
		conv = "no conversation recorded"
	}
	noun := m.Instance
	if noun == "" {
		noun = m.Container
	}
	return fmt.Sprintf("'%s'  %s  %s  [%s, %s]", tmuxpane.RowName(m), noun, conv, m.Mode, m.State)
}

// runRestoreDryRun is "--dry-run [--from SAVE]" in one pane (CS-TMUX-051):
// the pane's own pending mark, else last's row at its coordinates; with
// --from, that save's row only.
func runRestoreDryRun(env *Env, from string) error {
	paneID, ok := tmuxpane.FromEnv(env.Getenv)
	if !ok {
		return exitErr(2, "Error: tmux restore --dry-run runs in a tmux pane (TMUX_PANE is not set); use --dry-run --all outside tmux")
	}
	tp, ok := tmuxpane.ReadThisPane(env.Runner, paneID)
	if !ok {
		return exitErr(2, "Error: tmux did not answer for pane %s", paneID)
	}
	coords := tp.Coords()
	probes := restoreProbes(env, paneID, nil)
	out := env.Out
	fmt.Fprintln(out, "Dry run: nothing is changed.")
	if from == "" && tp.Marked && tp.Mark.State == tmuxpane.StatePending {
		fmt.Fprintf(out, "Pane %s, from its own pending mark:\n", coords)
		row := tmuxpane.Row{Session: tp.Session, Window: tp.Window, Pane: tp.Pane, Mark: tp.Mark}
		printDecision(out, "  ", tmuxpane.Decide(&row, coords, probes))
		return nil
	}
	saves, idx, err := openRestoreSaves(env)
	if err != nil {
		return err
	}
	stamp, err := resolveFrom(saves, from, tp.Server, idx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Pane %s, from save %s%s in %s:\n", coords, stamp, fromLabel(from), saves.Dir)
	sc, err := saves.Sidecar(stamp)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(out, "  this save has no claude-sandbox record (the save hook was not wired then): nothing to restore\n")
		return nil
	case err != nil:
		fmt.Fprintf(out, "  cannot read this save (%v): nothing to restore\n", err)
		return nil
	}
	if line := saves.SparseOf(stamp, len(sc.Panes), sc.Server, idx).Line(); line != "" {
		fmt.Fprintln(out, line)
	}
	var row *tmuxpane.Row
	for i := range sc.Panes {
		r := &sc.Panes[i]
		if r.Session == tp.Session && r.Window == tp.Window && r.Pane == tp.Pane {
			row = r
			break
		}
	}
	printDecision(out, "  ", tmuxpane.Decide(row, coords, probes))
	return nil
}

// runRestoreDryRunAll is "--dry-run --all [--from SAVE]" (CS-TMUX-051): the
// running server's pending marks, then every row of the save. It runs
// anywhere on the host; "previous" needs tmux.
func runRestoreDryRunAll(env *Env, from string) error {
	out := env.Out
	panes, ok := tmuxpane.ListPanes(env.Runner)
	probes := restoreProbes(env, "", panes)
	fmt.Fprintln(out, "Dry run: nothing is changed.")
	fmt.Fprintln(out, "Pending marks in the running tmux server:")
	switch {
	case !ok:
		fmt.Fprintln(out, "  (no tmux server answered)")
	default:
		n := 0
		for _, p := range panes {
			if !p.Marked || p.Mark.State != tmuxpane.StatePending {
				continue
			}
			n++
			row := tmuxpane.Row{Session: p.Session, Window: p.Window, Pane: p.Pane, Mark: p.Mark}
			fmt.Fprintf(out, "  %s  %s\n", p.Coords(), rowLabel(p.Mark))
			printDecision(out, "      ", tmuxpane.Decide(&row, p.Coords(), probes))
		}
		if n == 0 {
			fmt.Fprintln(out, "  (none)")
		}
	}
	saves, idx, err := openRestoreSaves(env)
	if err != nil {
		return err
	}
	var running *tmuxpane.Server
	if from == "previous" && env.Getenv("TMUX") != "" {
		running = tmuxpane.RunningServer(env.Runner)
	}
	stamp, err := resolveFrom(saves, from, running, idx)
	if err != nil {
		return err
	}
	sc, err := saves.Sidecar(stamp)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(out, "Save %s%s in %s has no claude-sandbox record (the save hook was not wired then).\n", stamp, fromLabel(from), saves.Dir)
		return nil
	case err != nil:
		fmt.Fprintf(out, "Cannot read save %s%s in %s (%v).\n", stamp, fromLabel(from), saves.Dir, err)
		return nil
	}
	fmt.Fprintf(out, "Save %s%s in %s: %s:\n", stamp, fromLabel(from), saves.Dir, plural(len(sc.Panes), "sandbox pane"))
	if line := saves.SparseOf(stamp, len(sc.Panes), sc.Server, idx).Line(); line != "" {
		fmt.Fprintln(out, line)
	}
	for i := range sc.Panes {
		r := &sc.Panes[i]
		coords := fmt.Sprintf("%s:%d.%d", r.Session, r.Window, r.Pane)
		if strings.ContainsFunc(r.Session, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			coords = "(an unprintable session name)"
		}
		fmt.Fprintf(out, "  %s  %s\n", coords, rowLabel(r.Mark))
		printDecision(out, "      ", tmuxpane.Decide(r, coords, probes))
	}
	return nil
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// runRestoreList is "--list [--all]" (CS-TMUX-048): the saves as runs, newest
// first, grouped by tmux server, then how to use a line.
func runRestoreList(env *Env, all bool) error {
	out := env.Out
	saves, idx, err := openRestoreSaves(env)
	if err != nil {
		return err
	}
	days := tmuxpane.ListDays
	if all {
		days = tmuxpane.ListDaysAll
	}
	runs := saves.Runs(env.now().Add(-time.Duration(days)*24*time.Hour), idx)
	if len(runs) == 0 {
		fmt.Fprintf(out, "No tmux-resurrect saves in %s in the last %d days.\n", saves.Dir, days)
		return nil
	}
	more := "; --all for 30"
	if all {
		more = ""
	}
	fmt.Fprintf(out, "tmux-resurrect saves in %s (newest first, last %d days%s):\n", saves.Dir, days, more)
	heading := "\x00"
	for _, r := range runs {
		// A save without a usable record has no server to sit under: it
		// gets a heading of its own, never another group's.
		key := "-"
		switch {
		case r.Record && r.Server != nil:
			key = fmt.Sprintf("%d.%d", r.Server.PID, r.Server.Start)
		case r.Record:
			key = "?"
		}
		if key != heading {
			heading = key
			switch key {
			case "-":
				fmt.Fprintln(out, "\nsaves without a usable claude-sandbox record:")
			case "?":
				fmt.Fprintln(out, "\nsaves that do not record their tmux server:")
			default:
				fmt.Fprintf(out, "\ntmux server started %s (pid %d):\n", time.Unix(r.Server.Start, 0).Local().Format("2006-01-02 15:04"), r.Server.PID)
			}
		}
		fmt.Fprintln(out, "  "+runLine(r))
	}
	example := exampleStamp(runs)
	fmt.Fprintf(out, `
Use a save by its stamp (%[1]s here):
  one pane, typed in that pane:   claude-sandbox tmux restore --dry-run --from %[1]s
  every pane of the save:         claude-sandbox tmux restore --dry-run --all --from %[1]s
  (restoring itself is still to come; the dry run prints each pane's manual command)
A whole layout from a save, when its windows are gone:
`, example)
	for _, l := range tmuxpane.Procedures(saves.Dir, example, tmuxpane.SaveInterval(env.Runner)) {
		fmt.Fprintln(out, "  "+l)
	}
	return nil
}

// runLine renders one run of --list (CS-TMUX-048).
func runLine(r tmuxpane.Run) string {
	when := func(st string) string {
		if t := tmuxpane.StampTime(st); !t.IsZero() {
			return t.Format("2006-01-02 15:04")
		}
		return "?"
	}
	span := "1 save"
	if r.Count > 1 {
		span = fmt.Sprintf("%d saves since %s", r.Count, when(r.Oldest))
	}
	parts := []string{r.Newest, when(r.Newest), span}
	switch {
	case r.Err != nil:
		parts = append(parts, "unreadable ("+r.Err.Error()+")")
	case !r.Record:
		parts = append(parts, "no record")
	default:
		c := plural(r.Rows, "sandbox pane")
		var st []string
		if r.Active > 0 {
			st = append(st, fmt.Sprintf("%d active", r.Active))
		}
		if r.Pending > 0 {
			st = append(st, fmt.Sprintf("%d pending", r.Pending))
		}
		if len(st) > 0 {
			c += " (" + strings.Join(st, ", ") + ")"
		}
		parts = append(parts, c)
	}
	if r.Last {
		parts = append(parts, "last")
	}
	if r.Sparse.Sparse {
		parts = append(parts, fmt.Sprintf("sparse (had %d)", r.Sparse.M))
	}
	return strings.Join(parts, "  ")
}

// exampleStamp is the stamp the footer fills in: the newest save of the
// previous tmux server when the list shows one, else the run before the
// newest, else the newest.
func exampleStamp(runs []tmuxpane.Run) string {
	first := runs[0].Server
	for _, r := range runs[1:] {
		if r.Record && r.Server != nil && first != nil && *r.Server != *first {
			return r.Newest
		}
	}
	if len(runs) > 1 {
		return runs[1].Newest
	}
	return runs[0].Newest
}
