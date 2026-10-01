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
// "claude-sandbox tmux restore" (CS-TMUX-045..051, F4a) has a read-only
// half: --list and --dry-run [--all] [--from], which write nothing, take no
// lock and start nothing. The plain form and --drop (CS-TMUX-052..063, F4b)
// act in the pane they are typed in: mark it pending from its row, decide
// the row, then clear it, keep it pending, attach to the running container,
// or resume the conversation in a new one — one start at a time, under the
// restore start lock.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
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
	list, all, dryRun, drop bool
	from                    string
}

func newTmuxRestoreCmd(env *Env) *cobra.Command {
	var o restoreOpts
	c := &cobra.Command{
		Use:   "restore [--from SAVE | --drop | --list [--all] | --dry-run [--all] [--from SAVE]]",
		Short: "Restore this tmux pane's sandbox session after a tmux restart; list or preview saves",
		Long: "Typed in a pane, restore the sandbox session recorded for it: attach to its container\n" +
			"when it still runs, else resume its conversation in a new container (one restore starts\n" +
			"at a time). The row is the pane's own pending mark, else the save's (--from SAVE: that\n" +
			"save's). --drop forgets this pane's pending mark. --list lists the tmux-resurrect saves;\n" +
			"--dry-run shows what a restore would do in this pane, or in every pane of a save (--all).\n" +
			"SAVE is last (the default), previous (what the previous tmux server ended with), a stamp\n" +
			"such as 20260929T120000, or the name of a save in the resurrect dir.",
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
	f.BoolVar(&o.drop, "drop", false, "forget this pane's pending mark (nothing is restored)")
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

// runTmuxRestore checks the form (CS-TMUX-051 row 1, CS-TMUX-049,
// CS-TMUX-052) and runs it. Every refusal is exit 2 with one line. Every
// form here is typed by the operator, so each claims a pending sparse notice
// (CS-TMUX-050) before it does anything else.
func runTmuxRestore(env *Env, o restoreOpts, fromSet bool) error {
	if hostdirs.InSandbox(env.Getenv) {
		return exitErr(2, "Error: tmux restore runs on the host only (a sandbox has no tmux server)")
	}
	switch {
	case o.list && (o.dryRun || fromSet || o.drop):
		return exitErr(2, "Error: --list takes only --all")
	case o.drop && (o.dryRun || o.all || fromSet):
		return exitErr(2, "Error: --drop takes no other flag")
	case o.all && !o.dryRun && !o.list:
		return exitErr(2, "Error: --all needs --dry-run or --list (restoring every pane of a save is still to come)")
	}
	claimNotice(env)
	switch {
	case o.list:
		return runRestoreList(env, o.all)
	case o.drop:
		return runRestoreDrop(env)
	case o.dryRun && o.all:
		return runRestoreDryRunAll(env, o.from)
	case o.dryRun:
		return runRestoreDryRun(env, o.from)
	}
	return runRestoreAct(env, o.from)
}

// claimNotice prints a pending sparse-restore notice and, inside tmux,
// claims it (CS-TMUX-050): host only, every failure silent.
func claimNotice(env *Env) {
	if hostdirs.InSandbox(env.Getenv) {
		return
	}
	tmuxpane.ClaimNotice(tmuxpane.ClaimOptions{
		CacheDir: env.cacheDir(), Runner: env.Runner,
		InTmux: env.Getenv("TMUX") != "", Now: env.now(), Out: env.Err,
	})
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
		// A reservation older than the reclaim age is an orphan: the launch
		// the resume runs removes it under the launch lock before its own
		// guard runs (CS-SESS-052), so it must not hold the row here
		// either — or a pane whose launcher died between create and start
		// would stay pending forever (CS-TMUX-051 row 15).
		stale := map[string]bool{}
		for _, s := range sessions.Stale(found, env.now(), staleReservationAge) {
			stale[s.Name] = true
		}
		var orphans []string
		var sess []sessions.Session
		for _, s := range found {
			if stale[s.Name] {
				if strings.EqualFold(s.Resume, id) {
					orphans = append(orphans, s.Name)
				}
				continue
			}
			sess = append(sess, s)
		}
		v := resumeguard.Check{
			ID: id, Sessions: sess, DiscoveryErr: derr, Runner: env.Runner,
			Home: home, ConfigDir: cfg, ProcRoot: env.ProcRoot,
		}.Run()
		g := tmuxpane.GuardResult{Open: v.Open, HostPID: v.HostPID, Reason: v.Reason, Orphans: orphans}
		if h := v.Holder; h != nil {
			g.Holder = h.Name
			// A created, never-started reservation holds the id for the
			// guard but is no session: the row stays pending (row 15).
			g.HolderReserved = h.Reserved()
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
  one pane, typed in that pane:   claude-sandbox tmux restore --from %[1]s
  preview it first:               claude-sandbox tmux restore --dry-run --from %[1]s
  every pane of the save:         claude-sandbox tmux restore --dry-run --all --from %[1]s
  (restoring every pane at once is still to come; the dry run prints each pane's manual command)
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

// ---- F4b: acting in one pane (CS-TMUX-052..063) ----

// restoreHooks ride the Env copy a restore resumes with (plan 10 § 5.2):
// launchWith hands prior into the new container's pane mark, and
// startReserved calls onReserved with the reserved plan to get the session's
// onChild (the readiness watcher's start) and keepUnlessReady.
type restoreHooks struct {
	prior      string
	onReserved func(plan *launch.Plan) (onChild func(), keepUnlessReady func() bool)
	// early records that keepUnlessReady put the pending row back.
	early bool
}

// The restore's docker wait (CS-TMUX-055 row 8): a bounded "docker version"
// every restoreDockerPoll for up to restoreDockerWait, so a restore typed at
// boot outlasts a docker that starts after tmux. Variables for tests.
var (
	restoreDockerWait = 120 * time.Second
	restoreDockerPoll = 2 * time.Second
)

// restoreSetenv is the os.Setenv/os.Unsetenv backstop of a resume
// (CS-TMUX-058): the launch reads CLAUDE_CONFIG_DIR and PROJECT_DIR through
// the Env copy, but the docker client it runs inherits the process
// environment (execx), and resolves a bare env-file line against it. A
// variable so tests never change their own process environment.
var restoreSetenv = func(k string, v *string) {
	if testing.Testing() {
		// The cacheDir rule: a forgotten fixture fails loudly instead of
		// changing the test process's own environment.
		panic("a test reached the real os.Setenv backstop; replace restoreSetenv")
	}
	if v == nil {
		os.Unsetenv(k)
		return
	}
	os.Setenv(k, *v)
}

// interruptContext is the Ctrl-C context a restore waits under.
func (e *Env) interruptContext() (context.Context, context.CancelFunc) {
	if e.interrupt != nil {
		return e.interrupt()
	}
	if testing.Testing() {
		panic("a test reached the real SIGINT handler; set Env.interrupt")
	}
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// restoreSay prints one restore line.
func restoreSay(env *Env, format string, a ...any) {
	fmt.Fprintf(env.Out, "claude-sandbox: "+format+"\n", a...)
}

// runRestoreDrop is "--drop" (CS-TMUX-063): forget this pane's pending mark.
func runRestoreDrop(env *Env) error {
	paneID, ok := tmuxpane.FromEnv(env.Getenv)
	if !ok {
		return exitErr(2, "Error: tmux restore --drop runs in a tmux pane (TMUX_PANE is not set)")
	}
	tp, ok := tmuxpane.ReadThisPane(env.Runner, paneID)
	if !ok {
		return exitErr(2, "Error: tmux did not answer for pane %s", paneID)
	}
	switch {
	case !tp.Marked:
		restoreSay(env, "nothing to drop: pane %s has no claude-sandbox mark", tp.Coords())
		return nil
	case tp.Mark.State != tmuxpane.StatePending:
		restoreSay(env, "nothing to drop: pane %s holds a running session, not a pending one", tp.Coords())
		return nil
	}
	(tmuxpane.Pane{Runner: env.Runner, ID: paneID}).Unset()
	if tmuxpane.ValidateRow(tp.Mark) != "" {
		restoreSay(env, "dropped the pending mark of pane %s (it could not be used)", tp.Coords())
		return nil
	}
	conv := tp.Mark.Conversation
	if conv == "" {
		conv = "no conversation recorded"
	}
	restoreSay(env, "dropped the pending mark of pane %s: '%s' (%s); the shell is yours", tp.Coords(), tmuxpane.RowName(tp.Mark), conv)
	return nil
}

// actProbes are the acting restore's probes (CS-TMUX-052..061): the
// dry-run's, except that docker is waited for, and that the start lock is
// taken before the first look at the container or the resume guard (rows
// 9..18), so those checks and what follows run one restore at a time.
type actProbes struct {
	*tmuxpane.ReadProbes
	env      *Env
	ctx      context.Context
	lockPath string
	holder   string

	lock        *tmuxpane.StartLock
	lockErr     error
	interrupted bool
	info        tmuxpane.ContainerInfo
}

func (p *actProbes) Docker() error {
	deadline := time.Now().Add(restoreDockerWait)
	told := false
	for {
		err := tmuxpane.DockerAnswers(p.env.Runner)
		if err == nil || !time.Now().Before(deadline) {
			return err
		}
		if !told {
			told = true
			fmt.Fprintln(p.env.Err, "claude-sandbox: waiting for docker… (Ctrl-C to skip)")
		}
		select {
		case <-p.ctx.Done():
			p.interrupted = true
			return errors.New("interrupted")
		case <-time.After(restoreDockerPoll):
		}
	}
}

// locked takes the start lock once (CS-TMUX-060).
func (p *actProbes) locked() error {
	if p.lock != nil || p.lockErr != nil {
		return p.lockErr
	}
	l, err := tmuxpane.AcquireStartLock(p.ctx, p.lockPath, p.holder, func(holder string) {
		if holder == "" {
			holder = "another restore"
		}
		fmt.Fprintf(p.env.Err, "claude-sandbox: waiting for %s to start (Ctrl-C to skip)…\n", holder)
	})
	if err != nil {
		if p.ctx.Err() != nil {
			p.interrupted = true
		}
		p.lockErr = err
		return err
	}
	p.lock = l
	return nil
}

// release releases the start lock, if held; safe to call more than once.
func (p *actProbes) release() { p.lock.Release() }

func (p *actProbes) Inspect(id string) (tmuxpane.ContainerInfo, error) {
	if err := p.locked(); err != nil {
		return tmuxpane.ContainerInfo{}, fmt.Errorf("the restore start lock: %v", err)
	}
	info, err := p.ReadProbes.Inspect(id)
	p.info = info
	return info, err
}

func (p *actProbes) Guard(m tmuxpane.Mark) tmuxpane.GuardResult {
	if err := p.locked(); err != nil {
		return tmuxpane.GuardResult{Open: true, Reason: "the restore start lock: " + err.Error()}
	}
	return p.ReadProbes.Guard(m)
}

// runRestoreAct is the plain restore and "--from SAVE" in one pane
// (CS-TMUX-052..062).
func runRestoreAct(env *Env, from string) error {
	paneID, ok := tmuxpane.FromEnv(env.Getenv)
	if !ok {
		return exitErr(2, "Error: tmux restore runs in a tmux pane (TMUX_PANE is not set); --dry-run --all works anywhere")
	}
	tp, ok := tmuxpane.ReadThisPane(env.Runner, paneID)
	if !ok {
		return exitErr(2, "Error: tmux did not answer for pane %s", paneID)
	}
	coords := tp.Coords()
	pane := tmuxpane.Pane{Runner: env.Runner, ID: paneID}

	// CS-TMUX-052: the pane's own pending mark, else last's row at these
	// coordinates; --from reads that save only. Read once, before any wait.
	var row *tmuxpane.Row
	if from == "" && tp.Marked && tp.Mark.State == tmuxpane.StatePending {
		row = &tmuxpane.Row{Session: tp.Session, Window: tp.Window, Pane: tp.Pane, Mark: tp.Mark}
	} else {
		saves, idx, err := openRestoreSaves(env)
		if err != nil {
			return err
		}
		stamp, err := resolveFrom(saves, from, tp.Server, idx)
		if err != nil {
			return err
		}
		sc, err := saves.Sidecar(stamp)
		switch {
		case errors.Is(err, os.ErrNotExist):
			restoreSay(env, "save %s has no claude-sandbox record (the save hook was not wired then): nothing to restore in %s", stamp, coords)
			return nil
		case err != nil:
			restoreSay(env, "cannot read save %s (%v): nothing to restore in %s", stamp, err, coords)
			return nil
		}
		if line := saves.SparseOf(stamp, len(sc.Panes), sc.Server, idx).Line(); line != "" {
			fmt.Fprintln(env.Err, line)
		}
		for i := range sc.Panes {
			r := &sc.Panes[i]
			if r.Session == tp.Session && r.Window == tp.Window && r.Pane == tp.Pane {
				row = r
				break
			}
		}
	}
	if row == nil || tmuxpane.ValidateRow(row.Mark) != "" {
		// Rows 2 and 3: nothing recorded leaves the pane alone; a row that
		// cannot be used is cleared, naming the field.
		d := tmuxpane.Decide(row, coords, nil)
		if d.Outcome == tmuxpane.OutcomeClear {
			pane.Unset()
		}
		restoreSay(env, "%s", d.Line)
		return nil
	}

	// CS-TMUX-053: the pane is pending from the row before anything waits,
	// so a restore cut short at any point leaves it on the restore list.
	pend := row.Mark
	pend.State = tmuxpane.StatePending
	prior := pend.JSON()
	pane.Set(prior)

	ctx, stop := env.interruptContext()
	probes := &actProbes{
		ReadProbes: restoreProbes(env, paneID, nil),
		env:        env, ctx: ctx,
		lockPath: filepath.Join(env.cacheDir(), tmuxpane.StartLockFile),
		holder:   tmuxpane.StartLockHolder(coords, pend.Instance, os.Getpid()),
	}
	// Released on every return path; an attach releases it from onChild,
	// a resume once its session is up (CS-TMUX-060/061).
	defer probes.release()
	d := tmuxpane.Decide(row, coords, probes)
	// A Ctrl-C at any point of the decision skips the restore, not only one
	// that cut a wait short. From here the session child installs its own
	// signal handling.
	if ctx.Err() != nil {
		probes.interrupted = true
	}
	stop()
	if probes.interrupted {
		restoreSay(env, "skipped; pane %s stays pending. Retry: claude-sandbox tmux restore", coords)
		return exitErr(130, "")
	}

	switch d.Outcome {
	case tmuxpane.OutcomeClear:
		pane.Unset()
		restoreSay(env, "%s", d.Line)
		if d.Manual != "" {
			restoreSay(env, "by hand: %s", d.Manual)
		}
		return nil
	case tmuxpane.OutcomePending:
		probes.release()
		restorePending(env, d.Line, d.Manual)
		return nil
	case tmuxpane.OutcomeAttach:
		return restoreAttach(env, probes, pend, d, prior)
	case tmuxpane.OutcomeResume:
		return restoreResume(env, probes, pend, d, prior)
	}
	restoreSay(env, "%s", d.Line)
	return nil
}

// restorePending prints a pending outcome (CS-TMUX-055): the line, the retry
// and the exact manual command.
func restorePending(env *Env, line, manual string) {
	restoreSay(env, "%s", line)
	fmt.Fprintln(env.Out, "  The pane stays pending. Retry: claude-sandbox tmux restore")
	if manual != "" {
		fmt.Fprintln(env.Out, "  By hand:  "+manual)
	}
	fmt.Fprintln(env.Out, "  Forget it: claude-sandbox tmux restore --drop")
}

// restoreEnv is the Env copy a restore attaches or resumes with
// (CS-TMUX-058): CLAUDE_CONFIG_DIR as the row recorded it (unset for "", the
// shell's own when the row predates the record), and PROJECT_DIR cleared, so
// the project is the row's, taken from the working directory.
func restoreEnv(env *Env, m tmuxpane.Mark) *Env {
	r := *env
	get, look := env.Getenv, env.lookupEnv
	r.Getenv = func(k string) string {
		switch {
		case k == "PROJECT_DIR":
			return ""
		case k == "CLAUDE_CONFIG_DIR" && m.ConfigDirEnv != nil:
			return *m.ConfigDirEnv
		}
		return get(k)
	}
	r.LookupEnv = func(k string) (string, bool) {
		switch {
		case k == "PROJECT_DIR":
			return "", false
		case k == "CLAUDE_CONFIG_DIR" && m.ConfigDirEnv != nil:
			return *m.ConfigDirEnv, *m.ConfigDirEnv != ""
		}
		return look(k)
	}
	return &r
}

// restoreCascade resolves the project's config cascade as a launch would,
// for the attach's detach keys and drift check.
func restoreCascade(env *Env, project string) (*cascade.Config, []string, *launch.LinkedWorktree, error) {
	linked, _ := launch.DetectLinkedWorktree(env.Runner, project)
	mainCheckout := ""
	if linked != nil {
		mainCheckout = linked.Main
	}
	chain := paths.Chain(project, mainCheckout)
	configFiles, err := paths.CollectChain(chain, paths.Config)
	if err != nil {
		return nil, nil, nil, err
	}
	envFiles, err := paths.CollectChain(chain, paths.Env)
	if err != nil {
		return nil, nil, nil, err
	}
	snap, err := cascade.ReadConfigFiles(configFiles)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg, err := cascade.LoadSnapshot(snap)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, envFiles, linked, nil
}

// restoreAttach is row 9's attach (CS-TMUX-056): docker attach by the 64-hex
// id, from the row's project, with the project's detach keys; drift is one
// note, never a prompt. The start lock is released once the pane holds the
// active mark (onChild), so another pane on the same container then finds
// it on screen (CS-TMUX-057).
func restoreAttach(env *Env, p *actProbes, m tmuxpane.Mark, d tmuxpane.Decision, prior string) error {
	if err := os.Chdir(m.Project); err != nil {
		p.release()
		restorePending(env, fmt.Sprintf("cannot enter %s (%v)", m.Project, err), d.Manual)
		return nil
	}
	renv := restoreEnv(env, m)
	s := sessions.Session{Name: p.info.Name, ID: m.ContainerID, Project: m.Project, Instance: m.Instance,
		Mode: "claude", Status: "Up"}
	if found, err := sessions.Discover(env.Runner, m.Project); err == nil {
		for _, x := range found {
			if x.ID == m.ContainerID {
				s = x
				break
			}
		}
	}
	keys := ""
	cfg, envFiles, linked, err := restoreCascade(renv, m.Project)
	if err == nil {
		keys = cfg.DetachKeys
		if s.ConfigHash != "" {
			if want, _ := wouldBeFingerprint(renv, m.Project, &launchFlags{}, cfg, envFiles, linked, &s); want != "" && want != s.ConfigHash {
				fmt.Fprintf(env.Err, "Note: '%s' was started with a different configuration; attaching does not apply the changes (claude-sandbox --attach=%s lists them).\n",
					sessionLabel(s), s.Instance)
			}
		}
	}
	_, _, _, home := hostIdentity(renv.Getenv)
	mark := attachMark(s, home, launch.GitRoot(env.Runner, m.Project))
	mark.prior = &prior
	mark.restoreAttach = true
	restoreSay(env, "%s", d.Line)
	return attachTo(renv, s, keys, mark, p.release)
}

// restoreResume is row 18's resume (CS-TMUX-058..062): the normal launch
// path with --new and the row's identity plus its replay allowlist, under
// the start lock, which the readiness watcher releases once the session is
// up plus the layout's gap.
func restoreResume(env *Env, p *actProbes, m tmuxpane.Mark, d tmuxpane.Decision, prior string) error {
	for _, n := range d.Notes {
		restoreSay(env, "note: %s", n)
	}
	if err := os.Chdir(m.Project); err != nil {
		p.release()
		restorePending(env, fmt.Sprintf("cannot enter %s (%v)", m.Project, err), d.Manual)
		return nil
	}
	restoreSetenv("PROJECT_DIR", nil)
	if m.ConfigDirEnv != nil {
		if *m.ConfigDirEnv == "" {
			restoreSetenv("CLAUDE_CONFIG_DIR", nil)
		} else {
			restoreSetenv("CLAUDE_CONFIG_DIR", m.ConfigDirEnv)
		}
	}
	renv := restoreEnv(env, m)
	// The headless precedent (CS-LNCH-061): nothing prompts.
	renv.Prompter = &prompt.Fixed{Out: env.Err}
	id := strings.ToLower(m.Conversation)
	name := tmuxpane.RowName(m)
	var w *tmuxpane.Watcher
	started, capped := false, false
	hooks := &restoreHooks{prior: prior}
	hooks.onReserved = func(plan *launch.Plan) (func(), func() bool) {
		class, err := strconv.Atoi(plan.PIDClass)
		if err != nil {
			class = -1 // no record can match: up comes from the fallback
		}
		reserved := time.Now()
		onChild := func() {
			started = true
			w = tmuxpane.StartWatcher(tmuxpane.ReadyOptions{
				RegistryDir: plan.RegistryDir, Class: class, Since: reserved, ID: id,
				Gap: d.Gap, Release: p.release,
				// Recorded, printed after the session: the TUI owns the
				// terminal while it runs.
				OnCap: func() { capped = true },
			})
		}
		keep := func() bool {
			hooks.early = w.EndedEarly()
			return hooks.early
		}
		return onChild, keep
	}
	renv.restore = hooks
	restoreSay(env, "%s", d.Line)
	err := runLaunch(renv, tmuxpane.ResumeArgs(m))
	w.ChildReturned()
	p.release()
	if capped {
		restoreSay(env, "'%s' was not up after %d s; the next restore was let start then", name, int(tmuxpane.ReadyCap/time.Second))
	}
	switch {
	case !started:
		// CS-TMUX-058: the launch failed before docker start (an image
		// build, a refusal, the resume guard's exit 4): the pending mark set
		// before the decision is still there. The launch's own error goes
		// first, so the lines below follow it.
		var ce *execx.CodeError
		switch {
		case err == nil:
		case errorAs(err, &ce):
			if ce.Msg != "" && ce.Msg != fmt.Sprintf("exit %d", ce.Code) {
				fmt.Fprintln(env.Err, ce.Msg)
			}
			err = &execx.CodeError{Code: ce.Code}
		default:
			fmt.Fprintf(env.Err, "Error: %v\n", err)
			err = &execx.CodeError{Code: 2}
		}
		restorePending(env, fmt.Sprintf("the resume of '%s' (%s) did not start (see above)", name, id), d.Manual)
	case !hooks.early && err != nil && !w.Up():
		// The session child could not be run, or the reservation never
		// started (CS-TMUX-018): the pending row went back; docker said why.
		restorePending(env, fmt.Sprintf("the resume of '%s' (%s) did not start (see above)", name, id), d.Manual)
	case hooks.early:
		cfg := m.ConfigDir
		if cfg == "" {
			cfg = "its config dir"
		}
		restoreSay(env, "the resume of '%s' (%s) ended before it was up (exit %d) — the conversation may be missing from %s, "+
			"or claude failed to start (see above). The pane stays pending: fix it and run claude-sandbox tmux restore, "+
			"or drop it with claude-sandbox tmux restore --drop", name, id, max(execx.ExitCode(err), 0), cfg)
	}
	return err
}
