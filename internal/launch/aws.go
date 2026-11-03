package launch

// Where AWS sign-ins live (CS-LNCH-229..240). With aws access on, the host's
// ~/.aws is mounted read-only (CS-LNCH-018), but the AWS CLI and SDKs must
// WRITE two folders under it — ~/.aws/sso/cache (the sign-in tokens and their
// refresh) and ~/.aws/cli/cache (the CLI's role credentials) — and no CLI or
// SDK takes another location for them. So a writable directory is bound AT
// each path, over the read-only ~/.aws:
//
//   - sandbox (default): the launcher's own ~/.cache/claude-sandbox/aws/<key>/
//     {sso,cli}, one per Claude config folder; every sandbox on that config
//     folder shares one sign-in made inside a sandbox, and a host sign-in
//     does not reach it.
//   - host: the host's own two folders, read-write: a host sign-in reaches
//     every sandbox, and a sandbox sign-out signs the host out.
//   - readonly: the read-only mount alone, as before.
//
// Nested launches create nothing and act only on the evidence the outer
// launch exports (CS-LNCH-236).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// AWSTokenCacheEnv overrides hostAccess.aws.tokenCache for one launch.
const AWSTokenCacheEnv = "CLAUDE_SANDBOX_AWS_TOKEN_CACHE"

// The tokenCache values a user chooses (CS-LNCH-231), and the two applied
// values only a nested launch reaches (CS-LNCH-236).
const (
	AWSCacheSandbox   = "sandbox"
	AWSCacheHost      = "host"
	AWSCacheReadonly  = "readonly"
	AWSCacheContainer = "container"
	AWSCacheNone      = "none"
)

// The evidence a launch exports for a launcher run inside its container
// (CS-LNCH-236): every launch, aws on or off, passes all three as
// "-e NAME=<value or empty>", so an env file can never set them.
const (
	// AWSHomeEnv is the host ~/.aws this launch mounted, "" when none.
	AWSHomeEnv = "CLAUDE_SANDBOX_AWS_HOME"
	// AWSCacheEnv is the applied cache mode when aws is on, else "".
	AWSCacheEnv = "CLAUDE_SANDBOX_AWS_CACHE"
	// AWSCacheDirEnv is the scope directory for an applied "sandbox", else "".
	AWSCacheDirEnv = "CLAUDE_SANDBOX_AWS_CACHE_DIR"
)

// AWSEvidenceEnv lists the three, in -e order.
var AWSEvidenceEnv = []string{AWSHomeEnv, AWSCacheEnv, AWSCacheDirEnv}

// AWSLoginCacheVar is where "aws login" keeps its sign-in (CS-LNCH-239).
const AWSLoginCacheVar = "AWS_LOGIN_CACHE_DIRECTORY"

// AWSCacheRoot is the launcher-owned tree of AWS sign-in caches, relative to
// $HOME: one <key> directory per Claude config folder.
const AWSCacheRoot = SandboxHomeRoot + "/aws"

// awsCacheDirs are the two folders bound over the read-only ~/.aws: the scope
// directory's subdirectory name, and the path under ~/.aws it is bound at.
var awsCacheDirs = []struct{ sub, under string }{
	{"sso", "sso/cache"},
	{"cli", "cli/cache"},
}

// awsMountPointDirs are the levels under ~/.aws that must exist on the host
// before a bind at sso/cache and cli/cache: runc would otherwise mkdir them
// through the read-only ~/.aws bind.
var awsMountPointDirs = []string{"sso", "sso/cache", "cli", "cli/cache"}

