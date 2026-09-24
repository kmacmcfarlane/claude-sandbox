// Package launch assembles the container invocation: mounts, shadow-file
// injections, host-access resolution, the container command, and the
// create-then-start of the session (CS-LNCH-057, CS-LNCH-085).
// Spec: spec/launch.feature (CS-LNCH).
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
)

// Inputs collects everything needed to assemble the docker create argv.
type Inputs struct {
	ProjectDir string
	Home       string
	HostUID    int
	HostGID    int
	HostUser   string

	// Getenv is the environment seam (defaults to os.Getenv).
	Getenv func(string) string

	// TempDir hosts the shadow files; "" makes a fresh one (NewShadowDir).
	TempDir string

	RalphMode       bool
	Limit           string
	SkipPermissions bool
	CLIModel        string
	Passthrough     []string

	// Instance is the noun identifying this session among the project's
	// containers (CS-LNCH-026). Empty for ralph, which is single-instance.
	Instance string

	// Worktree is the name handed to claude's --worktree (CS-LNCH-041); empty
	// means the session works in the shared checkout. Rendered as the
	// claude-sandbox.worktree label and, like Instance, excluded from the
	// fingerprint (CS-LNCH-044).
	Worktree string

	// Version stamps the claude-sandbox.version label.
	Version string

	// MemoryLimitSource is the cascade file that set memoryLimit
	// (cascade.MemoryLimitSource), "" when none did. Recorded on the
	// container, never hashed (CS-LNCH-093).
	MemoryLimitSource string
	// OOMScoreAdjSource is the cascade file that set oomScoreAdj
	// (cascade.KeySource), named when its value is invalid (CS-LNCH-112).
	OOMScoreAdjSource string

	// Chmod restricts a launcher-owned peer-registry directory (CS-LNCH-051).
	// Nil means fchmod on the descriptor hostdirs.EnsureOwnedDir checked;
	// when set it is called with that descriptor's path instead. Tests inject
	// a failure (CS-LNCH-107).
	Chmod func(string, os.FileMode) error

	// Linked is the verified linked git worktree the project lies in, nil
	// otherwise (CS-LNCH-070). Its common git dir is mounted read-write at
	// its own path so git works in the container (CS-LNCH-071).
	Linked *LinkedWorktree

	// PIDClass is the container's pid class (CS-PID-004), rendered as the
	// claude-sandbox.pidclass label and CLAUDE_SANDBOX_PID_CLASS. Empty
	// emits neither. Like Instance it is excluded from the fingerprint.
	PIDClass string

	// Headless marks a launch for an SDK client (CS-LNCH-058..067): the
	// container gets -i without -t, no detach keys, the mode=headless label and
	// the HeadlessEnv allowlist.
	Headless bool

	// Detached marks a --detach launch (CS-LNCH-113): the container is
	// created exactly as an attached one, -t included, and carries the
	// LabelDetached label. It is a per-session choice, outside the config
	// hash (CS-LNCH-118).
	Detached bool

	// LookupEnv tells set-but-empty from unset for the headless env allowlist
	// (CS-LNCH-063). Nil falls back to Getenv, where "" reads as unset.
	LookupEnv func(string) (string, bool)

	// ImageID is the resolved image's docker ID. It feeds the config hash so an
	// out-of-band rebuild registers as drift (CS-SESS-023).
	ImageID string

	// CLI host-access overrides (nil = not passed).
	CLISSH, CLIGit, CLIDockerSocket, CLIAWS *bool
	CLIPackageCaches                        *bool

	Cfg      *cascade.Config
	EnvFiles []string
	// Env is the cascade env files snapshotted ONCE by the caller, the bytes
	// its refusal check saw (CS-LNCH-129, CS-LNCH-132). Build writes a
	// verbatim 0600 copy of each into the shadow directory and passes THOSE
	// to docker create, so a session-writable file changed between the check
	// and the create never reaches docker. Nil: Build snapshots EnvFiles
	// itself (drift checks, tests) — still one read for the whole build.
	Env []cascade.EnvFile

	ImageName string
	Out       io.Writer
	Err       io.Writer

	// shadowDigests accumulates a content digest per generated shadow file, so
	// the config hash tracks what was actually mounted rather than the temp
	// paths those files happen to live at (which differ every launch).
	shadowDigests []InputDigest
}

// ModeHeadless is the claude-sandbox.mode label value of a headless container
// (CS-LNCH-064); discovery keeps such containers out of attach and join.
const ModeHeadless = "headless"

// LabelKeep marks a kept container — one created without --rm, with a
// restart policy — and records that policy. Discovery lists an exited or
// restarting container only when it carries this label (CS-SESS-070); a --rm
// container is only ever exited while docker removes it. Like every label it
// is outside the config hash.
const LabelKeep = "claude-sandbox.keep"

// LabelDetached marks a container launched with --detach (CS-LNCH-118), so
// later tooling (restore) can tell it was started with no client and relaunch
// it the same way. Set only on detached launches; an attached launch's argv is
// unchanged. Its mode label stays "claude": a detached session is an ordinary
// attach and join candidate.
const LabelDetached = "claude-sandbox.detached"

// HeadlessEnv is the exact list of variables a headless launch forwards from
// its own environment (CS-LNCH-063): what the Claude Agent SDK and Paseo set
// for the claude process they spawn. It is a list of names, never a prefix: a
// Paseo daemon's environment can hold PASEO_PASSWORD.
var HeadlessEnv = []string{
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING",
	"CLAUDE_AGENT_SDK_VERSION",
	"CLAUDE_AGENT_SDK_CLIENT_APP",
	"CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS",
	"CLAUDE_AGENT_SDK_MCP_NO_PREFIX",
	"PASEO_AGENT_ID",
	"PASEO_AGENT_CWD",
}

// DefaultDetachKeys is the sequence that detaches a session without stopping
// it. It must be applied to EVERY interactive docker invocation — `start`
// (the primary session), `attach` and `exec` alike — because docker's own default otherwise takes
// over, and docker's default is `ctrl-p,ctrl-q` while the Claude Code TUI binds
// ctrl+p. Requiring ctrl-q twice makes an accidental detach implausible;
// `-it` puts the terminal in raw mode, so XON/XOFF flow control does not eat it.
const DefaultDetachKeys = "ctrl-q,ctrl-q"

// DefaultMemoryLimit is the container memory limit when no config.yaml in
// the cascade sets memoryLimit (CS-LNCH-022).
const DefaultMemoryLimit = "8g"

// DefaultOOMScoreAdj is the container's oom_score_adj when neither
// CLAUDE_SANDBOX_OOM_SCORE_ADJ nor the oomScoreAdj key sets one (CS-LNCH-112).
// The kernel's global OOM killer ranks a process by its share of RAM + swap in
// permille plus oom_score_adj; the desktop session runs at 100 (gnome-shell on
// Fedora), sandbox memory is spread over many ~0.5 GB processes, so at docker's
// 0 the desktop always died first. 500 puts every sandbox process ahead of any
// host process at adj <= 100 until that one process holds ~40% of RAM + swap —
// a host runaway that large is still killed before the sandboxes. It shifts all
// of a container's processes equally, so the order inside a container and its
// own memoryLimit OOM are unchanged.
const DefaultOOMScoreAdj = 500

// OOMScoreAdjEnv overrides the oomScoreAdj key for one launch.
const OOMScoreAdjEnv = "CLAUDE_SANDBOX_OOM_SCORE_ADJ"

