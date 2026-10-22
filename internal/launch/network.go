package launch

// The launcher network (CS-LNCH-185..191, CS-LNCH-193, CS-SESS-097).
//
// On Docker's default bridge a container gets a frozen copy of the host's flat
// upstream resolv.conf: a host whose resolver splits DNS per link (a VPN for
// some domains, the LAN for others) loses the split, and a VPN drop leaves the
// container pointed at an unreachable nameserver. On a user-defined network
// moby (29.x) reads the host's /etc/resolv.conf instead, marks its nameservers
// host-loopback and dials them from the daemon, in the host namespace, so the
// embedded resolver at 127.0.0.11 forwards every query to the host's resolver
// (daemon/container_operations_unix.go buildSandboxPlatformOptions;
// libnetwork/internal/resolvconf TransformForIntNS; libnetwork/resolver.go
// dialExtDNS). Two things would defeat that, so neither ever happens here: a
// --dns (an override, dialled from the container namespace) and an
// --internal network (which does not forward host-loopback upstreams).
//
// On by default, off by key or env (CS-LNCH-188), and it never fails a
// launch: every problem warns once and the container lands on the default
// bridge, exactly as with the key off (the CS-LNCH-107 precedent).

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// NetworkName is the launcher's network.
const NetworkName = "claude-sandbox"

// LabelNetwork marks the network as the launcher's (value "1"). The network
// carries it; containers do not.
const LabelNetwork = "claude-sandbox.network"

// NetworkEnv turns the network on or off for one launch (CS-LNCH-188).
const NetworkEnv = "CLAUDE_SANDBOX_NETWORK"

// KindNetwork is the drift-report kind of the <network> pseudo input
// (CS-LNCH-189).
const KindNetwork Kind = "network"

// NetworkInput is the <network> pseudo input's path in the drift report.
const NetworkInput = "<network>"

// NetworkBanner is the one line a launch on the network prints (CS-LNCH-193):
// it is on by default (CS-LNCH-188), not from anything the person typed, and
// it changes what a session can reach.
const NetworkBanner = "Network: claude-sandbox - DNS goes through the host's resolver; " +
	"containers on Docker's default bridge are not reachable by IP (start siblings with --network claude-sandbox)."

// NetworkContext is the paragraph appended to the shadow CLAUDE.md only when
// the network applies (CS-LNCH-191), so a session reads it only when it is
// true, and container-context.md stays silent about a network a session with
// the key off (or a stood-down launch) is not on.
const NetworkContext = "- **Sibling containers and DNS.** This sandbox runs on the `claude-sandbox` Docker network,\n" +
	"  so its DNS goes through the host's resolver (`nameserver 127.0.0.11`), and names that\n" +
	"  resolve only on certain networks (a VPN, the LAN) work as they do on the host. Containers\n" +
	"  you start through the Docker socket with a plain `docker run` land on Docker's default\n" +
	"  bridge, which this network cannot reach by IP. Start them with\n" +
	"  `--network claude-sandbox`; then they are reachable by IP and by container name, unless\n" +
	"  the host disables inter-container traffic. Their published ports (`-p`) work either way,\n" +
	"  through this container's gateway.\n"

// NetworkWanted resolves the key (CS-LNCH-188): CLAUDE_SANDBOX_NETWORK
// (1/true/yes on, 0/false/no off) > sandboxNetwork > on. Default ON for host
// parity (operator answer 102 a): the host's per-link split DNS reaches the
// container; sandboxNetwork: false or CLAUDE_SANDBOX_NETWORK=0 turns it off.
func NetworkWanted(getenv func(string) string, cfg *cascade.Config) bool {
	var key *bool
	if cfg != nil {
		key = cfg.SandboxNetwork
	}
	return ResolveTristate(nil, getenv(NetworkEnv), key, true)
}

// NetworkState is what an inspect of the launcher network found.
type NetworkState int