// ResolveAWSTokenCache applies CLAUDE_SANDBOX_AWS_TOKEN_CACHE >
// hostAccess.aws.tokenCache > "sandbox" (CS-LNCH-231). An empty value falls
// through, as unset. source is the config file that set the key
// (cascade.KeyPathSourceOf), named in the error. The launcher calls it before
// any image work; Build calls it again when aws is on.
func ResolveAWSTokenCache(getenv func(string) string, cfg *cascade.Config, source string) (string, error) {
	if raw := strings.TrimSpace(getenv(AWSTokenCacheEnv)); raw != "" {
		if v, ok := awsTokenCacheValue(raw); ok {
			return v, nil
		}
		return "", fmt.Errorf("%s=%q: must be sandbox, host or readonly", AWSTokenCacheEnv, raw)
	}
	if cfg != nil && cfg.HostAccess.AWS.TokenCache != nil {
		if raw := strings.TrimSpace(*cfg.HostAccess.AWS.TokenCache); raw != "" {
			if v, ok := awsTokenCacheValue(raw); ok {
				return v, nil
			}
			if source == "" {
				source = "the config cascade"
			}
			return "", fmt.Errorf("hostAccess.aws.tokenCache: %q in %s: must be sandbox, host or readonly", raw, source)
		}
	}
	return AWSCacheSandbox, nil
}

func awsTokenCacheValue(raw string) (string, bool) {
	switch v := strings.ToLower(raw); v {
	case AWSCacheSandbox, AWSCacheHost, AWSCacheReadonly:
		return v, true
	}
	return "", false
}

// awsState carries what the aws assembly applied, from the ~/.aws mount
// (before the cascade mounts) to the cache step (after them) and the export.
type awsState struct {
	on  bool
	dir string // <home>/.aws
	// homeMounted: the host ~/.aws was bound read-only (CS-LNCH-018).
	homeMounted bool
	// covered: a cascade mount covers a cache dir (CS-LNCH-235).
	covered bool
	mode    string // the applied mode, hashed (CS-LNCH-237)
	scope   string // the scope directory of an applied "sandbox"
}

// AWSScopeDir is the scope directory of a launch on the given Claude config
// folder (CS-LNCH-229): <home>/.cache/claude-sandbox/aws/<sha256[:12] of the
// normalised config folder>. ok is false for a relative or "~" CLAUDE_CONFIG_DIR.
// configDir is the raw CLAUDE_CONFIG_DIR, "" when unset.
func AWSScopeDir(home, configDir string) (scope string, ok bool) {
	var norm string
	switch {
	case configDir == "":
		norm = filepath.Join(home, ".claude")
	case !filepath.IsAbs(configDir):
		return "", false
	default:
		norm = filepath.Clean(configDir)
		if r, err := filepath.EvalSymlinks(norm); err == nil {
			norm = r
		}
	}
	sum := sha256.Sum256([]byte(norm))
	return filepath.Join(home, AWSCacheRoot, hex.EncodeToString(sum[:])[:12]), true
}

// awsScope is AWSScopeDir for this launch, with the CS-LNCH-229 warning.
func (in *Inputs) awsScope() (string, bool) {
	raw := in.getenv("CLAUDE_CONFIG_DIR")
	scope, ok := AWSScopeDir(in.Home, raw)
	if !ok {
		fmt.Fprintf(in.Err, "WARNING: AWS sign-ins in this session use the host's read-only cache: CLAUDE_CONFIG_DIR=%q is not an absolute path, and the sandbox's own sign-in folder is chosen by it. Set CLAUDE_CONFIG_DIR to an absolute path.\n", raw)
	}
	return scope, ok
}

