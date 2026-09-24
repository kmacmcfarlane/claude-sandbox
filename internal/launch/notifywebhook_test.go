package launch_test

// Spec: spec/launch.feature CS-LNCH-111. The baked Notification hook used to
// post a fixed "🔔 Claude Code needs your input"; with a dozen concurrent
// sandboxes that named nothing. The hook body now lives in
// bin/notify-webhook, which the tools image ships (CS-IMG-051), and the
// launcher supplies the facts the container did not have:
// CLAUDE_SANDBOX_INSTANCE, CLAUDE_SANDBOX_CONTAINER and CLAUDE_SANDBOX_MODE.
//
// The script is bash + jq + curl. These tests run it with a stub curl on a
// hermetic PATH, so nothing ever reaches a real webhook, and with an explicit
// Env, so a CLAUDE_NOTIFICATION_WEBHOOK_URL in the developer's own environment
// cannot leak in.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("CS-LNCH-111: the notification ping names the waiting session", func() {
	Describe("the launcher's half: the container's own identity", func() {
		var (
			home, proj string
			in         launch.Inputs
		)

		BeforeEach(func() {
			base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			home = filepath.Join(base, "home")
			proj = filepath.Join(base, "proj")
			Expect(os.MkdirAll(home, 0o755)).To(Succeed())
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			in = launch.Inputs{
				ProjectDir: proj,
				Home:       home,
				HostUID:    1000,
				HostGID:    1000,
				HostUser:   "tester",
				Getenv:     func(string) string { return "" },
				TempDir:    filepath.Join(base, "shadow"),
				ImageName:  "claude-sandbox-proj",
				Instance:   "otter",
				Out:        &strings.Builder{},
				Err:        &strings.Builder{},
			}
			Expect(os.MkdirAll(in.TempDir, 0o755)).To(Succeed())
		})

		build := func() *launch.Plan {
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			return p
		}

		It("CS-LNCH-111: exports the instance noun and the container name", func() {
			p := build()
			Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_INSTANCE=otter"))
			Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_CONTAINER=" + p.ContainerName))
			Expect(p.ContainerName).To(HaveSuffix("-otter"))
			// They reach the container the same way the pid class does.
			Expect(p.CreateArgs(proj)).To(ContainElements("-e", "CLAUDE_SANDBOX_INSTANCE=otter"))
		})

		It("CS-LNCH-111: exports the mode as the claude-sandbox.mode label has it", func() {
			Expect(build().EnvFlags).To(ContainElement("CLAUDE_SANDBOX_MODE=claude"))
			in.Headless = true
			p := build()
			Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_MODE=" + launch.ModeHeadless))
			Expect(p.Labels).To(ContainElement("claude-sandbox.mode=" + launch.ModeHeadless))
			// Never the join marker: that rides a join's own exec (CS-SESS-075).
			Expect(p.EnvFlags).NotTo(ContainElement(HavePrefix("CLAUDE_SANDBOX_JOINED=")))
		})

		It("CS-LNCH-111: ralph has no noun, so it gets the container name only — as with the label", func() {
			in.RalphMode = true
			in.Instance = ""
			p := build()
			Expect(p.EnvFlags).NotTo(ContainElement(HavePrefix("CLAUDE_SANDBOX_INSTANCE=")))
			Expect(p.Labels).NotTo(ContainElement(HavePrefix("claude-sandbox.instance=")))
			Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_CONTAINER=" + p.ContainerName))
			Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_MODE=ralph"))
			Expect(p.ContainerName).To(HaveSuffix("-ralph"))
		})

		It("CS-LNCH-111: neither is in the config fingerprint — a new session is not drift", func() {
			in.Cfg = &cascade.Config{}
			first := build()
			in.Instance = "heron"
			second := build()
			Expect(second.ContainerName).NotTo(Equal(first.ContainerName))
			Expect(second.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_INSTANCE=heron"))
			Expect(second.ConfigHash).To(Equal(first.ConfigHash))
		})
	})

	Describe("the script's half: what it posts", func() {
		var (
			root, bindir, cfg, sink string
			run                     func(stdin string, env ...string)
		)

		BeforeEach(func() {
			root = GinkgoT().TempDir()
			bindir = filepath.Join(root, "bin")
			cfg = filepath.Join(root, "cfg")
			sink = filepath.Join(root, "sink")
			for _, d := range []string{bindir, filepath.Join(cfg, "sessions")} {
				Expect(os.MkdirAll(d, 0o755)).To(Succeed())
			}
			// A hermetic PATH: only the tools the script uses. A missing one
			// fails the spec rather than skipping — the image ships them all.
			bash, err := exec.LookPath("bash")
			Expect(err).NotTo(HaveOccurred(), "bash is required for the CS-LNCH-111 script tests")
			for _, tool := range []string{"jq", "cat"} {
				p, err := exec.LookPath(tool)
				Expect(err).NotTo(HaveOccurred(), "%s is required for the CS-LNCH-111 script tests", tool)
				Expect(os.Symlink(p, filepath.Join(bindir, tool))).To(Succeed())
			}
			// The stub curl: it records its argv (one per line), the -K
			// config it was handed and the request body instead of making a
			// request. Nothing in this suite can reach a real webhook.
			cat := filepath.Join(bindir, "cat")
			stub := "#!/bin/bash\ncfg=''\nprev=''\nfor a in \"$@\"; do [ \"$prev\" = -K ] && cfg=$a; prev=$a; done\n" +
				"printf '%s\\n' \"$@\" > " + sink + ".argv\n" +
				"{ " + cat + " \"$cfg\"; " + cat + "; } > " + sink + "\n"
			Expect(os.WriteFile(filepath.Join(bindir, "curl"), []byte(stub), 0o755)).To(Succeed())

			script, err := filepath.Abs(filepath.Join("..", "..", "bin", "notify-webhook"))
			Expect(err).NotTo(HaveOccurred())
			run = func(stdin string, extra ...string) {
				cmd := exec.Command(bash, script)
				cmd.Stdin = strings.NewReader(stdin)
				// An explicit Env: the developer's own
				// CLAUDE_NOTIFICATION_WEBHOOK_URL, CLAUDE_PID and
				// CLAUDE_CODE_SESSION_ID must not leak in.
				cmd.Env = append([]string{
					"PATH=" + bindir,
					"HOME=" + filepath.Join(root, "home"),
					"CLAUDE_CONFIG_DIR=" + cfg,
				}, extra...)
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), "the hook must never fail a session: %s", out)
				Expect(string(out)).To(BeEmpty(), "the hook must be silent")
			}
		})

		// posted returns the URL curl was given and the "content" of the JSON
		// body, or ok=false when nothing was posted at all.
		posted := func() (url, content string, ok bool) {
			raw, err := os.ReadFile(sink)
			if err != nil {
				return "", "", false
			}
			cfgLine, body, found := strings.Cut(string(raw), "\n")
			Expect(found).To(BeTrue(), "stub curl wrote no body: %q", raw)
			Expect(cfgLine).To(HavePrefix(`url = "`))
			url = strings.TrimSuffix(strings.TrimPrefix(cfgLine, `url = "`), `"`)
			// The URL reaches curl only through -K, never through argv.
			argv, err := os.ReadFile(sink + ".argv")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(argv)).NotTo(ContainSubstring(url))
			Expect(string(argv)).To(ContainSubstring("--data-binary\n@-\n"))
			var m map[string]any
			Expect(json.Unmarshal([]byte(body), &m)).To(Succeed(), "body must be valid JSON: %s", body)
			// Exactly these three keys: nothing else leaves the container.
			Expect(m).To(HaveLen(3))
			// flags 4 = SUPPRESS_EMBEDS: no link in a name unfurls.
			Expect(m).To(HaveKeyWithValue("flags", 4.0))
			// No interpolated text can become an @everyone.
			Expect(m).To(HaveKeyWithValue("allowed_mentions", map[string]any{"parse": []any{}}))
			content, isStr := m["content"].(string)
			Expect(isStr).To(BeTrue())
			return url, content, true
		}

		const hookURL = "CLAUDE_NOTIFICATION_WEBHOOK_URL=https://webhook.invalid/hook"

		payload := func(sessionID, msg, kind string) string {
			b, err := json.Marshal(map[string]string{
				"session_id":        sessionID,
				"transcript_path":   filepath.Join(root, "projects", "slug", sessionID+".jsonl"),
				"cwd":               "/ws/fromcwd",
				"hook_event_name":   "Notification",
				"message":           msg,
				"notification_type": kind,
			})
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		// record writes a peer-registry record the way Claude Code does:
		// <config dir>/sessions/<pid>.json.
		record := func(pid, sessionID, name string) {
			b, err := json.Marshal(map[string]any{
				"pid": 0, "sessionId": sessionID, "name": name, "nameSource": "user",
				"peerToken": "SECRET-TOKEN", "messagingSocketPath": "/secret/sock",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(cfg, "sessions", pid+".json"), b, 0o644)).To(Succeed())
		}

		const permMsg = "Claude needs your permission to use Bash"
		const idleMsg = "Claude is waiting for your input"

		It("CS-LNCH-111: names the session, the project, the kind and how to reach it", func() {
			record("4242", "SID", "claude-sandbox librarian")
			run(payload("SID", permMsg, "permission_prompt"),
				hookURL,
				"CLAUDE_PID=4242",
				"CLAUDE_SANDBOX_INSTANCE=otter",
				"CLAUDE_SANDBOX_CONTAINER=claude-sandbox-work-cs-a1b2c3-otter",
				"CLAUDE_SANDBOX_MODE=claude",
				"HOME=/home/rt",
				"CLAUDE_SANDBOX_PROJECT_DIR=/home/rt/work/src/claude-sandbox")

			url, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(url).To(Equal("https://webhook.invalid/hook"))
			Expect(content).To(Equal(
				"🔔 permission prompt · **`claude-sandbox librarian`** (`otter`) · project `claude-sandbox`\n" +
					"> `Claude needs your permission to use Bash`\n" +
					"Attach: `cd ~/work/src/claude-sandbox && claude-sandbox --attach=otter` · container `claude-sandbox-work-cs-a1b2c3-otter`"))
			// The project path only with $HOME shortened; never the URL, and
			// nothing else from the registry record.
			Expect(content).NotTo(ContainSubstring("/home/rt"))
			Expect(content).NotTo(ContainSubstring("webhook.invalid"))
			Expect(content).NotTo(ContainSubstring("SECRET"))
			Expect(content).NotTo(ContainSubstring("/secret"))
		})

		It("CS-LNCH-111: an idle ping quotes no message, and the project falls back to cwd", func() {
			record("17", "RID", "claude-sandbox-61")
			run(payload("RID", idleMsg, "idle_prompt"),
				hookURL, "CLAUDE_PID=17", "CLAUDE_SANDBOX_INSTANCE=heron")

			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(Equal("🔔 idle · **`claude-sandbox-61`** (`heron`) · project `fromcwd`\n" +
				"Attach: `claude-sandbox --attach=heron`"))
		})

		It("CS-LNCH-111: the session id may come from CLAUDE_CODE_SESSION_ID", func() {
			record("17", "ENVSID", "from-env")
			run(`{"notification_type":"idle_prompt"}`, hookURL,
				"CLAUDE_PID=17", "CLAUDE_CODE_SESSION_ID=ENVSID")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(Equal("🔔 idle · **`from-env`**"))
		})

		It("CS-LNCH-111: reads only this session's record, and only when its sessionId matches", func() {
			// A record at this pid left by another session names nothing.
			record("17", "SOMEONE-ELSE", "not-me")
			// Another session's record, at a different pid, is never read —
			// even though its sessionId would match.
			record("99", "SID", "wrong-file")
			run(payload("SID", idleMsg, "idle_prompt"), hookURL,
				"CLAUDE_PID=17", "CLAUDE_SANDBOX_INSTANCE=otter")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).NotTo(ContainSubstring("not-me"))
			Expect(content).NotTo(ContainSubstring("wrong-file"))
			Expect(content).To(HavePrefix("🔔 idle · **`otter`**"))
		})

		It("CS-LNCH-111: a CLAUDE_PID that is not a number cannot name another file", func() {
			Expect(os.WriteFile(filepath.Join(cfg, "evil.json"),
				[]byte(`{"sessionId":"SID","name":"PWNED"}`), 0o644)).To(Succeed())
			run(payload("SID", idleMsg, "idle_prompt"), hookURL,
				"CLAUDE_PID=../evil", "CLAUDE_SANDBOX_INSTANCE=otter")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).NotTo(ContainSubstring("PWNED"))
		})

		It("CS-LNCH-111, CS-SESS-075: a joined session is not offered an attach", func() {
			run(payload("SID", idleMsg, "idle_prompt"), hookURL,
				"CLAUDE_SANDBOX_INSTANCE=otter", "CLAUDE_SANDBOX_CONTAINER=cs-otter",
				"CLAUDE_SANDBOX_MODE=claude", "CLAUDE_SANDBOX_JOINED=1")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(HaveSuffix("\nJoined session (not attachable) · container `cs-otter`"))
			Expect(content).NotTo(ContainSubstring("--attach"))
		})

		It("CS-LNCH-111: a headless session is not offered an attach", func() {
			run(payload("SID", permMsg, "permission_prompt"), hookURL,
				"CLAUDE_SANDBOX_INSTANCE=otter", "CLAUDE_SANDBOX_CONTAINER=cs-otter",
				"CLAUDE_SANDBOX_MODE=headless")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(HaveSuffix("\nHeadless session (SDK client) · container `cs-otter`"))
			Expect(content).NotTo(ContainSubstring("--attach"))
		})

		It("CS-LNCH-111: an unset webhook URL posts nothing at all", func() {
			run(payload("SID", idleMsg, "idle_prompt"), "CLAUDE_SANDBOX_INSTANCE=otter")
			_, _, ok := posted()
			Expect(ok).To(BeFalse())
		})

		It("CS-LNCH-111: keeps the identity it has when the payload is unusable", func() {
			// jq cannot parse it; the env still names the session.
			run("not json at all", hookURL,
				"CLAUDE_SANDBOX_CONTAINER=claude-sandbox-w-p-a1b2c3-ralph",
				"CLAUDE_SANDBOX_MODE=ralph",
				"CLAUDE_SANDBOX_PROJECT_DIR=/w/myproj")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(Equal("🔔 needs your input · project `myproj`\n" +
				"Container: `claude-sandbox-w-p-a1b2c3-ralph`"))
		})

		It("CS-LNCH-111: degrades to the line it posted before when nothing identifies the session", func() {
			run("", hookURL)
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(Equal("🔔 Claude Code needs your input"))
		})

		It("CS-LNCH-111: degrades to that line when jq is missing, and still exits 0", func() {
			Expect(os.Remove(filepath.Join(bindir, "jq"))).To(Succeed())
			record("17", "SID", "named")
			run(payload("SID", idleMsg, "idle_prompt"),
				hookURL, "CLAUDE_PID=17", "CLAUDE_SANDBOX_INSTANCE=otter")
			raw, err := os.ReadFile(sink)
			Expect(err).NotTo(HaveOccurred())
			_, body, _ := strings.Cut(string(raw), "\n")
			Expect(body).To(MatchJSON(`{"content":"🔔 Claude Code needs your input","allowed_mentions":{"parse":[]},"flags":4}`))
		})

		It("CS-LNCH-111: exits 0 when curl itself is missing", func() {
			Expect(os.Remove(filepath.Join(bindir, "curl"))).To(Succeed())
			run(payload("SID", idleMsg, "idle_prompt"), hookURL, "CLAUDE_SANDBOX_INSTANCE=otter")
			_, _, ok := posted()
			Expect(ok).To(BeFalse())
		})

		It("CS-LNCH-111: JSON-escapes quotes, and strips backticks and control characters", func() {
			record("17", "SID", "say \"hi\" `rm` \\ back\nslash")
			long := "Claude needs your permission to use " + strings.Repeat("A", 400)
			run(payload("SID", long, "permission_prompt"), hookURL,
				"CLAUDE_PID=17", "CLAUDE_SANDBOX_INSTANCE=ot`ter")

			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(ContainSubstring("**`say \"hi\"  rm  \\ back slash`** (`ot ter`)"))
			// The message is cut at 160 codepoints.
			Expect(content).To(ContainSubstring("\n> `Claude needs your permission to use AAAA"))
			Expect(content).NotTo(ContainSubstring(strings.Repeat("A", 160-len("Claude needs your permission to use ")+1)))
			// No injected line breaks: header, quote, reach.
			Expect(strings.Split(content, "\n")).To(HaveLen(3))
		})

		It("CS-LNCH-111: a name cannot become live markdown: it sits in a code span", func() {
			record("17", "SID", "[x](http://y) _a_ @everyone")
			run(payload("SID", "Claude needs your permission to use mcp__gh__create_issue", "permission_prompt"),
				hookURL, "CLAUDE_PID=17", "CLAUDE_SANDBOX_INSTANCE=otter")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(HavePrefix("🔔 permission prompt · **`[x](http://y) _a_ @everyone`** (`otter`)"))
			// The tool name in the quoted message is in a code span too.
			Expect(content).To(ContainSubstring("\n> `Claude needs your permission to use mcp__gh__create_issue`\n"))
		})

		It("CS-LNCH-111: the attach command shell-quotes a project path that needs it", func() {
			run(payload("SID", idleMsg, "idle_prompt"), hookURL,
				"HOME=/home/u", "CLAUDE_SANDBOX_INSTANCE=otter",
				"CLAUDE_SANDBOX_PROJECT_DIR=/home/u/my proj's")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(HaveSuffix("\nAttach: `cd ~/'my proj'\\''s' && claude-sandbox --attach=otter`"))
		})

		It("CS-LNCH-111: a project outside $HOME keeps its full path; a mangled one is omitted", func() {
			run(payload("SID", idleMsg, "idle_prompt"), hookURL,
				"HOME=/home/u", "CLAUDE_SANDBOX_INSTANCE=otter", "CLAUDE_SANDBOX_PROJECT_DIR=/srv/p")
			_, content, _ := posted()
			Expect(content).To(HaveSuffix("\nAttach: `cd /srv/p && claude-sandbox --attach=otter`"))

			run(payload("SID", idleMsg, "idle_prompt"), hookURL,
				"HOME=/home/u", "CLAUDE_SANDBOX_INSTANCE=otter", "CLAUDE_SANDBOX_PROJECT_DIR=/srv/a`b")
			_, content, _ = posted()
			Expect(content).To(HaveSuffix("\nAttach: `claude-sandbox --attach=otter`"))
		})

		It("CS-LNCH-111: exits 0 with neither HOME nor CLAUDE_CONFIG_DIR set, and on a NUL byte", func() {
			bash, err := exec.LookPath("bash")
			Expect(err).NotTo(HaveOccurred())
			script, err := filepath.Abs(filepath.Join("..", "..", "bin", "notify-webhook"))
			Expect(err).NotTo(HaveOccurred())
			cmd := exec.Command(bash, script)
			cmd.Stdin = strings.NewReader("{\"session_id\":\"SID\"}\x00junk")
			cmd.Env = []string{"PATH=" + bindir, hookURL, "CLAUDE_PID=17", "CLAUDE_SANDBOX_INSTANCE=otter"}
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "%s", out)
			Expect(string(out)).To(BeEmpty())
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(ContainSubstring("--attach=otter"))
		})

		It("CS-LNCH-111: truncates by codepoint, never splitting a multibyte character", func() {
			record("17", "SID", strings.Repeat("é", 200))
			run(payload("SID", idleMsg, "idle_prompt"), hookURL, "CLAUDE_PID=17")
			_, content, ok := posted()
			Expect(ok).To(BeTrue())
			Expect(content).To(Equal("🔔 idle · **`" + strings.Repeat("é", 80) + "`** · project `fromcwd`"))
		})
	})

	It("CS-LNCH-111: the drop-in runs the script and still swallows its failure", func() {
		var m struct {
			Hooks struct {
				Notification []struct {
					Matcher string `json:"matcher"`
					Hooks   []struct {
						Type    string `json:"type"`
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"Notification"`
			} `json:"hooks"`
		}
		Expect(json.Unmarshal(repoFile("notification-hooks.json"), &m)).To(Succeed())
		Expect(m.Hooks.Notification).To(HaveLen(1))
		// The matcher is matched against notification_type (Claude Code 2.1.280),
		// not against message.
		Expect(m.Hooks.Notification[0].Matcher).To(Equal("idle_prompt|permission_prompt"))
		Expect(m.Hooks.Notification[0].Hooks).To(HaveLen(1))
		Expect(m.Hooks.Notification[0].Hooks[0].Type).To(Equal("command"))
		Expect(m.Hooks.Notification[0].Hooks[0].Command).To(
			Equal("/opt/claude-sandbox/bin/notify-webhook || true"))
	})
})

// jsonQuote JSON-quotes a string for embedding in a small fixture.
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}