const (
	// NetworkOurs: driver bridge, not internal, labelled claude-sandbox.network=1.
	NetworkOurs NetworkState = iota
	// NetworkMissing: no network of that name.
	NetworkMissing
	// NetworkForeign: a network of that name the launcher did not make.
	NetworkForeign
	// NetworkError: docker failed otherwise, or printed something unparsable.
	NetworkError
)

// NetworkInspect is InspectNetwork's answer; Reason says why a network is
// foreign, or what failed.
type NetworkInspect struct {
	State  NetworkState
	Reason string
}

const networkSep = "\x1f"

// networkInspectArgs is CS-LNCH-185's first step.
func networkInspectArgs() []string {
	return []string{"network", "inspect", "--format",
		"{{.Driver}}" + networkSep + "{{.Internal}}" + networkSep + `{{index .Labels "` + LabelNetwork + `"}}`,
		NetworkName}
}

// InspectNetwork runs the one inspect that decides whether the network is the
// launcher's (CS-LNCH-185), shared by the ensure step and the drift check
// (CS-LNCH-189), which passes a bounded runner on the restore path.
func InspectNetwork(r execx.Runner) NetworkInspect {
	var stdout, stderr strings.Builder
	err := r.Run(execx.Cmd{Name: "docker", Args: networkInspectArgs(), Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		if isNotFound(stderr.String()) {
			return NetworkInspect{State: NetworkMissing}
		}
		return NetworkInspect{State: NetworkError, Reason: dockerReason(stderr.String(), err)}
	}
	out := strings.TrimRight(stdout.String(), "\r\n")
	f := strings.Split(out, networkSep)
	if len(f) != 3 {
		return NetworkInspect{State: NetworkError, Reason: fmt.Sprintf("unexpected output %q", firstLine(out))}
	}
	driver, internal, label := strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
	if label == "<no value>" {
		label = ""
	}
	isInternal, perr := strconv.ParseBool(internal)
	if perr != nil {
		return NetworkInspect{State: NetworkError, Reason: fmt.Sprintf("unexpected output %q", firstLine(out))}
	}
	switch {
	case driver != "bridge":
		return NetworkInspect{State: NetworkForeign, Reason: "driver " + driver}
	case isInternal:
		return NetworkInspect{State: NetworkForeign, Reason: "internal"}
	case label != "1":
		return NetworkInspect{State: NetworkForeign, Reason: "no " + LabelNetwork + "=1 label"}
	}
	return NetworkInspect{State: NetworkOurs}
}

// EnsureNetwork makes sure the launcher network exists and is the launcher's
// (CS-LNCH-185..187), and returns its name, or "" when this launch stands
// down to the default bridge — after one warning on warn.
func EnsureNetwork(r execx.Runner, warn io.Writer) string {
	switch got := InspectNetwork(r); got.State {
	case NetworkOurs:
		return NetworkName
	case NetworkForeign:
		warnForeignNetwork(warn, got.Reason)
		return ""
	case NetworkError:
		warnNetwork(warn, "could not inspect the claude-sandbox network", got.Reason)
		return ""
	}
	opts, err := bridgeOptions(r)
	if err != nil {
		warnNetwork(warn, "could not read the default bridge's options", err.Error())
		return ""
	}
	args := []string{"network", "create", "--driver", "bridge", "--label", LabelNetwork + "=1"}
	args = append(args, opts...)
	args = append(args, NetworkName)
	var stdout, stderr strings.Builder
	if cerr := r.Run(execx.Cmd{Name: "docker", Args: args, Stdout: &stdout, Stderr: &stderr}); cerr != nil {
		if !strings.Contains(stderr.String(), "already exists") {
			warnNetwork(warn, "could not create the claude-sandbox network", dockerReason(stderr.String(), cerr))
			return ""
		}
		// A concurrent launcher made it (a nested one takes another lock
		// file, a lockless one none): judge what is there, never create again.
		switch again := InspectNetwork(r); again.State {
		case NetworkOurs:
			return NetworkName
		case NetworkForeign:
			warnForeignNetwork(warn, again.Reason)
			return ""
		case NetworkMissing:
			warnNetwork(warn, "could not create the claude-sandbox network", "it was reported to exist, then not found")
			return ""
		default:
			warnNetwork(warn, "could not inspect the claude-sandbox network", again.Reason)
			return ""
		}
	}
	return NetworkName
}

// bridgeOption is one of docker0's options the create copies (CS-LNCH-185),
// with its test for "differs from Docker's built-in default". copy reports
// whether to pass the value; an error is a value that does not parse.
type bridgeOption struct {
	key  string
	copy func(v string) (bool, error)
}

func notTrue(v string) (bool, error) {
	b, err := strconv.ParseBool(v)
	return err == nil && !b, err
}

// bridgeOptionKeys are the four keys, in a fixed order. Only these: the daemon's
// icc, mtu, ip-masq and ip settings configure docker0 alone, so a fresh network
// would otherwise silently take Docker's defaults; and only where they differ
// from those defaults, since an explicit -o would override the daemon's
// default-network-opts.
var bridgeOptionKeys = []bridgeOption{
	{"com.docker.network.bridge.enable_icc", notTrue},
	{"com.docker.network.driver.mtu", func(v string) (bool, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return false, fmt.Errorf("not an MTU")
		}
		return n != 0 && n != 1500, nil
	}},
	{"com.docker.network.bridge.enable_ip_masquerade", notTrue},
	{"com.docker.network.bridge.host_binding_ipv4", func(v string) (bool, error) {
		ip := net.ParseIP(v)
		if ip == nil {
			return false, fmt.Errorf("not an IP address")
		}
		return !ip.IsUnspecified(), nil
	}},
}