// assembleAWSCache binds the writable sign-in folders (CS-LNCH-229..240).
// After the cascade mounts, which it checks for a cover. Never fails the
// launch but for an invalid tokenCache: every other problem stands the
// caches down to "readonly" (the mount CS-LNCH-018 already added) with one
// warning.
func (in *Inputs) assembleAWSCache(p *Plan, st *awsState) error {
	want, err := ResolveAWSTokenCache(in.getenv, in.Cfg, in.AWSTokenCacheSource)
	if err != nil {
		return err
	}
	st.mode = AWSCacheReadonly
	if !st.homeMounted && hostdirs.InSandbox(in.getenv) {
		st.mode = AWSCacheNone
	}
	if want == AWSCacheReadonly {
		return nil // CS-LNCH-233
	}
	// CS-LNCH-235: a cascade mount covering a cache dir is kept; nothing is
	// added, and the evidence says readonly with no ~/.aws.
	if cover, ok := in.awsCascadeCover(st.dir); ok {
		st.covered = true
		st.mode = AWSCacheReadonly
		if strings.HasSuffix(cover, ":ro") {
			fmt.Fprintf(in.Err, "WARNING: AWS sign-ins cannot be saved in this session: %s is under the read-only mount %s from the config cascade, so \"aws sso login\" and token refreshes fail. Make that mount writable, or drop it.\n", filepath.Join(st.dir, "sso/cache"), cover)
		}
		return nil
	}
	if !in.cacheHomeUsable("AWS sign-in cache", "AWS in this session uses the host's read-only ~/.aws") {
		return nil
	}
	if hostdirs.InSandbox(in.getenv) {
		in.awsCacheNested(p, st, want)
		return nil
	}
	switch want {
	case AWSCacheHost:
		in.awsHostMode(p, st)
	case AWSCacheSandbox:
		in.awsSandboxMode(p, st)
	}
	return nil
}

// awsCascadeCover returns the cascade mount whose container side covers
// either cache dir (CS-LNCH-235).
func (in *Inputs) awsCascadeCover(awsDir string) (string, bool) {
	for _, m := range in.Cfg.Mounts {
		if m.Container == in.ProjectDir {
			continue // skipped by the mount loop
		}
		dst := filepath.Clean(m.Container)
		for _, c := range awsCacheDirs {
			path := filepath.Join(awsDir, c.under)
			if path == dst || strings.HasPrefix(path, dst+"/") {
				spec := m.Host + ":" + m.Container
				if !m.Writable {
					spec += ":ro"
				}
				return spec, true
			}
		}
	}
	return "", false
}

// awsStandDown is the one warning of a refused directory.
func (in *Inputs) awsStandDown(st *awsState, what string, err error) {
	st.mode = AWSCacheReadonly
	fmt.Fprintf(in.Err, "WARNING: AWS sign-ins in this session use the host's read-only cache: cannot prepare %s (%v). Make it a directory you own (chown it, or remove it and relaunch), or set hostAccess.aws.tokenCache: readonly.\n", what, err)
}

// awsMountPoints makes sure the four levels under the host ~/.aws exist as
// the user's own directories, create-only (CS-LNCH-230): an existing one is
// never re-moded; a symlink, a non-directory or another uid's directory is
// refused. ~/.aws itself may be a symlink (followed). The drift build
// creates nothing; a missing level is not a refusal there.
func (in *Inputs) awsMountPoints(awsDir string) (string, error) {
	for _, rel := range awsMountPointDirs {
		d := filepath.Join(awsDir, rel)
		if err := hostdirs.CheckOwnedDir(d, in.Getuid); err != nil {
			return d, err
		}
		if in.NoCreatePeerDirs {
			continue
		}
		if _, err := os.Lstat(d); errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(d, hostdirs.OwnedDirMode); err != nil && !errors.Is(err, os.ErrExist) {
				return d, fmt.Errorf("creating it: %w", err)
			}
			if err := hostdirs.CheckOwnedDir(d, in.Getuid); err != nil {
				return d, err
			}
		}
	}
	return "", nil
}

// awsOwnedDir is EnsureOwnedDir for a launch, CheckOwnedDir for the drift
// build (CS-LNCH-237: it creates and re-modes nothing; a missing directory is
// no refusal, since the launch would create it).
func (in *Inputs) awsOwnedDir(d string) error {
	if in.NoCreatePeerDirs {
		return hostdirs.CheckOwnedDir(d, in.Getuid)
	}
	return hostdirs.EnsureOwnedDir(d, hostdirs.OwnedDirMode, &hostdirs.Ops{Getuid: in.Getuid})
}

