// Package launch assembles the docker run invocation: mounts, shadow-file
// injections, host-access resolution, and the container command.
// Spec: spec/launch.feature (CS-LNCH).
package launch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// Inputs collects everything needed to assemble the docker run argv.
type Inputs struct {
	ProjectDir string
	Home       string
	HostUID    int
	HostGID    int
	HostUser   string

	// Getenv is the environment seam (defaults to os.Getenv).
	Getenv func(string) string

	// TempDir hosts the shadow files (defaults to os.MkdirTemp base).
	TempDir string

	RalphMode       bool
	Limit           string
	SkipPermissions bool
	CLIModel        string
	Passthrough     []string

	// CLI host-access overrides (nil = not passed).
	CLISSH, CLIGit, CLIDockerSocket, CLIAWS *bool

	Cfg      *cascade.Config
	EnvFiles []string

	ImageName string
	Out       io.Writer
	Err       io.Writer
}

// Plan is the assembled invocation.
type Plan struct {
	Image         string
	ContainerName string
	Volumes       []string // -v specs
	EnvFlags      []string // -e KEY=VAL specs
	EnvFiles      []string // --env-file paths
	MemoryLimit   string
	Command       []string // command + args inside the container
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
	p := &Plan{Image: in.ImageName}

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
	// CS-LNCH-011: settings.json shadow (notification hooks merged).
	if err := in.shadowSettings(p, configDir); err != nil {
		return nil, err
	}
	// CS-LNCH-012/013: siblings of the config dir.
	if err := in.shadowSiblings(p, configDir); err != nil {
		return nil, err
	}

	// CS-LNCH-014: host access precedence CLI > env var > YAML.
	ssh := resolveFlag(in.CLISSH, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED"), in.Cfg.HostAccess.SSH.Enabled)
	git := resolveFlag(in.CLIGit, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED"), in.Cfg.HostAccess.Git.Enabled)
	dockerSocket := resolveFlag(in.CLIDockerSocket, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED"), in.Cfg.HostAccess.DockerSocket.Enabled)
	aws := resolveFlag(in.CLIAWS, in.getenv("CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED"), in.Cfg.HostAccess.AWS.Enabled)

	dockerGID := ""
	if dockerSocket {
		p.Volumes = append(p.Volumes, "/var/run/docker.sock:/var/run/docker.sock")
		dockerGID = socketGID("/var/run/docker.sock")
	}
	if aws {
		in.assembleAWS(p)
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

	// CS-LNCH-022: memory limit.
	p.MemoryLimit = in.Cfg.MemoryLimit
	if p.MemoryLimit == "" {
		p.MemoryLimit = "8g"
	}

	// Env files (root-first; later wins).
	p.EnvFiles = in.EnvFiles

	// CS-LNCH-023: model precedence CLI > YAML.
	model := in.CLIModel
	if model == "" {
		model = in.Cfg.Model
	}

	// CS-LNCH-026..028: container command and name.
	slug := imagebuild.Slug(in.ProjectDir)
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
		p.ContainerName = "claude-sandbox-" + slug
		p.Command = []string{"claude"}
		if in.SkipPermissions {
			p.Command = append(p.Command, "--dangerously-skip-permissions")
		}
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
		fmt.Sprintf("ANTHROPIC_API_KEY=%s", in.getenv("ANTHROPIC_API_KEY")),
	)
	if d := in.getenv("CLAUDE_CONFIG_DIR"); d != "" {
		p.EnvFlags = append(p.EnvFlags, "CLAUDE_CONFIG_DIR="+d)
	}

	return p, nil
}

// DockerArgs renders the plan as the docker run argv.
func (p *Plan) DockerArgs(workdir string) []string {
	args := []string{"run", "-it", "--rm", "--init"}
	for _, v := range p.Volumes {
		args = append(args, "-v", v)
	}
	args = append(args, "-w", workdir)
	for _, ef := range p.EnvFiles {
		args = append(args, "--env-file", ef)
	}
	args = append(args, "--memory", p.MemoryLimit, "--memory-swap", p.MemoryLimit)
	for _, e := range p.EnvFlags {
		args = append(args, "-e", e)
	}
	args = append(args, "--name", p.ContainerName, p.Image)
	args = append(args, p.Command...)
	return args
}

// Exec hands the process over to docker run.
func (p *Plan) Exec(r execx.Runner, workdir string) error {
	return r.Exec(execx.Cmd{Name: "docker", Args: p.DockerArgs(workdir)})
}

func (in *Inputs) tempFile(name string, content []byte) (string, error) {
	dir := in.TempDir
	if dir == "" {
		d, err := os.MkdirTemp("", "claude-sandbox")
		if err != nil {
			return "", err
		}
		in.TempDir = d
		dir = d
	}
	p := filepath.Join(dir, name)
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

func (in *Inputs) shadowSettings(p *Plan, configDir string) error {
	hostSettings := filepath.Join(configDir, "settings.json")
	var merged []byte
	if raw, err := os.ReadFile(hostSettings); err == nil {
		m, merr := mergeTopLevel(raw, assets.NotificationHooks)
		if merr != nil {
			return fmt.Errorf("merging settings.json: %w", merr)
		}
		merged = m
	} else {
		merged = assets.NotificationHooks
	}
	tmp, err := in.tempFile("settings.json", merged)
	if err != nil {
		return err
	}
	// Read-write shadow (sessions may write); the host file is never touched.
	p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", tmp, hostSettings))
	return nil
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
	for _, v := range awsEnvAllowlist {
		if val := in.getenv(v); val != "" {
			p.EnvFlags = append(p.EnvFlags, v+"="+val)
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

// mergeTopLevel merges b's top-level keys over a's ({...a, ...b}).
func mergeTopLevel(a, b []byte) ([]byte, error) {
	var am, bm map[string]json.RawMessage
	if err := json.Unmarshal(a, &am); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &bm); err != nil {
		return nil, err
	}
	if am == nil {
		am = map[string]json.RawMessage{}
	}
	for k, v := range bm {
		am[k] = v
	}
	return json.MarshalIndent(am, "", "  ")
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

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