// bridgeOptions reads docker0's options and returns the -o flags to copy.
// docker0 missing (a daemon run with --bridge=none) means nothing to match;
// any other failure is an error, and nothing is created with guessed options.
func bridgeOptions(r execx.Runner) ([]string, error) {
	var stdout, stderr strings.Builder
	err := r.Run(execx.Cmd{Name: "docker", Args: []string{"network", "inspect", "--format", "{{json .Options}}", "bridge"},
		Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		if isNotFound(stderr.String()) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s", dockerReason(stderr.String(), err))
	}
	raw := strings.TrimSpace(stdout.String())
	var opts map[string]string
	if raw != "null" {
		if jerr := json.Unmarshal([]byte(raw), &opts); jerr != nil {
			return nil, fmt.Errorf("unexpected output %q", firstLine(raw))
		}
	}
	var out []string
	for _, o := range bridgeOptionKeys {
		v := strings.TrimSpace(opts[o.key])
		if v == "" {
			continue
		}
		ok, perr := o.copy(v)
		if perr != nil {
			return nil, fmt.Errorf("%s=%q does not parse", o.key, v)
		}
		if ok {
			out = append(out, "-o", o.key+"="+v)
		}
	}
	return out, nil
}

// NetworkMissing reports a docker create refused because the network is
// gone (CS-LNCH-190): pruned between the ensure step and the create.
func (e *CreateError) NetworkMissing() bool {
	return strings.Contains(e.Stderr, "network "+NetworkName+" not found") ||
		strings.Contains(e.Stderr, "No such network: "+NetworkName)
}

func isNotFound(stderr string) bool {
	return strings.Contains(stderr, "not found") || strings.Contains(stderr, "No such network")
}

func warnForeignNetwork(w io.Writer, reason string) {
	fmt.Fprintf(w, "WARNING: a Docker network named %s exists but was not created by claude-sandbox (%s); launching on the default bridge. "+
		"Remove it (docker network rm %s) or recreate it with --label %s=1.\n", NetworkName, reason, NetworkName, LabelNetwork)
}

func warnNetwork(w io.Writer, what, reason string) {
	fmt.Fprintf(w, "WARNING: %s (%s); launching on the default bridge.\n", what, reason)
}

// dockerReason is docker's last stderr line, else the error.
func dockerReason(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	if err != nil {
		return err.Error()
	}
	return "no output"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