// ResolveOOMScoreAdj applies CLAUDE_SANDBOX_OOM_SCORE_ADJ > oomScoreAdj >
// DefaultOOMScoreAdj and validates docker's range (CS-LNCH-112). An empty env
// value falls through to the key, as unset. source is the config file that
// set the key (cascade.KeySource), named in the error. The launcher calls it
// before any image work so a bad value never costs a build; Build calls it
// again as a backstop.
func ResolveOOMScoreAdj(getenv func(string) string, cfg *cascade.Config, source string) (int, error) {
	if raw := strings.TrimSpace(getenv(OOMScoreAdjEnv)); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < -1000 || v > 1000 {
			return 0, fmt.Errorf("%s=%q: must be an integer in [-1000, 1000]", OOMScoreAdjEnv, raw)
		}
		return v, nil
	}
	if cfg != nil && cfg.OOMScoreAdj != nil {
		v := *cfg.OOMScoreAdj
		if v < -1000 || v > 1000 {
			if source == "" {
				source = "the config cascade"
			}
			return 0, fmt.Errorf("oomScoreAdj: %d in %s: must be an integer in [-1000, 1000]", v, source)
		}
		return v, nil
	}
	return DefaultOOMScoreAdj, nil
}

func (in *Inputs) resolveOOMScoreAdj() (int, error) {
	return ResolveOOMScoreAdj(in.getenv, in.Cfg, in.OOMScoreAdjSource)
}

// ResolveDetachKeys applies the configured override, falling back to the
// default. Every caller must route through here so the three docker paths
// cannot disagree about which keys detach.
func ResolveDetachKeys(configured string) string {
	if s := strings.TrimSpace(configured); s != "" {
		return s
	}
	return DefaultDetachKeys
}

// Plan is the assembled invocation.
type Plan struct {
	Image         string
	ContainerName string
	Volumes       []string // -v specs
	EnvFlags      []string // -e KEY=VAL, or bare KEY read from the launcher env (CS-LNCH-102)
	EnvFiles      []string // --env-file paths
	MemoryLimit   string
	Command       []string // command + args inside the container
	Labels        []string // --label KEY=VAL specs
	DetachKeys    string   // --detach-keys sequence, for docker start only
	// Headless renders create with -i but no -t (CS-LNCH-059).
	Headless bool
	// Instance is the container's instance noun ("" for ralph), for messages
	// such as the --detach attach hint (CS-LNCH-113).
	Instance string
	// MemoryLimitSource is where MemoryLimit came from: a config.yaml path,
	// oomreport.SourceDefault, or "" when the caller did not say (CS-LNCH-093).
	MemoryLimitSource string
	// OOMScoreAdj is the container's --oom-score-adj (CS-LNCH-112).
	OOMScoreAdj int
	// ShadowDir is the directory holding this launch's shadow files, also
	// recorded as the claude-sandbox.shadowdir label (CS-LNCH-080).
	ShadowDir string

	// ConfigHash identifies the effective configuration this container was
	// launched with; ConfigInputs records the contributing files so drift can
	// be explained rather than merely reported (CS-SESS-020, CS-SESS-021).
	ConfigHash   string
	ConfigInputs []InputDigest
}

// lookupEnv reports whether k is set in the launcher's environment.
func (in *Inputs) lookupEnv(k string) bool {
	_, ok := in.lookupEnvValue(k)
	return ok
}

func (in *Inputs) getenv(k string) string {
	if in.Getenv != nil {
		return in.Getenv(k)
	}
	return os.Getenv(k)
}

// ConfigDir resolves the Claude config directory (CLAUDE_CONFIG_DIR or
// $HOME/.claude).
func (in *Inputs) ConfigDir() string {
	if d := in.getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(in.Home, ".claude")
}