// awsSandboxMode is CS-LNCH-229/230/234/239/240 on the host.
func (in *Inputs) awsSandboxMode(p *Plan, st *awsState) {
	scope, ok := in.awsScope()
	if !ok {
		return
	}
	root := filepath.Join(in.Home, AWSCacheRoot)
	// CS-LNCH-240: every level re-checked at every launch, 0700.
	for _, d := range []string{root, scope, filepath.Join(scope, "sso"), filepath.Join(scope, "cli")} {
		if err := in.awsOwnedDir(d); err != nil {
			in.awsStandDown(st, d, err)
			return
		}
	}
	// CS-LNCH-230: the mount points under the host ~/.aws; CS-LNCH-234: with
	// no host ~/.aws nothing is created there, and runc makes the mount
	// points in the container's own home.
	if st.homeMounted {
		if d, err := in.awsMountPoints(st.dir); err != nil {
			in.awsStandDown(st, d, err)
			return
		}
	}
	if !in.NoCreatePeerDirs {
		for _, c := range awsCacheDirs {
			in.sweepNonRegular(filepath.Join(scope, c.sub))
		}
	}
	in.bindAWSScope(p, st, scope)
	// CS-LNCH-239: aws login follows, unless an env file sets its folder
	// (docker -e would beat it, the CS-LNCH-135 rule).
	if !in.envFilesDefine(AWSLoginCacheVar) {
		login := filepath.Join(scope, "login")
		if err := in.awsOwnedDir(login); err != nil {
			fmt.Fprintf(in.Err, "WARNING: \"aws login\" sign-ins are not kept in this session: cannot prepare %s (%v). Make it a directory you own (chown it, or remove it and relaunch).\n", login, err)
		} else {
			in.bindAWSLogin(p, login)
		}
	}
	in.awsBanner(st)
}

// bindAWSScope binds the scope's two folders over the read-only ~/.aws.
func (in *Inputs) bindAWSScope(p *Plan, st *awsState, scope string) {
	for _, c := range awsCacheDirs {
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", filepath.Join(scope, c.sub), filepath.Join(st.dir, c.under)))
	}
	st.mode, st.scope = AWSCacheSandbox, scope
}

// bindAWSLogin mounts the aws login folder at its own path and names it.
func (in *Inputs) bindAWSLogin(p *Plan, login string) {
	p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", login, login))
	p.EnvFlags = append(p.EnvFlags, AWSLoginCacheVar+"="+login)
}

// awsHostMode is CS-LNCH-232: the host's own two folders, read-write.
func (in *Inputs) awsHostMode(p *Plan, st *awsState) {
	if !st.homeMounted {
		fmt.Fprintf(in.Err, "WARNING: hostAccess.aws.tokenCache: host has no host folders to share: %s does not exist. Sign in on the host once (it creates them), or use tokenCache: sandbox.\n", st.dir)
		return
	}
	if d, err := in.awsMountPoints(st.dir); err != nil {
		in.awsStandDown(st, d, err)
		return
	}
	in.bindAWSHost(p, st)
	in.awsBanner(st)
}

func (in *Inputs) bindAWSHost(p *Plan, st *awsState) {
	for _, c := range awsCacheDirs {
		d := filepath.Join(st.dir, c.under)
		p.Volumes = append(p.Volumes, fmt.Sprintf("%s:%s", d, d))
	}
	st.mode = AWSCacheHost
}

