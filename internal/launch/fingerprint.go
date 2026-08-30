package launch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config fingerprinting: identify the *effective* configuration a container was
// launched with, so that attaching to or joining an existing container can tell
// the user when it was built from something other than what is on disk now.
// Spec: spec/sessions.feature (CS-SESS-020..024).
//
// Two things this deliberately does NOT do:
//
//   - It does not hash the docker argv. Four shadow files (CLAUDE.md,
//     settings.json, .mcp.json, gitconfig) are bind-mounted from a temp
//     directory that is recreated every launch, so an argv hash would report
//     drift every single time.
//   - It does not hash the config files as found on disk. Hashing the *merged*
//     result is strictly better: a newly added upstream config.yaml changes the
//     merge and so registers as drift, while an upstream edit that a more-local
//     file fully overrides does not — which is correct, because the effective
//     launch really is identical (CS-SESS-024).

// Kind classifies a fingerprint input for the human-readable drift report.
type Kind string

const (
	KindConfig Kind = "config"
	KindEnv    Kind = "env"
	KindImage  Kind = "image"
	KindShadow Kind = "shadow"
)

// InputDigest is one contributing file and a digest of what it contributed.
type InputDigest struct {
	Path   string `json:"p"`
	Digest string `json:"d"`
	Kind   Kind   `json:"k"`
}

type hostAccess struct {
	SSH, Git, DockerSocket, AWS, PackageCaches bool
}

func shortDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}

// configFingerprint hashes the effective launch and returns the hash plus the
// per-file digests used to explain a mismatch.
func (in *Inputs) configFingerprint(p *Plan, ha hostAccess) (string, []InputDigest) {
	inputs := make([]InputDigest, 0, len(in.EnvFiles)+len(in.shadowDigests)+2)

	// (1) The merged cascade config. Canonical JSON of the post-merge struct:
	// this is the value that actually governs the launch, whatever combination
	// of files across the tree produced it.
	merged, _ := json.Marshal(in.Cfg)
	inputs = append(inputs, InputDigest{Path: "<merged config>", Digest: shortDigest(merged), Kind: KindConfig})

	// (2) Env files, in cascade order, by content — docker reads --env-file at
	// run time, so the contents are what matter, not just the paths.
	for _, ef := range in.EnvFiles {
		raw, err := os.ReadFile(ef)
		if err != nil {
			raw = []byte("<unreadable>")
		}
		inputs = append(inputs, InputDigest{Path: ef, Digest: shortDigest(raw), Kind: KindEnv})
	}

	// (3)+(4) The image: which Dockerfile and context produced it, and the ID
	// of the image actually used, so an out-of-band rebuild counts as drift.
	inputs = append(inputs, InputDigest{
		Path:   in.ImageName,
		Digest: shortDigest([]byte(in.ImageID)),
		Kind:   KindImage,
	})

	// (5) The generated shadow files, by content (collected in tempFile).
	inputs = append(inputs, in.shadowDigests...)

	// (6)(7)(8) Everything else that shapes the container but is not a file.
	// Mounts are normalized to the container path plus its ro/rw flag: host
	// paths of shadow mounts are per-launch temp paths and are already covered
	// by their content digests above.
	var env strings.Builder
	for _, v := range normalizedMounts(p.Volumes) {
		fmt.Fprintf(&env, "mount=%s\n", v)
	}
	fmt.Fprintf(&env, "ssh=%t git=%t docker=%t aws=%t packageCaches=%t\n", ha.SSH, ha.Git, ha.DockerSocket, ha.AWS, ha.PackageCaches)
	fmt.Fprintf(&env, "uid=%d gid=%d user=%s home=%s\n", in.HostUID, in.HostGID, in.HostUser, in.Home)
	fmt.Fprintf(&env, "memory=%s\n", p.MemoryLimit)

	// Excluded on purpose: model, passthrough args, --limit, the instance noun
	// and the container name. Those are per-session choices, not the
	// environment, and would make every new session look like drift. The model
	// is reported separately, since attaching cannot change it (CS-SESS-027).
	h := sha256.New()
	for _, d := range inputs {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", d.Kind, d.Path, d.Digest)
	}
	h.Write([]byte(env.String()))
	return hex.EncodeToString(h.Sum(nil))[:12], inputs
}

// normalizedMounts reduces -v specs to "containerPath:mode", sorted, dropping
// the host side so per-launch temp paths do not leak into the hash.
func normalizedMounts(volumes []string) []string {
	out := make([]string, 0, len(volumes))
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		switch len(parts) {
		case 2:
			out = append(out, parts[1]+":rw")
		case 3:
			out = append(out, parts[1]+":"+parts[2])
		default:
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func encodeInputs(inputs []InputDigest) string {
	b, err := json.Marshal(inputs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// DecodeInputs parses a claude-sandbox.inputs label. A malformed or absent
// label yields no inputs, which the drift report renders as "unknown" rather
// than as a spurious list of changes.
func DecodeInputs(s string) []InputDigest {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []InputDigest
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// DriftChange is one difference between a container's recorded inputs and the
// inputs of the launch we would perform now.
type DriftChange struct {
	Path string
	How  string // "changed", "added", "removed"
	Kind Kind
}

// Drift compares the inputs recorded on a container against current inputs.
// Returns nil when they agree.
func Drift(was, now []InputDigest) []DriftChange {
	oldByPath := map[string]InputDigest{}
	for _, d := range was {
		oldByPath[d.Path] = d
	}
	newByPath := map[string]InputDigest{}
	for _, d := range now {
		newByPath[d.Path] = d
	}
	var out []DriftChange
	for _, d := range now {
		prev, ok := oldByPath[d.Path]
		switch {
		case !ok:
			out = append(out, DriftChange{Path: d.Path, How: "added", Kind: d.Kind})
		case prev.Digest != d.Digest:
			out = append(out, DriftChange{Path: d.Path, How: "changed", Kind: d.Kind})
		}
	}
	for _, d := range was {
		if _, ok := newByPath[d.Path]; !ok {
			out = append(out, DriftChange{Path: d.Path, How: "removed", Kind: d.Kind})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].How != out[j].How {
			return out[i].How < out[j].How
		}
		return out[i].Path < out[j].Path
	})
	return out
}