// Build assembles the launch plan. Shadow files are written under in.TempDir.
func Build(in Inputs) (*Plan, error) {
	if in.Cfg == nil {
		in.Cfg = &cascade.Config{}
	}
	p := &Plan{Image: in.ImageName, Headless: in.Headless, Instance: in.Instance}

	// CS-LNCH-132: one snapshot for everything below — the stand-downs
	// (CS-LNCH-108), the fingerprint and the copies docker gets.
	if in.Env == nil && len(in.EnvFiles) > 0 {
		snap, err := cascade.ReadEnvFiles(in.EnvFiles)
		if err != nil {
			return nil, fmt.Errorf("reading env file: %w", err)
		}
		in.Env = snap
	}

	// CS-LNCH-007: project at its real host path.
	p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", in.ProjectDir, in.ProjectDir))

	// CS-LNCH-008: Claude config dir at its real path.
	configDir := in.ConfigDir()
	if dirExists(configDir) {
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", configDir, configDir))
	}

	// CS-LNCH-009: direnv allow-records, read-only (security boundary).
	direnv := filepath.Join(in.Home, ".local/share/direnv")
	if dirExists(direnv) {
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", direnv, direnv))
	}

	// CS-LNCH-010: CLAUDE.md shadow (host memory + container context).
	if err := in.shadowClaudeMD(p, configDir); err != nil {
		return nil, err
	}
	// CS-LNCH-011: settings.json is NOT shadowed — it reaches the container
	// live through the config-dir bind above. The notification hooks ship as
	// managed settings baked into the tools image and copied in by the cap
	// (CS-LNCH-068, CS-IMG-024).
	// CS-LNCH-012/013: siblings of the config dir.
	if err := in.shadowSiblings(p, configDir); err != nil {
		return nil, err
	}

	// CS-LNCH-014: host access precedence CLI > env var > YAML.
	ssh := resolveFlag(in.CLISSH, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED"), in.Cfg.HostAccess.SSH.Enabled)
	git := resolveFlag(in.CLIGit, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED"), in.Cfg.HostAccess.Git.Enabled)
	dockerSocket := resolveFlag(in.CLIDockerSocket, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED"), in.Cfg.HostAccess.DockerSocket.Enabled)
	aws := resolveFlag(in.CLIAWS, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED"), in.Cfg.HostAccess.AWS.Enabled)
	packageCaches := resolveFlag(in.CLIPackageCaches, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED"), in.Cfg.HostAccess.PackageCaches.Enabled)

	dockerGID := ""
	if dockerSocket {
		p.Volumes = append(p.Volumes, "/var/run/docker.sock:/var/run/docker.sock")
		dockerGID = socketGID("/var/run/docker.sock")
	}
	if aws {
		in.assembleAWS(p)
	}
	if packageCaches {
		if err := in.assemblePackageCaches(p); err != nil {
			return nil, err
		}
	}
	if git {
		if err := in.shadowGitconfig(p); err != nil {
			return nil, err
		}
	}
	if ssh {
		sshDir := filepath.Join(in.Home, ".ssh")
		if dirExists(sshDir) {
			p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", sshDir, sshDir))
		}
	}

	// CS-LNCH-021: extra mounts from the merged cascade.
	for i, m := range in.Cfg.Mounts {
		if m.Container == in.ProjectDir {
			fmt.Fprintf(in.Out, "Skipping mounts[%d]: container path '%s' duplicates project directory mount\n", i, m.Container)
			continue
		}
		if m.Writable {
			p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", m.Host, m.Container))
		} else {
			p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", m.Host, m.Container))
		}
	}

	// CS-LNCH-133..139: container-private pre-commit cache. Always on. After
	// the cascade mounts, so one that already covers it wins (CS-LNCH-139).
	in.assemblePreCommitCache(p)

	// CS-LNCH-071: a linked worktree's .git file names a git dir inside the
	// repository's common dir, outside the project mount. Read-write — git
	// writes objects, refs and the worktree's index there — which is the
	// power over that .git (hooks, refs, config) a session in the main
	// checkout already has. After the cascade mounts, so a same-path entry
	// that already covers it wins; the normalized mount set carries it into
	// the fingerprint.
	if in.Linked.MountsCommonDir(in.ProjectDir) {
		if cover, ok := samePathMountOf(p.Volumes, in.Linked.CommonDir); !ok {
			p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", in.Linked.CommonDir, in.Linked.CommonDir))
		} else if strings.HasSuffix(cover, ":ro") {
			fmt.Fprintf(in.Err, "WARNING: git dir %s is under the read-only mount %s; git cannot write to the repository in this session.\n", in.Linked.CommonDir, cover)
		}
	}

	// CS-LNCH-069: a symlinked settings.json keeps working. After the cascade
	// mounts, so a same-path cascade mount that already covers the target wins.
	in.mountSettingsTarget(p, configDir)

	// CS-LNCH-022: memory limit.
	p.MemoryLimit, p.MemoryLimitSource = in.Cfg.MemoryLimit, in.MemoryLimitSource
	if p.MemoryLimit == "" {
		p.MemoryLimit, p.MemoryLimitSource = DefaultMemoryLimit, oomreport.SourceDefault
	}
	// CS-LNCH-112: sandboxes are the host's preferred global-OOM victims.
	adj, err := in.resolveOOMScoreAdj()
	if err != nil {
		return nil, err
	}
	p.OOMScoreAdj = adj

	// CS-LNCH-033: detach keys for the primary session. A headless session has
	// no terminal, and detach keys would swallow bytes of its stream-json
	// input (CS-LNCH-059).
	if !in.Headless {
		p.DetachKeys = ResolveDetachKeys(in.Cfg.DetachKeys)
	}

	// Env files (root-first; later wins). CS-LNCH-132: docker gets verbatim
	// copies of the snapshot, written 0600 into the shadow directory (outside
	// the project tree), never the session-writable originals.
	for i, ef := range in.Env {
		copyPath, err := in.envCopy(i, ef.Content)
		if err != nil {
			return nil, fmt.Errorf("copying env file %s: %w", ef.Path, err)
		}
		p.EnvFiles = append(p.EnvFiles, copyPath)
	}

	// CS-LNCH-023: model precedence CLI > YAML.
	model := in.CLIModel
	if model == "" {
		model = in.Cfg.Model
	}

	// CS-LNCH-026..028: container command and name.
	slug := imagebuild.ProjectSlug(in.ProjectDir)
	if in.RalphMode {
		p.ContainerName = "claude-sandbox-" + slug + "-ralph"
		p.Command = []string{"/opt/claude-sandbox/bin/ralph"}
		if in.Limit != "" {
			p.Command = append(p.Command, "--limit", in.Limit)
		}
		if in.SkipPermissions {
			p.Command = append(p.Command, "--dangerously-skip-permissions")
		}
	} else {
		// The instance noun distinguishes concurrent sessions in one project.
		p.ContainerName = "claude-sandbox-" + slug + "-" + in.Instance
		p.Command = []string{"claude"}
		if in.SkipPermissions {
			p.Command = append(p.Command, "--dangerously-skip-permissions")
		}
	}
	// CS-LNCH-041/045: launcher-owned flags precede --model and the
	// passthrough tail, so a passthrough claude flag can still override them.
	if in.Worktree != "" {
		p.Command = append(p.Command, "--worktree", in.Worktree)
	}
	if model != "" {
		p.Command = append(p.Command, "--model", model)
	}
	p.Command = append(p.Command, in.Passthrough...)

	// CS-LNCH-029: container runtime environment.
	p.EnvFlags = append(p.EnvFlags,
		fmt.Sprintf("HOST_UID=%d", in.HostUID),
		fmt.Sprintf("HOST_GID=%d", in.HostGID),
		fmt.Sprintf("HOST_USER=%s", in.HostUser),
		fmt.Sprintf("HOST_HOME=%s", in.Home),
		fmt.Sprintf("HOME=%s", in.Home),
		fmt.Sprintf("DOCKER_GID=%s", dockerGID),
		// CS-LNCH-047: a session inside .claude/worktrees/<name> cannot
		// otherwise tell where the project root (and .claude-sandbox/) is.
		"CLAUDE_SANDBOX_PROJECT_DIR="+in.ProjectDir,
	)
	// CS-LNCH-102: a credential is forwarded as a bare "-e NAME", never
	// "-e NAME=value" — argv is world-readable through ps and /proc while
	// docker create runs. The docker client resolves a bare name from the
	// environment it inherits from the launcher (execx.System leaves Env
	// nil), so the container sees the same value.
	// CS-LNCH-106: unset gets no -e at all. -e outranks --env-file, so the
	// "ANTHROPIC_API_KEY=" an unset key used to produce (a leftover of the bash
	// launcher's ${ANTHROPIC_API_KEY:-} passthrough) blanked a key set in the
	// .claude-sandbox/env cascade.
	if in.lookupEnv("ANTHROPIC_API_KEY") {
		p.EnvFlags = append(p.EnvFlags, "ANTHROPIC_API_KEY")
	}
	if d := in.getenv("CLAUDE_CONFIG_DIR"); d != "" {
		p.EnvFlags = append(p.EnvFlags, "CLAUDE_CONFIG_DIR="+d)
	}
	// CS-LNCH-063: the SDK client's own variables, by exact name and as bare
	// "-e NAME" so their values never appear in argv (docker create reads
	// them from the environment it inherits from the launcher).
	if in.Headless {
		for _, k := range HeadlessEnv {
			if in.lookupEnv(k) {
				p.EnvFlags = append(p.EnvFlags, k)
			}
		}
	}

	// CS-LNCH-034: durable scratchpad root. Claude Code roots its per-session
	// scratchpad (and background-task output) at $CLAUDE_CODE_TMPDIR, falling
	// back to /tmp — which is the container's writable layer, destroyed at
	// session exit because the container runs with --rm. Point the root inside
	// the config-dir mount (CS-LNCH-008) so scratch state survives the
	// container and `claude --resume` finds it again; the CLI partitions
	// beneath the root by uid, project and session id, so one root is shared
	// safely. Precedence: host env > env file (docker -e would silently beat
	// --env-file, so stand down) > derived default.
	switch {
	case in.getenv("CLAUDE_CODE_TMPDIR") != "":
		v := in.getenv("CLAUDE_CODE_TMPDIR")
		p.EnvFlags = append(p.EnvFlags, "CLAUDE_CODE_TMPDIR="+v)
		if !underAnyMount(p.Volumes, v) {
			fmt.Fprintf(in.Out, "Warning: CLAUDE_CODE_TMPDIR=%s is not under any container mount; scratchpad files will be lost when the session exits.\n", v)
		}
	case in.envFilesDefine("CLAUDE_CODE_TMPDIR"):
		// The env file supplies it; adding -e would override the file.
	case dirExists(configDir):
		p.EnvFlags = append(p.EnvFlags, "CLAUDE_CODE_TMPDIR="+filepath.Join(configDir, "tmp"))
	}

	// CS-LNCH-049..055, 107: opt-in bridge for peer discovery and messaging across
	// containers whose CLAUDE_CONFIG_DIR differs. Default off: with the key
	// unset this adds nothing and the argv is unchanged.
	// Tri-state, not the OR shape of `dangerous`: an operator whose workspace
	// config sets sharedPeerRegistry true must be able to keep ONE session off
	// the shared registry with CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0. A
	// fall-through there would silently cross the boundary they just opted out
	// of. Same argument as worktree mode (CS-LNCH-042).
	sharedPeers := ResolveTristate(nil, in.getenv("CLAUDE_SANDBOX_SHARED_PEER_REGISTRY"), in.Cfg.SharedPeerRegistry, false)
	// bridged is what was APPLIED: a session that stands down (CS-LNCH-054/055/107)
	// launches exactly as with the key off, so it hashes like one.
	bridged := false
	if sharedPeers {
		bridged = in.assembleSharedPeerRegistry(p, configDir)
	}

	// CS-SESS-020/021: hash the effective configuration, then record it and the
	// contributing files on the container so a later attach can tell whether it
	// is joining a container built from the config now on disk.
	p.ConfigHash, p.ConfigInputs = in.configFingerprint(p, hostAccess{
		SSH: ssh, Git: git, DockerSocket: dockerSocket, AWS: aws, PackageCaches: packageCaches,
	}, bridged)

	// CS-LNCH-032: identity labels. Discovery filters on these rather than
	// parsing container names, which are lossy.
	mode := "claude"
	switch {
	case in.RalphMode:
		mode = "ralph"
	case in.Headless:
		mode = ModeHeadless // CS-LNCH-064
	}
	p.Labels = append(p.Labels,
		"claude-sandbox.project="+in.ProjectDir,
		"claude-sandbox.mode="+mode,
		"claude-sandbox.version="+in.Version,
		"claude-sandbox.model="+model,
		"claude-sandbox.confighash="+p.ConfigHash,
		"claude-sandbox.inputs="+encodeInputs(p.ConfigInputs),
		// CS-LNCH-044: empty when off, so `sessions` and attach can tell.
		"claude-sandbox.worktree="+in.Worktree,
	)
	if in.Instance != "" {
		p.Labels = append(p.Labels, "claude-sandbox.instance="+in.Instance)
	}
	if in.Detached {
		p.Labels = append(p.Labels, LabelDetached+"=1") // CS-LNCH-118
	}
	// CS-LNCH-111: the container's own identity, for code inside it — the
	// baked Notification hook posts it so an operator running a dozen
	// sandboxes can tell which one is waiting and how to reach it. Env vars
	// only: the instance noun is a per-session choice and is deliberately out
	// of the config hash (see fingerprint.go), and EnvFlags are not hashed at
	// all, so none of this can register as drift. A joined session (docker
	// exec) and every ralph iteration inherit the container's environment; a
	// join adds CLAUDE_SANDBOX_JOINED=1 on its own exec (CS-SESS-075).
	// CLAUDE_SANDBOX_INSTANCE and CLAUDE_SANDBOX_MODE mirror their labels: the
	// noun is unset for ralph, which is single-instance and has none. The
	// container name is always set. reserveContainer re-runs Build after a
	// noun re-pick, so these never go stale.
	if in.Instance != "" {
		p.EnvFlags = append(p.EnvFlags, "CLAUDE_SANDBOX_INSTANCE="+in.Instance)
	}
	p.EnvFlags = append(p.EnvFlags,
		"CLAUDE_SANDBOX_CONTAINER="+p.ContainerName,
		"CLAUDE_SANDBOX_MODE="+mode,
	)
	// CS-LNCH-039: the pid class rides a label (for allocation) and an env
	// var (for the in-container helper). EnvFlags are not fingerprinted, and
	// the label is not either, so a fresh class is never drift (CS-LNCH-040).
	if in.PIDClass != "" {
		p.Labels = append(p.Labels, "claude-sandbox.pidclass="+in.PIDClass)
		p.EnvFlags = append(p.EnvFlags, "CLAUDE_SANDBOX_PID_CLASS="+in.PIDClass)
	}
	// CS-LNCH-093: the memory limit and its source, for the OOM report of a
	// later attach or join (read back from the event stream's labels) and for
	// code inside the container. Labels and EnvFlags are outside the config
	// hash: the value is already hashed ("memory="), and where in the cascade
	// it is written is not a property of the container.
	p.Labels = append(p.Labels,
		oomreport.LabelMemoryLimit+"="+p.MemoryLimit,
		oomreport.LabelMemoryLimitSource+"="+p.MemoryLimitSource)
	p.EnvFlags = append(p.EnvFlags,
		"CLAUDE_SANDBOX_MEMORY_LIMIT="+p.MemoryLimit,
		"CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE="+p.MemoryLimitSource)
	// CS-LNCH-080: name the shadow directory, so a later launch's sweep knows
	// this container still uses it. Labels are outside the config hash.
	if in.TempDir != "" {
		p.ShadowDir = in.TempDir
		p.Labels = append(p.Labels, LabelShadowDir+"="+in.TempDir)
	}

	return p, nil
}

// CreateArgs renders the plan as the "docker create" argv: every flag the old
// single "docker run" carried — -it, --rm, --init, mounts, env, labels, the
// name, the image and the command — minus the detach keys, which belong to
// the client that attaches (StartArgs). CS-LNCH-057. A headless container gets
// -i without -t: with a TTY docker merges stderr into stdout and writes CR
// line endings, which corrupts a stream-json channel (CS-LNCH-059).
func (p *Plan) CreateArgs(workdir string) []string {
	tty := "-it"
	if p.Headless {
		tty = "-i"
	}
	args := []string{"create", tty, "--rm", "--init"}
	for _, v := range p.Volumes {
		args = append(args, "-v", v)
	}
	args = append(args, "-w", workdir)
	for _, ef := range p.EnvFiles {
		args = append(args, "--env-file", ef)
	}
	args = append(args, "--memory", p.MemoryLimit, "--memory-swap", p.MemoryLimit)
	// CS-LNCH-112: runc applies it to the init and to every docker exec.
	args = append(args, "--oom-score-adj", strconv.Itoa(p.OOMScoreAdj))
	for _, e := range p.EnvFlags {
		args = append(args, "-e", e)
	}
	for _, l := range p.Labels {
		args = append(args, "--label", l)
	}
	args = append(args, "--name", p.ContainerName, p.Image)
	args = append(args, p.Command...)
	return args
}

// StartArgs renders the "docker start" argv that attaches to the reserved
// container. The primary session needs detach keys as much as attach does:
// without them docker's ctrl-p,ctrl-q applies, which the TUI collides with.
func (p *Plan) StartArgs() []string {
	args := []string{"start", "-ai"}
	if p.DetachKeys != "" {
		args = append(args, "--detach-keys="+p.DetachKeys)
	}
	return append(args, p.ContainerName)
}

// ErrNameConflict marks a "docker create" refused because the container name
// is taken (CS-SESS-053). Docker checks names atomically, so this is the one
// failure a re-pick can fix.
var ErrNameConflict = errors.New("container name already in use")

// CreateError is a failed "docker create": docker's own message plus, when the
// name was the problem, ErrNameConflict.
type CreateError struct {
	Name     string
	Stderr   string
	Code     int
	conflict bool
}

func (e *CreateError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit %d", e.Code)
	}
	return fmt.Sprintf("docker create %s failed: %s", e.Name, msg)
}

func (e *CreateError) Unwrap() error {
	if e.conflict {
		return ErrNameConflict
	}
	return nil
}

// Reserve creates the container without starting it (CS-LNCH-057). The name,
// and with it the instance noun and pid class on its labels, is taken
// atomically: docker refuses a second create with the same name. Anything
// docker prints on success (kernel capability warnings) is forwarded to warn.
func (p *Plan) Reserve(r execx.Runner, workdir string, warn io.Writer) error {
	var stderr strings.Builder
	err := r.Run(execx.Cmd{Name: "docker", Args: p.CreateArgs(workdir), Stderr: &stderr})
	if err == nil {
		if warn != nil && stderr.Len() > 0 {
			io.WriteString(warn, stderr.String())
		}
		return nil
	}
	return &CreateError{
		Name: p.ContainerName, Stderr: stderr.String(), Code: execx.ExitCode(err),
		conflict: isNameConflict(stderr.String()),
	}
}

// isNameConflict recognises docker's refusal of a taken name:
//
//	Error response from daemon: Conflict. The container name "/x" is already in use by container "…"
//
// and nothing else. A bare "Conflict" would also match docker's unrelated
// "Conflicting options: …" flag errors, which a re-pick cannot fix; retrying
// those would end in a false "name in use" report (CS-SESS-053).
func isNameConflict(stderr string) bool {
	return strings.Contains(stderr, "Conflict. The container name") ||
		(strings.Contains(stderr, "The container name") && strings.Contains(stderr, "is already in use"))
}

// StartCmd is the session child that attaches to the reserved container,
// "docker start -ai" (CS-LNCH-057), whose exit code is the container's. The
// launcher runs it as a child and waits (CS-LNCH-085).
func (p *Plan) StartCmd() execx.Cmd {
	return execx.Cmd{Name: "docker", Args: p.StartArgs()}
}

// DetachedStartArgs renders the "docker start" of a --detach launch
// (CS-LNCH-113): no -a and no -i, so nothing attaches, and so no detach keys
// either — they belong to the later "docker attach".
func (p *Plan) DetachedStartArgs() []string {
	return []string{"start", p.ContainerName}
}

// shadowDirFor returns the shadow directory, making one when none was given.
func (in *Inputs) shadowDirFor() (string, error) {
	if in.TempDir == "" {
		d, err := NewShadowDir("")
		if err != nil {
			return "", err
		}
		in.TempDir = d
	}
	return in.TempDir, nil
}

// envCopy writes the i-th snapshotted env file verbatim into the shadow
// directory, mode 0600 (env files hold secrets), and returns its path
// (CS-LNCH-132). Not through tempFile: the content is already in the
// fingerprint as KindEnv under its original path (configFingerprint), and a
// second digest under a shadow name would double-count it.
func (in *Inputs) envCopy(i int, content []byte) (string, error) {
	dir, err := in.shadowDirFor()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("env-%d", i))
	return p, os.WriteFile(p, content, 0o600)
}

func (in *Inputs) tempFile(name string, content []byte) (string, error) {
	dir, err := in.shadowDirFor()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	// Every shadow file is written through here, so this is the one place that
	// needs to record what was mounted. The digest is of the CONTENT: the temp
	// path itself is fresh on every launch and must never reach the hash.
	in.shadowDigests = append(in.shadowDigests, InputDigest{
		Path: name, Digest: shortDigest(content), Kind: KindShadow,
	})
	return p, os.WriteFile(p, content, 0o644)
}

func (in *Inputs) shadowClaudeMD(p *Plan, configDir string) error {
	var buf strings.Builder
	if raw, err := os.ReadFile(filepath.Join(configDir, "CLAUDE.md")); err == nil {
		// Host memory + a blank line separator; without a host file the temp
		// file is container-context.md alone (CS-LNCH-010).
		buf.Write(raw)
		buf.WriteString("\n")
	}
	buf.Write(assets.ContainerContext)
	tmp, err := in.tempFile("CLAUDE.md", []byte(buf.String()))
	if err != nil {
		return err
	}
	p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", tmp, filepath.Join(configDir, "CLAUDE.md")))
	return nil
}

// mountSettingsTarget handles a settings.json that is a symlink (CS-LNCH-069).
// The config-dir bind carries the link itself, so a link pointing outside
// every mount would dangle in the container and Claude Code would run with no
// user settings, logging that only at debug level. The fully resolved target
// is bind-mounted read-write at its own path so the link resolves identically
// inside; a target already under a same-path mount needs nothing.
func (in *Inputs) mountSettingsTarget(p *Plan, configDir string) {
	link := filepath.Join(configDir, "settings.json")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		dest, _ := os.Readlink(link)
		fmt.Fprintf(in.Err, "WARNING: %s is a symlink to %s, which does not resolve; the sandbox runs without user settings\n", link, dest)
		return
	}
	if underSamePathMount(p.Volumes, target) {
		return
	}
	p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", target, target))
}

func (in *Inputs) shadowSiblings(p *Plan, configDir string) error {
	parent := filepath.Dir(configDir)
	claudeJSON := filepath.Join(parent, ".claude.json")
	if fileExists(claudeJSON) {
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", claudeJSON, claudeJSON))
	}
	hostMCP := filepath.Join(parent, ".mcp.json")
	target := filepath.Join(parent, ".mcp.json")
	if raw, err := os.ReadFile(hostMCP); err == nil {
		merged, merr := mergeMCP(raw, assets.MCPServers)
		if merr != nil {
			return fmt.Errorf("merging .mcp.json: %w", merr)
		}
		tmp, terr := in.tempFile(".mcp.json", merged)
		if terr != nil {
			return terr
		}
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", tmp, target))
	} else {
		tmp, terr := in.tempFile(".mcp.json", assets.MCPServers)
		if terr != nil {
			return terr
		}
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", tmp, target))
	}
	return nil
}

func (in *Inputs) shadowGitconfig(p *Plan) error {
	src := filepath.Join(in.Home, ".gitconfig")
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil // no gitconfig: nothing to mount
	}
	// A temp COPY, never the host file: git writes config via lock+rename,
	// which fails (EBUSY) against a live single-file mountpoint. Host edits
	// apply on the next launch.
	tmp, err := in.tempFile("gitconfig", raw)
	if err != nil {
		return err
	}
	p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", tmp, src))
	return nil
}

// awsEnvAllowlist keeps the trust boundary explicit — no arbitrary host env
// leaks into the container.
var awsEnvAllowlist = []string{
	"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
	"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ENDPOINT_URL",
}

var awsPathVars = []string{"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_WEB_IDENTITY_TOKEN_FILE"}

func (in *Inputs) assembleAWS(p *Plan) {
	mounted := map[string]bool{}
	awsDir := filepath.Join(in.Home, ".aws")
	if dirExists(awsDir) {
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", awsDir, awsDir))
		mounted[awsDir] = true
	}
	// CS-LNCH-103: bare "-e NAME" for the whole allowlist, so the key id,
	// secret and session token never reach argv; docker reads each value from
	// the launcher's environment. Every name is inherited unchanged, so the
	// non-secret ones go bare too rather than splitting the list by judgement.
	for _, v := range awsEnvAllowlist {
		if in.getenv(v) != "" {
			p.EnvFlags = append(p.EnvFlags, v)
		}
	}
	// Path-valued vars: bind-mount each file's PARENT DIRECTORY read-only so
	// atomic-rename credential refreshes on the host propagate live
	// (CS-LNCH-019/020).
	for _, v := range awsPathVars {
		path := in.getenv(v)
		if path == "" {
			continue
		}
		if !fileExists(path) {
			fmt.Fprintf(in.Err, "WARNING: %s=%s but file not found on host; skipping mount\n", v, path)
			continue
		}
		dir := filepath.Dir(path)
		parent := filepath.Dir(dir)
		if dir == in.Home || dir == "/" || dir == "/root" || parent == "/home" || parent == "/Users" {
			fmt.Fprintf(in.Err, "WARNING: %s=%s is directly under '%s'; refusing to bind-mount that whole directory. Move it into a dedicated subdir (e.g. ~/.aws/) or mount it explicitly via config.yaml mounts:.\n", v, path, dir)
			continue
		}
		if !mounted[dir] {
			p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s:ro", dir, dir))
			mounted[dir] = true
		}
	}
}

// SandboxHomeRoot is the one tree under $HOME that belongs to the sandbox
// alone. Every fixed host-side directory the launcher mounts derives from it,
// so the roots cannot drift apart. It is hostdirs' cache root (CS-DIR-003).
const SandboxHomeRoot = hostdirs.CacheRootRel

// PackageCacheRoot is the sandbox-only tree under $HOME that the package-cache
// lever mounts (CS-LNCH-037). Fixed on purpose: pointing this at the host's
// own ~/go, ~/.npm or ~/.cache/pip would let a session plant a module the
// host's toolchain then trusts (Go verifies zips on download, not extracted
// dirs). Confined here, the blast radius is other sandbox sessions.
const PackageCacheRoot = SandboxHomeRoot

// packageCaches maps each cache dir name to the env var that redirects its
// toolchain (CS-LNCH-035).
var packageCaches = []struct{ dir, env string }{
	{"go-mod", "GOMODCACHE"},
	{"go-build", "GOCACHE"},
	{"npm", "npm_config_cache"},
	{"pip", "PIP_CACHE_DIR"},
}

// assemblePackageCaches mounts the cache dirs writable at the same path and
// points the toolchains at them. The dirs are created here, before docker
// create, as the invoking user (CS-LNCH-036): docker creates a missing bind
// source as root, and the entrypoint deliberately never chowns a mount point.
func (in *Inputs) assemblePackageCaches(p *Plan) error {
	root := filepath.Join(in.Home, PackageCacheRoot)
	for _, c := range packageCaches {
		dir := filepath.Join(root, c.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("package caches: creating %s: %w", dir, err)
		}
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", dir, dir))
		p.EnvFlags = append(p.EnvFlags, c.env+"="+dir)
	}
	return nil
}

// PreCommitCacheDir is the sandbox-only pre-commit cache under $HOME
// (CS-LNCH-133). The host's own ~/.cache/pre-commit holds hook environments
// built by the host's Python; the image's Python differs, so sharing that
// cache made every host/sandbox alternation rebuild them.
const PreCommitCacheDir = SandboxHomeRoot + "/pre-commit"

// preCommitHomeEnv is the variable pre-commit reads its cache location from.
const preCommitHomeEnv = "PRE_COMMIT_HOME"

// assemblePreCommitCache mounts the sandbox-only pre-commit cache writable at
// the same path and points PRE_COMMIT_HOME at it (CS-LNCH-133). It never
// fails the launch: without it the session shares the host's cache, as it
// did before.
//
// The launcher's own PRE_COMMIT_HOME is deliberately not forwarded
// (CS-LNCH-136): it names the host's cache, i.e. the collision. The explicit
// way to hand it to a session is an env file, and an env file wins
// (CS-LNCH-135).
func (in *Inputs) assemblePreCommitCache(p *Plan) {
	// CS-LNCH-135: docker -e silently beats --env-file.
	if in.envFilesDefine(preCommitHomeEnv) {
		return
	}
	// CS-LNCH-138: a relative (or empty) home would name a directory under
	// the launcher's cwd on the host and a different one in the container.
	if !filepath.IsAbs(in.Home) {
		if testing.Testing() {
			panic(fmt.Sprintf("launch: a test launched with a non-absolute home %q; set HOME (Inputs.Home) to a scratch directory", in.Home))
		}
		fmt.Fprintf(in.Out, "Warning: pre-commit cache not mounted: the home directory %q is not an absolute path. pre-commit in this session uses its default cache.\n", in.Home)
		return
	}
	dir := filepath.Join(in.Home, PreCommitCacheDir)
	// A forgotten fixture (no HOME in its getenv map) resolves the real home;
	// fail loudly rather than create a directory under it (the shadowRoot
	// precedent).
	if testing.Testing() && isRealHome(in.Home) {
		panic(fmt.Sprintf("launch: a test would create the pre-commit cache under the real home %s; set HOME (Inputs.Home) to a scratch directory", in.Home))
	}
	// CS-LNCH-137: nested, the bind source resolves on the host; only the
	// outer sandbox's own mount (which set PRE_COMMIT_HOME to this path) shows
	// that it exists there and is the user's. Cleaned, so a trailing slash or
	// a doubled separator in the inherited value still matches.
	if hostdirs.InSandbox(in.getenv) {
		if v := in.getenv(preCommitHomeEnv); v == "" || filepath.Clean(v) != dir {
			fmt.Fprintf(in.Out, "Note: pre-commit cache not mounted: this launcher runs inside a sandbox that does not mount %s, so docker would create it on the host as root. pre-commit in this session uses its default cache.\n", dir)
			return
		}
	}
	// CS-LNCH-134: created as the invoking user before docker create.
	if err := hostdirs.EnsureOwnedDir(dir, hostdirs.OwnedDirMode, nil); err != nil {
		fmt.Fprintf(in.Out, "Warning: pre-commit cache is not isolated for this session: cannot prepare %s (%v); pre-commit in this session shares the host's cache. Make it a directory you own (chown it, or remove it and relaunch), or set PRE_COMMIT_HOME in a .claude-sandbox/env file.\n", dir, err)
		return
	}
	// CS-LNCH-139: a same-path mount that already covers the directory (a
	// cascade mounts: entry naming it or a parent) is kept, and no second
	// mount point is added; PRE_COMMIT_HOME still names it. Runs after the
	// cascade mounts so it can see them.
	if cover, ok := samePathMountOf(p.Volumes, dir); !ok {
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", dir, dir))
	} else if strings.HasSuffix(cover, ":ro") {
		fmt.Fprintf(in.Err, "WARNING: pre-commit cache %s is under the read-only mount %s; pre-commit cannot install hook environments in this session.\n", dir, cover)
	}
	p.EnvFlags = append(p.EnvFlags, preCommitHomeEnv+"="+dir)
}

// isRealHome reports whether home is the invoking user's actual home
// directory, however it is spelled: $HOME's, or the user database's (HOME
// can be unset, as under "env -i go test", while hostIdentity falls back to
// the user database).
func isRealHome(home string) bool {
	var reals []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		reals = append(reals, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		reals = append(reals, u.HomeDir)
	}
	for _, real := range reals {
		if samePath(home, real) {
			return true
		}
	}
	return false
}

// samePath compares two paths through symlinks when both resolve, else
// lexically.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// PeerRegistryRoot is the sandbox-only tree under $HOME that the shared peer
// registry is built from (CS-LNCH-050/051): its sessions/ is mounted over the
// container's <config dir>/sessions, and the root itself is mounted at the
// same path and named by XDG_RUNTIME_DIR so the message sockets land in its
// cc-socks/. Fixed, like PackageCacheRoot: a free-form path could name one
// tree's own <config dir>/sessions as the shared root, putting other trees'
// sandboxes into a registry the host's own claude also writes. A dedicated
// root cannot.
const PeerRegistryRoot = SandboxHomeRoot + "/peers"

// peerRegistryDirs are the two subdirectories of PeerRegistryRoot: the peer
// registry itself, and the socket directory Claude Code creates beneath
// XDG_RUNTIME_DIR.
const (
	peerSessionsDir = "sessions"
	peerSocketsDir  = "cc-socks"
)

// peerDirMode matches the mode Claude Code itself gives <config dir>/sessions
// and its socket directory. The registry holds one <pid>.json and one
// <pid>.<hash>.key per live session, so on a multi-user host 0755 would let
// any other local user enumerate every sandbox session in the shared root.
// Claude Code also refuses a socket directory whose ancestry is group- or
// world-writable, silently falling back to a container-private /tmp.
const peerDirMode = 0o700

// maxSocketPath is the longest unix socket path Claude Code will bind; past
// it, it silently falls back to /tmp/cc-socks-<uid> inside the container
// (CS-LNCH-055). sun_path is 108 bytes on Linux, 104 on macOS; 103 leaves the
// NUL for the smaller.
const maxSocketPath = 103

// worstSocketSuffix is what Claude Code appends to XDG_RUNTIME_DIR for the
// largest pid a sandbox can get: /cc-socks/<7-digit pid>.sock.
const worstSocketSuffix = "/" + peerSocketsDir + "/1234567.sock"

// assembleSharedPeerRegistry bridges peer discovery and messaging across
// containers whose CLAUDE_CONFIG_DIR differs (CS-LNCH-050).
//
// Discovery: the shared sessions/ is mounted over the container's
// <config dir>/sessions, so every opted-in container reads one registry.
//
// Messaging: each record carries the ABSOLUTE socket path its writer bound,
// and a reader lists a peer only if connect() to that path works from its own
// container. Claude Code binds at XDG_RUNTIME_DIR || CLAUDE_CODE_TMPDIR ||
// os.tmpdir(), then /cc-socks/<pid>.sock. So the peers root is mounted at the
// SAME path in every bridged container and XDG_RUNTIME_DIR points at it: every
// session advertises <home>/.cache/claude-sandbox/peers/cc-socks/<pid>.sock,
// valid everywhere. XDG_RUNTIME_DIR outranks CLAUDE_CODE_TMPDIR for the socket
// path only, so scratchpads stay where CS-LNCH-034 put them. It names the ROOT,
// not cc-socks: Claude Code appends /cc-socks itself, and a bundled
// language-server library drops vscode-ipc-*.sock directly under it.
// XDG_RUNTIME_DIR applies to the WHOLE container, not only to Claude Code:
// anything else that honours it (dbus, gpg, podman, pulse) puts its runtime
// files in this shared, host-persistent folder too.
//
// Both mounts are READ-WRITE on purpose: every session writes its own
// <pid>.json into the registry and bind()s its own <pid>.sock under the socket
// root, and bind() under a read-only bind mount fails with EROFS. (connect()
// through a :ro bind succeeds; it is the bind that needs the write.)
//
// The bridge REPLACES the container's own registry rather than unioning it: a
// bind mount hides whatever the destination held. Every session that is to be
// visible must therefore be opted in and (re)launched — sessions still running
// against the real <config dir>/sessions, the host's own claude included,
// neither see the bridged ones nor are seen by them.
//
// The host directories are created here, before docker create, as the invoking
// user (CS-LNCH-051): docker creates a missing bind source as root and the
// entrypoint never chowns a mount point, and Claude Code refuses a socket
// directory not owned by the user (or root). The registry DESTINATION is
// created too, for the mirror image of that: its parent is the read-write
// same-path bind of the config dir, so a mountpoint docker creates as root
// materialises on the HOST and outlives the container, leaving a root-owned
// <config dir>/sessions that every later un-bridged sandbox — and the host's
// own claude — could not write. That holds only for a destination behind a
// same-path bind, hence mkPeerDest's guard.
//
// It reports whether the bridge was applied; false means it stood down for
// this session (CS-LNCH-054/055/107) and the plan is exactly the key-off plan.
// It never fails the launch: every obstacle is a stand-down with one warning.
func (in *Inputs) assembleSharedPeerRegistry(p *Plan, configDir string) bool {
	root := filepath.Join(in.Home, PeerRegistryRoot)

	// CS-LNCH-054/055: when the socket cannot be bridged, the WHOLE bridge
	// stands down for this session. A registry-only bridge is strictly worse
	// than the key off: Claude Code drops every peer whose advertised socket
	// it cannot connect() to, so the session would list nobody and nobody
	// would list it — while the overmount hid its own tree's real registry
	// and with it the same-tree peers it can reach today. Checked before
	// anything is created or mounted, so a stood-down launch touches nothing.
	//
	// 054: docker -e silently beats --env-file, so an env file that sets
	// XDG_RUNTIME_DIR keeps it — the CLAUDE_CODE_TMPDIR precedent. The
	// launcher never forwards the host's own XDG_RUNTIME_DIR, so an env file
	// is the only source that can collide.
	if in.envFilesDefine("XDG_RUNTIME_DIR") {
		fmt.Fprintln(in.Out, "Warning: sharedPeerRegistry is off for this session: an env file sets XDG_RUNTIME_DIR, and the bridge needs to set it to share message sockets. Without that, bridged peers could not reach this session nor it them.")
		return false
	}
	// 055: past maxSocketPath Claude Code would silently bind in a
	// container-private /tmp instead, advertising an address no other
	// container can reach.
	if n := len(root) + len(worstSocketSuffix); n > maxSocketPath {
		fmt.Fprintf(in.Out, "Warning: sharedPeerRegistry is off for this session: the message socket path %s would be %d bytes, over Claude Code's %d-byte limit. Without a shared socket, bridged peers could not reach this session nor it them.\n", root+worstSocketSuffix, n, maxSocketPath)
		return false
	}

	// Every launcher-owned directory is created, and TIGHTENED, before any
	// mount is assembled: MkdirAll leaves an existing wider mode alone, and
	// Claude Code silently falls back to a container-private /tmp when the
	// socket directory's ancestry is group- or world-writable.
	//
	// CS-LNCH-107: a directory that cannot be made ours (root-owned, read-only,
	// a file or a symlink in the way) stands the bridge down rather than
	// failing the launch — an error here would fail every launch in every tree
	// inheriting the key. Nothing has been mounted yet, so the plan is still
	// the key-off plan. The warning names the fix, since it will recur.
	sessions := filepath.Join(root, peerSessionsDir)
	for _, d := range []string{root, sessions, filepath.Join(root, peerSocketsDir)} {
		if err := in.mkOwnedPeerDir(d); err != nil {
			in.peerDirStandDown(d, err)
			return false
		}
	}
	// Guarded BEFORE the mount is appended: afterwards the destination would
	// be under a mount by definition — its own. Not chmod'ed: it lives under
	// the user's config dir and is not the launcher's to tighten.
	dstSessions := filepath.Join(configDir, peerSessionsDir)
	if err := in.mkPeerDest(p, dstSessions); err != nil {
		in.peerDirStandDown(dstSessions, err)
		return false
	}
	p.Volumes = append(p.Volumes,
		fmt.Sprintf("%s:%s", sessions, dstSessions),
		fmt.Sprintf("%s:%s", root, root),
	)
	p.EnvFlags = append(p.EnvFlags, "XDG_RUNTIME_DIR="+root)

	// CS-LNCH-053: the bridge crosses a boundary the operator drew on purpose,
	// and a workspace-level config key can switch it on for a session that
	// never asked. Like the Worktree banner, it prints only when the mode is
	// actually in use.
	fmt.Fprintf(in.Out, "Peer registry: shared (%s) - /peers and SendMessage reach every other opted-in sandbox on this host, and only those.\n", root)
	return true
}

// peerDirStandDown prints the one CS-LNCH-107 warning: the directory, the
// error, and both remedies.
func (in *Inputs) peerDirStandDown(dir string, err error) {
	fmt.Fprintf(in.Out, "Warning: sharedPeerRegistry is off for this session: cannot prepare %s (%v). Make it a directory you own (chown it, or remove it and relaunch), or set CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0 to keep this tree off the bridge.\n", dir, err)
}

// mkPeerDest creates a mount DESTINATION, but only when it is a host path
// behind an existing bind — the one case where docker would otherwise create
// it as root on the host. Anything else is left to docker: creating it here
// would either fail the launch or plant a directory tree on the host that the
// host was never meant to own.
//
// The predicate is deliberately underSamePathMount, not underAnyMount. dst is
// a CONTAINER path, and mkPeerDir creates it on the HOST; the two are the same
// path only under a same-path mount. A cascade `mounts:` entry may set host !=
// container (CS-LNCH-021), and then a container path under it would be created
// under a host path that means nothing here. The registry destination sits
// under the config dir's own <cfg>:<cfg> mount, so the normal path is
// unaffected.
func (in *Inputs) mkPeerDest(p *Plan, dst string) error {
	if !underSamePathMount(p.Volumes, dst) {
		return nil
	}
	return in.mkPeerDir(dst)
}

// mkPeerDir creates one side of a peer-registry mount as the invoking user.
func (in *Inputs) mkPeerDir(dir string) error {
	if err := os.MkdirAll(dir, peerDirMode); err != nil {
		return fmt.Errorf("creating it: %w", err)
	}
	return nil
}

// mkOwnedPeerDir creates a directory under PeerRegistryRoot and enforces
// peerDirMode even when it already existed wider (CS-LNCH-051). These are
// sandbox-only directories the launcher owns, so tightening them is safe.
// The rule is hostdirs.EnsureOwnedDir's (CS-DIR-004/005): never through a
// symlink, never a non-directory or another uid's directory (CS-LNCH-107).
func (in *Inputs) mkOwnedPeerDir(dir string) error {
	ops := &hostdirs.Ops{}
	if in.Chmod != nil {
		ops.Fchmod = func(f *os.File, m os.FileMode) error { return in.Chmod(f.Name(), m) }
	}
	return hostdirs.EnsureOwnedDir(dir, peerDirMode, ops)
}

// ResolveTristate implements CLI > env var > YAML > default for a setting
// whose default may be TRUE (CS-LNCH-042). Unlike resolveFlag, a falsy env
// value ("0", "false", "no") is an explicit off rather than a fall-through:
// with a true default, fall-through would leave the env var unable to disable
// a config "true". Any other env value is treated as unset.
func ResolveTristate(cli *bool, envVal string, yaml *bool, def bool) bool {
	if cli != nil {
		return *cli
	}
	switch strings.ToLower(strings.TrimSpace(envVal)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	if yaml != nil {
		return *yaml
	}
	return def
}

// resolveFlag implements the CLI > env var > YAML precedence.
func resolveFlag(cli *bool, envVal string, yaml *bool) bool {
	if cli != nil {
		return *cli
	}
	switch envVal {
	case "1", "true", "yes":
		return true
	}
	return yaml != nil && *yaml
}

// mergeMCP merges mcpServers key-by-key, fragment servers winning.
func mergeMCP(host, fragment []byte) ([]byte, error) {
	var hm, fm map[string]json.RawMessage
	if err := json.Unmarshal(host, &hm); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fragment, &fm); err != nil {
		return nil, err
	}
	if hm == nil {
		hm = map[string]json.RawMessage{}
	}
	var hServers, fServers map[string]json.RawMessage
	if raw, ok := hm["mcpServers"]; ok {
		json.Unmarshal(raw, &hServers)
	}
	if hServers == nil {
		hServers = map[string]json.RawMessage{}
	}
	if raw, ok := fm["mcpServers"]; ok {
		json.Unmarshal(raw, &fServers)
	}
	for k, v := range fServers {
		hServers[k] = v
	}
	mergedServers, err := json.Marshal(hServers)
	if err != nil {
		return nil, err
	}
	hm["mcpServers"] = mergedServers
	return json.MarshalIndent(hm, "", "  ")
}

func socketGID(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d", st.Gid)
	}
	return ""
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// underAnyMount reports whether path lies inside (or at) the container side of
// any -v spec. Used to warn when a user-supplied CLAUDE_CODE_TMPDIR points at
// the container's writable layer, where files silently die with the container.
func underAnyMount(volumes []string, path string) bool {
	path = filepath.Clean(path)
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			continue
		}
		dst := filepath.Clean(parts[1])
		if path == dst || strings.HasPrefix(path, dst+"/") {
			return true
		}
	}
	return false
}