// awsCacheNested is CS-LNCH-236's inner rules: nothing is created, and a bind
// source is used only when the outer launch's evidence shows it exists on
// the host.
func (in *Inputs) awsCacheNested(p *Plan, st *awsState, want string) {
	ev := in.getenv(AWSCacheEnv)
	switch {
	case want == AWSCacheSandbox && ev == AWSCacheSandbox:
		if scope, ok := in.awsScope(); ok {
			if d := in.getenv(AWSCacheDirEnv); d != "" && filepath.Clean(d) == scope {
				in.bindAWSScope(p, st, scope)
				login := filepath.Join(scope, "login")
				if v := in.getenv(AWSLoginCacheVar); !in.envFilesDefine(AWSLoginCacheVar) && v != "" && filepath.Clean(v) == login {
					in.bindAWSLogin(p, login)
				}
				in.awsBanner(st)
				return
			}
		}
	case want == AWSCacheHost && ev == AWSCacheHost && st.homeMounted:
		in.bindAWSHost(p, st)
		in.awsBanner(st)
		return
	}
	switch {
	case st.homeMounted && (ev == AWSCacheSandbox || ev == AWSCacheHost || ev == AWSCacheContainer):
		// The mount points exist on the host (the outer's binds needed them):
		// a container-private cache, nothing resolved on the host.
		for _, c := range awsCacheDirs {
			p.Tmpfs = append(p.Tmpfs, fmt.Sprintf("%s:rw,nosuid,nodev,noexec,mode=0700,uid=%d,gid=%d", filepath.Join(st.dir, c.under), in.HostUID, in.HostGID))
		}
		st.mode = AWSCacheContainer
		fmt.Fprintln(in.Out, "Note: AWS sign-ins in this session are its own and end with it: this launcher runs inside a sandbox whose AWS sign-in folder it cannot share.")
	case st.homeMounted:
		st.mode = AWSCacheReadonly
	default:
		st.mode = AWSCacheNone
	}
}

// awsBanner is CS-LNCH-238 (sandbox) and CS-LNCH-232 (host): one line.
func (in *Inputs) awsBanner(st *awsState) {
	if in.Out == nil {
		return
	}
	switch st.mode {
	case AWSCacheSandbox:
		fmt.Fprintf(in.Out, "AWS: sign-ins here are shared by every sandbox on the Claude config folder %s (kept in %s); sign in with \"aws sso login --use-device-code\". A host sign-in does not reach here: hostAccess.aws.tokenCache: host shares the host's, readonly mounts it read-only.\n", in.ConfigDir(), st.scope)
	case AWSCacheHost:
		fmt.Fprintf(in.Out, "AWS: sign-ins here are the host's own (%s/sso/cache and cli/cache, read-write): a sign-out here signs out every session on the host. hostAccess.aws.tokenCache: sandbox keeps sandboxes' sign-ins apart.\n", st.dir)
	}
}

// sweepNonRegular removes every entry of dir that Lstat shows is not a
// regular file (CS-LNCH-240): a symlink, FIFO, socket or device is unlinked,
// an empty directory removed, a non-empty one left with one warning. Nothing
// is followed, opened or read. Partial by nature: it catches only what was
// planted before this launch.
func (in *Inputs) sweepNonRegular(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode().IsRegular() {
			continue
		}
		if fi.IsDir() {
			if err := syscall.Rmdir(path); err != nil {
				fmt.Fprintf(in.Err, "WARNING: %s is a directory in the AWS sign-in folder and could not be removed (%v); the AWS tools never make one there. Remove it.\n", path, err)
			}
			continue
		}
		if err := syscall.Unlink(path); err != nil {
			fmt.Fprintf(in.Err, "WARNING: %s is not a regular file and could not be removed from the AWS sign-in folder (%v). Remove it.\n", path, err)
		}
	}
}

// exportAWSEvidence passes CS-LNCH-236's three variables on EVERY launch, aws
// on or off, each with its value or empty: -e beats --env-file, so a value a
// session wrote into an env file never reaches the container. An env file
// defining one gets one warning naming it.
func (in *Inputs) exportAWSEvidence(p *Plan, st awsState) {
	home, cache, dir := "", "", ""
	if st.on {
		cache = st.mode
		if st.homeMounted && !st.covered {
			home = st.dir
		}
		if st.mode == AWSCacheSandbox {
			dir = st.scope
		}
	}
	p.EnvFlags = append(p.EnvFlags, AWSHomeEnv+"="+home, AWSCacheEnv+"="+cache, AWSCacheDirEnv+"="+dir)
	for _, k := range AWSEvidenceEnv {
		if in.envFilesDefine(k) {
			fmt.Fprintf(in.Err, "WARNING: %s in an env file is ignored: the launcher sets it on every launch, for launchers run inside this sandbox. Remove it from the env file.\n", k)
		}
	}
}
