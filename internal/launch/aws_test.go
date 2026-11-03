package launch_test

// Spec: spec/launch.feature CS-LNCH-229..240 — where AWS sign-ins live
// (hostAccess.aws.tokenCache). Every home is a scratch directory; nothing
// here reads or writes the real ~/.aws or ~/.cache.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

func modeOf(p string) os.FileMode {
	fi, err := os.Stat(p)
	Expect(err).NotTo(HaveOccurred())
	return fi.Mode().Perm()
}

// envValue is the value of the plan's "-e NAME=value" for name, and whether
// one is present.
func envValue(p *launch.Plan, name string) (string, bool) {
	for _, e := range p.EnvFlags {
		if k, v, ok := strings.Cut(e, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

var _ = Describe("AWS sign-in folders (CS-LNCH-229..240)", func() {
	var (
		home, proj, awsDir string
		env                map[string]string
		in                 launch.Inputs
		out, errw          *bytes.Buffer
		hashText           string
	)

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(base, "home")
		proj = filepath.Join(base, "proj")
		awsDir = filepath.Join(home, ".aws")
		mkdir(home)
		mkdir(proj)
		env = map[string]string{}
		out, errw = &bytes.Buffer{}, &bytes.Buffer{}
		t := true
		in = launch.Inputs{
			ProjectDir: proj, Home: home,
			HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(k string) string { return env[k] },
			TempDir:   filepath.Join(base, "shadow"),
			ImageName: "claude-sandbox-proj",
			CLIAWS:    &t,
			Out:       out, Err: errw,
		}
		mkdir(in.TempDir)
		DeferCleanup(launch.CaptureFingerprintText(func(s string) { hashText = s }))
	})

	build := func() *launch.Plan {
		p, err := launch.Build(in)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	scopeFor := func(norm string) string {
		sum := sha256.Sum256([]byte(norm))
		return filepath.Join(home, ".cache", "claude-sandbox", "aws", hex.EncodeToString(sum[:])[:12])
	}
	cacheBinds := func(p *launch.Plan) []string {
		var outv []string
		for _, v := range p.Volumes {
			parts := strings.Split(v, ":")
			if len(parts) >= 2 && (parts[1] == filepath.Join(awsDir, "sso/cache") || parts[1] == filepath.Join(awsDir, "cli/cache")) {
				outv = append(outv, v)
			}
		}
		return outv
	}
	evidence := func(p *launch.Plan) []string {
		var outv []string
		for _, k := range launch.AWSEvidenceEnv[:3] { // the fourth is asserted where it matters
			v, ok := envValue(p, k)
			Expect(ok).To(BeTrue(), k)
			outv = append(outv, v)
		}
		return outv
	}
	expectReadonly := func(p *launch.Plan) {
		Expect(cacheBinds(p)).To(BeEmpty())
		Expect(p.Tmpfs).To(BeEmpty())
		Expect(hashText).To(ContainSubstring("awsTokenCache=readonly\n"))
		Expect(out.String()).NotTo(ContainSubstring("AWS: "))
	}

	// ---- CS-LNCH-229 ----

	It("CS-LNCH-229: binds the scope's sso and cli over ~/.aws/sso/cache and ~/.aws/cli/cache, after the read-only ~/.aws", func() {
		mkdir(awsDir)
		p := build()
		scope := scopeFor(filepath.Join(home, ".claude"))
		Expect(p.Volumes).To(ContainElements(
			filepath.Join(scope, "sso")+":"+filepath.Join(awsDir, "sso/cache"),
			filepath.Join(scope, "cli")+":"+filepath.Join(awsDir, "cli/cache"),
		))
		ro := slices.Index(p.Volumes, awsDir+":"+awsDir+":ro")
		Expect(ro).To(BeNumerically(">=", 0))
		Expect(slices.Index(p.Volumes, filepath.Join(scope, "sso")+":"+filepath.Join(awsDir, "sso/cache"))).To(BeNumerically(">", ro))
		Expect(filepath.Join(scope, "sso")).To(BeADirectory())
		Expect(filepath.Join(scope, "cli")).To(BeADirectory())
		Expect(hashText).To(ContainSubstring("awsTokenCache=sandbox\n"))
	})

	It("CS-LNCH-229: the key is the cleaned, symlink-resolved CLAUDE_CONFIG_DIR; different dirs get different scopes", func() {
		real := filepath.Join(home, "cfg-real")
		mkdir(real)
		link := filepath.Join(home, "cfg-link")
		Expect(os.Symlink(real, link)).To(Succeed())
		env["CLAUDE_CONFIG_DIR"] = link + "/"
		got, ok := launch.AWSScopeDir(home, link+"/")
		Expect(ok).To(BeTrue())
		Expect(got).To(Equal(scopeFor(real)))
		p := build()
		Expect(cacheBinds(p)).To(ContainElement(filepath.Join(scopeFor(real), "sso") + ":" + filepath.Join(awsDir, "sso/cache")))

		other, ok := launch.AWSScopeDir(home, filepath.Join(home, "elsewhere"))
		Expect(ok).To(BeTrue())
		Expect(other).NotTo(Equal(scopeFor(real)))
		def, _ := launch.AWSScopeDir(home, "")
		Expect(def).To(Equal(scopeFor(filepath.Join(home, ".claude"))))
		Expect(def).NotTo(Equal(other))
	})

	DescribeTable("CS-LNCH-229: a relative or ~ CLAUDE_CONFIG_DIR stands down to readonly with one warning",
		func(v string) {
			mkdir(awsDir)
			env["CLAUDE_CONFIG_DIR"] = v
			p := build()
			expectReadonly(p)
			Expect(strings.Count(errw.String(), "WARNING: AWS sign-ins")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring("is not an absolute path"))
			Expect(evidence(p)).To(Equal([]string{awsDir, "readonly", ""}))
		},
		Entry("relative", "cfg"),
		Entry("tilde", "~/.claude"),
	)

	// ---- CS-LNCH-230 ----

	It("CS-LNCH-230: missing mount points are created 0700; an existing one keeps its mode", func() {
		mkdir(awsDir)
		Expect(os.Mkdir(filepath.Join(awsDir, "sso"), 0o755)).To(Succeed())
		build()
		Expect(modeOf(filepath.Join(awsDir, "sso"))).To(Equal(os.FileMode(0o755)))
		for _, d := range []string{"sso/cache", "cli", "cli/cache"} {
			Expect(modeOf(filepath.Join(awsDir, d))).To(Equal(os.FileMode(0o700)), d)
		}
	})

	It("CS-LNCH-230: a symlinked ~/.aws is followed; the folders land in its target", func() {
		target := filepath.Join(home, "dotfiles", "aws")
		mkdir(target)
		Expect(os.Symlink(target, awsDir)).To(Succeed())
		p := build()
		Expect(filepath.Join(target, "sso", "cache")).To(BeADirectory())
		Expect(filepath.Join(target, "cli", "cache")).To(BeADirectory())
		Expect(cacheBinds(p)).To(HaveLen(2))
	})

	It("CS-LNCH-230: a symlink at a level below ~/.aws is refused: readonly, one warning", func() {
		mkdir(awsDir)
		elsewhere := filepath.Join(home, "elsewhere")
		mkdir(elsewhere)
		Expect(os.Symlink(elsewhere, filepath.Join(awsDir, "sso"))).To(Succeed())
		p := build()
		expectReadonly(p)
		Expect(strings.Count(errw.String(), "WARNING: AWS sign-ins")).To(Equal(1))
		Expect(errw.String()).To(ContainSubstring(filepath.Join(awsDir, "sso")))
		Expect(filepath.Join(elsewhere, "cache")).NotTo(BeAnExistingFile())
	})

	It("CS-LNCH-230: a non-directory or another uid's directory is refused", func() {
		mkdir(awsDir)
		touch(filepath.Join(awsDir, "cli"), "")
		expectReadonly(build())
		Expect(errw.String()).To(ContainSubstring("not a directory"))

		Expect(os.Remove(filepath.Join(awsDir, "cli"))).To(Succeed())
		mkdir(filepath.Join(awsDir, "sso"))
		errw.Reset()
		// The scope dirs are checked first: let them pass, then refuse ~/.aws/sso.
		scope := scopeFor(filepath.Join(home, ".claude"))
		mkdir(filepath.Join(scope, "sso"))
		mkdir(filepath.Join(scope, "cli"))
		in.NoCreatePeerDirs = true // checks only, so the fake uid is not asked to own a created dir
		in.Getuid = func() int { return 4242 }
		expectReadonly(build())
		Expect(errw.String()).To(ContainSubstring("owned by uid"))
	})

	// ---- CS-LNCH-231 ----

	It("CS-LNCH-231: the env var wins, then the config, then sandbox; empty falls through; any case", func() {
		get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
		ro, host := "readonly", "Host"
		cfg := &cascade.Config{}
		cfg.HostAccess.AWS.TokenCache = &host
		Expect(launch.ResolveAWSTokenCache(get(map[string]string{launch.AWSTokenCacheEnv: ro}), cfg, "")).To(Equal("readonly"))
		Expect(launch.ResolveAWSTokenCache(get(map[string]string{launch.AWSTokenCacheEnv: " "}), cfg, "")).To(Equal("host"))
		Expect(launch.ResolveAWSTokenCache(get(nil), &cascade.Config{}, "")).To(Equal("sandbox"))
		empty := ""
		cfg.HostAccess.AWS.TokenCache = &empty
		Expect(launch.ResolveAWSTokenCache(get(nil), cfg, "")).To(Equal("sandbox"))
	})

	It("CS-LNCH-231: an invalid value errors naming the variable or the file", func() {
		_, err := launch.ResolveAWSTokenCache(func(k string) string {
			if k == launch.AWSTokenCacheEnv {
				return "shared"
			}
			return ""
		}, nil, "")
		Expect(err).To(MatchError(ContainSubstring(`CLAUDE_SANDBOX_AWS_TOKEN_CACHE="shared"`)))
		bad := "both"
		cfg := &cascade.Config{}
		cfg.HostAccess.AWS.TokenCache = &bad
		_, err = launch.ResolveAWSTokenCache(func(string) string { return "" }, cfg, "/w/.claude-sandbox/config.yaml")
		Expect(err).To(MatchError(ContainSubstring(`hostAccess.aws.tokenCache: "both" in /w/.claude-sandbox/config.yaml`)))
		// Build refuses it too, when aws is on.
		in.Cfg = cfg
		_, err = launch.Build(in)
		Expect(err).To(HaveOccurred())
	})

	It("CS-LNCH-231: KeyPathSourceOf names the most-local file setting the nested key", func() {
		snap := []cascade.ConfigFile{
			{Path: "/a", Content: []byte("hostAccess:\n  aws:\n    tokenCache: host\n")},
			{Path: "/b", Content: []byte("hostAccess:\n  aws:\n    enabled: true\n")},
		}
		Expect(cascade.KeyPathSourceOf(snap, "hostAccess", "aws", "tokenCache")).To(Equal("/a"))
		cfg, err := cascade.LoadSnapshot(snap)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.HostAccess.AWS.TokenCache).To(HaveValue(Equal("host")))
	})

	// ---- CS-LNCH-232 ----

	It("CS-LNCH-232: host binds the host's caches read-write at their own paths, with the banner", func() {
		mkdir(awsDir)
		env[launch.AWSTokenCacheEnv] = "host"
		p := build()
		sso, cli := filepath.Join(awsDir, "sso/cache"), filepath.Join(awsDir, "cli/cache")
		Expect(cacheBinds(p)).To(ConsistOf(sso+":"+sso, cli+":"+cli))
		Expect(sso).To(BeADirectory())
		Expect(strings.Count(out.String(), "AWS: ")).To(Equal(1))
		Expect(out.String()).To(ContainSubstring("a sign-out here signs out every session on the host"))
		Expect(hashText).To(ContainSubstring("awsTokenCache=host\n"))
		Expect(filepath.Join(home, ".cache", "claude-sandbox", "aws")).NotTo(BeAnExistingFile())
	})

	It("CS-LNCH-232: host with no host ~/.aws creates nothing, warns once, readonly", func() {
		env[launch.AWSTokenCacheEnv] = "host"
		expectReadonly(build())
		Expect(awsDir).NotTo(BeAnExistingFile())
		Expect(strings.Count(errw.String(), "WARNING: hostAccess.aws.tokenCache: host")).To(Equal(1))
	})

	// ---- CS-LNCH-233 ----

	It("CS-LNCH-233: readonly adds only the read-only mounts and creates nothing", func() {
		mkdir(awsDir)
		env[launch.AWSTokenCacheEnv] = "readonly"
		p := build()
		expectReadonly(p)
		Expect(p.Volumes).To(ContainElement(awsDir + ":" + awsDir + ":ro"))
		Expect(filepath.Join(home, ".cache", "claude-sandbox", "aws")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(awsDir, "sso")).NotTo(BeAnExistingFile())
		Expect(evidence(p)).To(Equal([]string{awsDir, "readonly", ""}))
	})

	// ---- CS-LNCH-234 ----

	It("CS-LNCH-234: no host ~/.aws: nothing created under it, the cache still bound, AWS_HOME empty", func() {
		p := build()
		Expect(awsDir).NotTo(BeAnExistingFile())
		Expect(cacheBinds(p)).To(HaveLen(2))
		scope := scopeFor(filepath.Join(home, ".claude"))
		Expect(evidence(p)).To(Equal([]string{"", "sandbox", scope}))
	})

	// ---- CS-LNCH-235 ----

	DescribeTable("CS-LNCH-235: a cascade mount covering a cache dir is kept; nothing added or created",
		func(container string, writable bool) {
			mkdir(awsDir)
			c := filepath.Join(home, container)
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: c, Container: c, Writable: writable}}}
			p := build()
			Expect(p.Volumes).NotTo(ContainElement(ContainSubstring("claude-sandbox/aws")))
			Expect(p.Volumes).NotTo(ContainElement(HavePrefix(filepath.Join(awsDir, "sso/cache") + ":")))
			Expect(hashText).To(ContainSubstring("awsTokenCache=readonly\n"))
			Expect(evidence(p)).To(Equal([]string{"", "readonly", ""}))
			Expect(filepath.Join(home, ".cache", "claude-sandbox", "aws")).NotTo(BeAnExistingFile())
			if writable {
				Expect(errw.String()).NotTo(ContainSubstring("WARNING"))
			} else {
				Expect(strings.Count(errw.String(), "WARNING: AWS sign-ins cannot be saved")).To(Equal(1))
			}
		},
		Entry("~/.aws/sso, writable", ".aws/sso", true),
		Entry("~/.aws/cli/cache, read-only", ".aws/cli/cache", false),
	)

	// ---- CS-LNCH-236 ----

	It("CS-LNCH-236: aws off still passes all three, empty", func() {
		f := false
		in.CLIAWS = &f
		p := build()
		Expect(evidence(p)).To(Equal([]string{"", "", ""}))
		Expect(hashText).NotTo(ContainSubstring("awsTokenCache"))
	})

	It("CS-LNCH-236: the outer exports its mounted ~/.aws, mode and scope dir", func() {
		mkdir(awsDir)
		p := build()
		Expect(evidence(p)).To(Equal([]string{awsDir, "sandbox", scopeFor(filepath.Join(home, ".claude"))}))
		args := p.CreateArgs(proj)
		Expect(args).To(ContainElement("CLAUDE_SANDBOX_AWS_CACHE=sandbox"))
	})

	It("CS-LNCH-236: an env file defining one still loses to -e, with one warning naming it", func() {
		ef := filepath.Join(proj, "env")
		touch(ef, "CLAUDE_SANDBOX_AWS_HOME=/home/x/.aws\n")
		in.EnvFiles = []string{ef}
		f := false
		in.CLIAWS = &f
		p := build()
		Expect(evidence(p)).To(Equal([]string{"", "", ""}))
		Expect(strings.Count(errw.String(), "WARNING: CLAUDE_SANDBOX_AWS_HOME in an env file is ignored")).To(Equal(1))
	})

	Describe("CS-LNCH-236: inside a sandbox", func() {
		var scope string
		BeforeEach(func() {
			env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
			mkdir(awsDir)
			scope = scopeFor(filepath.Join(home, ".claude"))
		})
		noneCreated := func() {
			Expect(filepath.Join(home, ".cache", "claude-sandbox", "aws")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(awsDir, "sso")).NotTo(BeAnExistingFile())
		}

		It("CS-LNCH-236: no evidence: no host ~/.aws, no path-var dir, one Note, none", func() {
			creds := filepath.Join(home, "creds", "credentials")
			touch(creds, "")
			env["AWS_SHARED_CREDENTIALS_FILE"] = creds
			p := build()
			for _, v := range p.Volumes {
				Expect(v).NotTo(ContainSubstring(awsDir))
				Expect(v).NotTo(ContainSubstring(filepath.Dir(creds)))
			}
			Expect(strings.Count(out.String(), "Note: the host's "+awsDir+" is not mounted")).To(Equal(1))
			Expect(hashText).To(ContainSubstring("awsTokenCache=none\n"))
			Expect(evidence(p)).To(Equal([]string{"", "none", ""}))
			Expect(filepath.Join(home, ".cache", "claude-sandbox", "aws")).NotTo(BeAnExistingFile())
		})

		It("CS-LNCH-236: evidence sandbox with this scope binds it, creating nothing", func() {
			env[launch.AWSHomeEnv] = awsDir + "/"
			env[launch.AWSCacheEnv] = "sandbox"
			env[launch.AWSCacheDirEnv] = scope
			env["AWS_LOGIN_CACHE_DIRECTORY"] = filepath.Join(scope, "login")
			p := build()
			Expect(p.Volumes).To(ContainElement(awsDir + ":" + awsDir + ":ro"))
			Expect(cacheBinds(p)).To(ConsistOf(
				filepath.Join(scope, "sso")+":"+filepath.Join(awsDir, "sso/cache"),
				filepath.Join(scope, "cli")+":"+filepath.Join(awsDir, "cli/cache")))
			Expect(p.Volumes).To(ContainElement(filepath.Join(scope, "login") + ":" + filepath.Join(scope, "login")))
			Expect(hashText).To(ContainSubstring("awsTokenCache=sandbox\n"))
			Expect(evidence(p)).To(Equal([]string{awsDir, "sandbox", scope}))
			noneCreated()
		})

		It("CS-LNCH-236: evidence sandbox for another scope gets the tmpfs", func() {
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "sandbox"
			env[launch.AWSCacheDirEnv] = scope + "x"
			p := build()
			Expect(cacheBinds(p)).To(BeEmpty())
			Expect(p.Tmpfs).To(HaveLen(2))
			Expect(hashText).To(ContainSubstring("awsTokenCache=container\n"))
			noneCreated()
		})

		It("CS-LNCH-236: evidence host with an inner sandbox gets a 0700 tmpfs of the user's, with one Note", func() {
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "host"
			p := build()
			Expect(cacheBinds(p)).To(BeEmpty())
			want := filepath.Join(awsDir, "sso/cache") + ":rw,nosuid,nodev,noexec,mode=0700,uid=1000,gid=1000"
			Expect(p.Tmpfs).To(ContainElement(want))
			args := p.CreateArgs(proj)
			Expect(argPairs(args, "--tmpfs")).To(ContainElement(want))
			Expect(strings.Count(out.String(), "Note: AWS sign-ins in this session are its own")).To(Equal(1))
			Expect(evidence(p)).To(Equal([]string{awsDir, "container", ""}))
			noneCreated()
		})

		It("CS-LNCH-236: evidence host with an inner host binds the host's caches", func() {
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "host"
			env[launch.AWSTokenCacheEnv] = "host"
			p := build()
			sso := filepath.Join(awsDir, "sso/cache")
			Expect(cacheBinds(p)).To(ContainElement(sso + ":" + sso))
			Expect(hashText).To(ContainSubstring("awsTokenCache=host\n"))
			noneCreated()
		})

		It("CS-LNCH-236: evidence readonly gives the read-only mount only", func() {
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "readonly"
			p := build()
			expectReadonly(p)
			Expect(p.Volumes).To(ContainElement(awsDir + ":" + awsDir + ":ro"))
			noneCreated()
		})

		It("CS-LNCH-236: the outer's raw CLAUDE_CONFIG_DIR matching this launch's own shares the scope though symlinks resolve differently", func() {
			// The host keyed the scope on the resolved path; here the raw
			// path resolves to itself, so the computed scope differs.
			raw := filepath.Join(home, "cfg-link")
			hostScope := filepath.Join(home, ".cache", "claude-sandbox", "aws", "0123456789ab")
			env["CLAUDE_CONFIG_DIR"] = raw
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "sandbox"
			env[launch.AWSCacheDirEnv] = hostScope
			env[launch.AWSConfigDirEnv] = raw
			p := build()
			Expect(cacheBinds(p)).To(ContainElement(filepath.Join(hostScope, "sso") + ":" + filepath.Join(awsDir, "sso/cache")))
			v, _ := envValue(p, launch.AWSConfigDirEnv)
			Expect(v).To(Equal(raw))
			noneCreated()
		})

		It("CS-LNCH-236: a different raw CLAUDE_CONFIG_DIR, or a dir outside the AWS cache root, does not match", func() {
			env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, "cfg-a")
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "sandbox"
			env[launch.AWSCacheDirEnv] = filepath.Join(home, ".cache", "claude-sandbox", "aws", "0123456789ab")
			env[launch.AWSConfigDirEnv] = filepath.Join(home, "cfg-b")
			Expect(build().Tmpfs).To(HaveLen(2))
			env[launch.AWSConfigDirEnv] = env["CLAUDE_CONFIG_DIR"]
			env[launch.AWSCacheDirEnv] = filepath.Join(home, "elsewhere", "0123456789ab")
			Expect(build().Tmpfs).To(HaveLen(2))
		})

		It("CS-LNCH-236: with no ~/.aws mounted, a bind holding ~/.aws rules the scope binds out", func() {
			Expect(os.RemoveAll(awsDir)).To(Succeed())
			env[launch.AWSCacheEnv] = "sandbox"
			env[launch.AWSCacheDirEnv] = scope
			in.ProjectDir = home
			in.MountInfo = func() (string, error) { return withBind(home), nil }
			p := build()
			Expect(cacheBinds(p)).To(BeEmpty())
			Expect(hashText).To(ContainSubstring("awsTokenCache=none\n"))
			Expect(awsDir).NotTo(BeAnExistingFile())
		})

		It("CS-LNCH-236: a path-var directory needs the outer's mount too (nestedBindSourceOK)", func() {
			env[launch.AWSHomeEnv] = awsDir
			env[launch.AWSCacheEnv] = "readonly"
			creds := filepath.Join(home, "creds", "credentials")
			touch(creds, "")
			env["AWS_SHARED_CREDENTIALS_FILE"] = creds
			in.MountInfo = func() (string, error) { return rootOnly, nil }
			p := build()
			Expect(p.Volumes).NotTo(ContainElement(ContainSubstring(filepath.Dir(creds))))
			Expect(strings.Count(errw.String(), "WARNING: AWS_SHARED_CREDENTIALS_FILE="+creds)).To(Equal(1))
			in.MountInfo = func() (string, error) { return withBind(filepath.Dir(creds)), nil }
			errw.Reset()
			p = build()
			Expect(p.Volumes).To(ContainElement(filepath.Dir(creds) + ":" + filepath.Dir(creds) + ":ro"))
		})
	})

	// ---- CS-LNCH-237 ----

	It("CS-LNCH-237: sandbox and host hash differently, though their mount lines match", func() {
		mkdir(awsDir)
		a := build().ConfigHash
		env[launch.AWSTokenCacheEnv] = "host"
		out.Reset()
		b := build().ConfigHash
		Expect(a).NotTo(Equal(b))
	})

	It("CS-LNCH-237: the drift build creates nothing, and a missing dir is not a refusal", func() {
		mkdir(awsDir)
		in.NoCreatePeerDirs = true
		drift := build()
		Expect(hashText).To(ContainSubstring("awsTokenCache=sandbox\n"))
		Expect(filepath.Join(home, ".cache", "claude-sandbox", "aws")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(awsDir, "sso")).NotTo(BeAnExistingFile())
		in.NoCreatePeerDirs = false
		Expect(build().ConfigHash).To(Equal(drift.ConfigHash))
	})

	It("CS-LNCH-237: the drift build sweeps nothing and a refused dir reads readonly", func() {
		scope := scopeFor(filepath.Join(home, ".claude"))
		mkdir(filepath.Join(scope, "sso"))
		link := filepath.Join(scope, "sso", "planted")
		Expect(os.Symlink("/nowhere", link)).To(Succeed())
		Expect(os.Symlink(home, filepath.Join(scope, "cli"))).To(Succeed())
		in.NoCreatePeerDirs = true
		expectReadonly(build())
		_, err := os.Lstat(link)
		Expect(err).NotTo(HaveOccurred())
	})

	// ---- CS-LNCH-238 ----

	It("CS-LNCH-238: one AWS: line names the config folder, the scope dir and both ways back", func() {
		build()
		scope := scopeFor(filepath.Join(home, ".claude"))
		var lines []string
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(l, "AWS: ") {
				lines = append(lines, l)
			}
		}
		Expect(lines).To(HaveLen(1))
		line := lines[0]
		Expect(line).To(ContainSubstring(filepath.Join(home, ".claude") + " (kept in " + scope + ")"))
		Expect(line).To(ContainSubstring(`"aws sso login --use-device-code"`))
		Expect(line).To(ContainSubstring("hostAccess.aws.tokenCache: host"))
		Expect(line).To(ContainSubstring("readonly"))
	})

	// ---- CS-LNCH-239 ----

	It("CS-LNCH-239: the aws login folder is made 0700, bound at its own path and named", func() {
		p := build()
		login := filepath.Join(scopeFor(filepath.Join(home, ".claude")), "login")
		Expect(modeOf(login)).To(Equal(os.FileMode(0o700)))
		Expect(p.Volumes).To(ContainElement(login + ":" + login))
		v, ok := envValue(p, "AWS_LOGIN_CACHE_DIRECTORY")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(login))
	})

	It("CS-LNCH-239: an env file defining AWS_LOGIN_CACHE_DIRECTORY wins, silently; the folder is still made the user's", func() {
		ef := filepath.Join(proj, "env")
		touch(ef, "AWS_LOGIN_CACHE_DIRECTORY=/work/login\n")
		in.EnvFiles = []string{ef}
		p := build()
		login := filepath.Join(scopeFor(filepath.Join(home, ".claude")), "login")
		Expect(modeOf(login)).To(Equal(os.FileMode(0o700)))
		Expect(p.Volumes).NotTo(ContainElement(ContainSubstring(login)))
		_, ok := envValue(p, "AWS_LOGIN_CACHE_DIRECTORY")
		Expect(ok).To(BeFalse())
		Expect(errw.String()).NotTo(ContainSubstring("login"))
	})

	It("CS-LNCH-240: the login folder is swept too, but not by the drift build", func() {
		login := filepath.Join(scopeFor(filepath.Join(home, ".claude")), "login")
		mkdir(login)
		link := filepath.Join(login, "planted")
		Expect(os.Symlink(filepath.Join(home, "victim"), link)).To(Succeed())
		in.NoCreatePeerDirs = true
		build()
		_, err := os.Lstat(link)
		Expect(err).NotTo(HaveOccurred())
		in.NoCreatePeerDirs = false
		build()
		_, err = os.Lstat(link)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	// ---- fix round 1: CS-LNCH-234, 236, 237 ----

	It("CS-LNCH-234: no host ~/.aws and the project is the home: ~/.aws and its four levels are created as the user", func() {
		in.ProjectDir = home
		p := build()
		Expect(modeOf(awsDir)).To(Equal(os.FileMode(0o700)))
		for _, d := range []string{"sso", "sso/cache", "cli", "cli/cache"} {
			Expect(modeOf(filepath.Join(awsDir, d))).To(Equal(os.FileMode(0o700)), d)
		}
		Expect(cacheBinds(p)).To(HaveLen(2))
		Expect(hashText).To(ContainSubstring("awsTokenCache=sandbox\n"))
	})

	It("CS-LNCH-234: the drift build of that launch creates nothing and hashes alike", func() {
		in.ProjectDir = home
		// A first launch with aws off makes the project-is-home shadow
		// placeholders (CS-LNCH-169), which would differ otherwise.
		off, on := false, true
		in.CLIAWS = &off
		build()
		in.CLIAWS = &on
		in.NoCreatePeerDirs = true
		drift := build()
		driftText := hashText
		Expect(awsDir).NotTo(BeAnExistingFile())
		in.NoCreatePeerDirs = false
		p2 := build()
		Expect(hashText).To(Equal(driftText))
		Expect(p2.ConfigHash).To(Equal(drift.ConfigHash))
	})

	It("CS-LNCH-234: the config dir mount holding ~/.aws counts the same way", func() {
		env["CLAUDE_CONFIG_DIR"] = home
		build()
		Expect(filepath.Join(awsDir, "cli", "cache")).To(BeADirectory())
	})

	It("CS-LNCH-234: when ~/.aws cannot be created under that bind, readonly with one warning", func() {
		in.ProjectDir = home
		scope := scopeFor(filepath.Join(home, ".claude"))
		for _, d := range []string{"sso", "cli", "login"} {
			mkdir(filepath.Join(scope, d))
		}
		mkdir(filepath.Join(home, ".cache", "claude-sandbox", "pre-commit"))
		Expect(os.Chmod(home, 0o500)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(home, 0o755) })
		if syscall.Access(home, 0x2) == nil {
			Skip("running as a user that can write a 0500 directory (root)")
		}
		p := build()
		Expect(cacheBinds(p)).To(BeEmpty())
		Expect(hashText).To(ContainSubstring("awsTokenCache=readonly\n"))
		Expect(strings.Count(errw.String(), "WARNING: AWS sign-ins in this session use the host's read-only cache: cannot prepare "+awsDir)).To(Equal(1))
		Expect(awsDir).NotTo(BeAnExistingFile())
	})

	It("CS-LNCH-237: the drift build of a launch whose mkdir would fail reads readonly, as the launch does", func() {
		mkdir(awsDir)
		Expect(os.Chmod(awsDir, 0o500)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(awsDir, 0o755) })
		if syscall.Access(awsDir, 0x2) == nil {
			Skip("running as a user that can write a 0500 directory (root)")
		}
		in.NoCreatePeerDirs = true
		drift := build()
		Expect(hashText).To(ContainSubstring("awsTokenCache=readonly\n"))
		in.NoCreatePeerDirs = false
		errw.Reset()
		launch := build()
		Expect(hashText).To(ContainSubstring("awsTokenCache=readonly\n"))
		Expect(launch.ConfigHash).To(Equal(drift.ConfigHash))
	})

	// ---- CS-LNCH-240 ----

	It("CS-LNCH-240: every scope level is tightened to 0700 at every launch", func() {
		scope := scopeFor(filepath.Join(home, ".claude"))
		Expect(os.MkdirAll(filepath.Join(scope, "sso"), 0o755)).To(Succeed())
		Expect(os.Chmod(scope, 0o755)).To(Succeed())
		build()
		for _, d := range []string{filepath.Dir(scope), scope, filepath.Join(scope, "sso"), filepath.Join(scope, "cli")} {
			Expect(modeOf(d)).To(Equal(os.FileMode(0o700)), d)
		}
	})

	It("CS-LNCH-240: a symlinked scope dir stands down to readonly with one warning", func() {
		scope := scopeFor(filepath.Join(home, ".claude"))
		mkdir(scope)
		elsewhere := filepath.Join(home, "elsewhere")
		mkdir(elsewhere)
		Expect(os.Symlink(elsewhere, filepath.Join(scope, "sso"))).To(Succeed())
		expectReadonly(build())
		Expect(strings.Count(errw.String(), "WARNING: AWS sign-ins in this session use the host's read-only cache")).To(Equal(1))
		Expect(modeOf(elsewhere)).To(Equal(os.FileMode(0o755)))
	})

	It("CS-LNCH-240: the sweep unlinks non-regular entries without following them, and keeps regular files", func() {
		scope := scopeFor(filepath.Join(home, ".claude"))
		sso, cli := filepath.Join(scope, "sso"), filepath.Join(scope, "cli")
		mkdir(sso)
		mkdir(cli)
		victim := filepath.Join(home, "victim")
		touch(victim, "keep")
		touch(filepath.Join(sso, "token.json"), "{}")
		Expect(os.Symlink(victim, filepath.Join(sso, "link.json"))).To(Succeed())
		Expect(syscall.Mkfifo(filepath.Join(cli, "fifo"), 0o600)).To(Succeed())
		mkdir(filepath.Join(cli, "empty"))
		touch(filepath.Join(cli, "full", "x"), "")
		build()
		Expect(filepath.Join(sso, "token.json")).To(BeARegularFile())
		for _, gone := range []string{filepath.Join(sso, "link.json"), filepath.Join(cli, "fifo"), filepath.Join(cli, "empty")} {
			_, err := os.Lstat(gone)
			Expect(os.IsNotExist(err)).To(BeTrue(), gone)
		}
		Expect(os.ReadFile(victim)).To(Equal([]byte("keep")))
		Expect(filepath.Join(cli, "full")).To(BeADirectory())
		Expect(strings.Count(errw.String(), "WARNING: "+filepath.Join(cli, "full"))).To(Equal(1))
	})
})