// underSamePathMount reports whether path lies inside (or at) a mount whose
// host and container sides are the SAME path — the only case in which a
// container path is also a meaningful host path. underAnyMount answers a
// different question (is this visible in the container at all) and must not be
// substituted for it when the answer is used to create something on the host.
func underSamePathMount(volumes []string, path string) bool {
	_, ok := samePathMountOf(volumes, path)
	return ok
}

// samePathMountOf returns the first same-path -v spec that covers path, as
// underSamePathMount decides it.
func samePathMountOf(volumes []string, path string) (string, bool) {
	path = filepath.Clean(path)
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		if len(parts) < 2 || filepath.Clean(parts[0]) != filepath.Clean(parts[1]) {
			continue
		}
		dst := filepath.Clean(parts[1])
		if path == dst || strings.HasPrefix(path, dst+"/") {
			return v, true
		}
	}
	return "", false
}

// envFilesDefine reports whether docker would set key from the env files,
// parsed by the cascade's shared docker-faithful reader (CS-LNCH-108): a BOM,
// indentation and a CRLF ending do not hide a key, and a bare KEY line counts
// when the launcher's own environment sets KEY, since docker passes it through.
// It reads the snapshot docker will get, never the path (CS-LNCH-132).
func (in *Inputs) envFilesDefine(key string) bool {
	return cascade.EnvFilesDefine(in.Env, key, in.lookupEnvValue)
}

// lookupEnvValue is the launcher's environment as docker resolves a bare env
// file key against it: LookupEnv when injected, else Getenv with "" as unset.
func (in *Inputs) lookupEnvValue(k string) (string, bool) {
	if in.LookupEnv != nil {
		return in.LookupEnv(k)
	}
	v := in.getenv(k)
	return v, v != ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
