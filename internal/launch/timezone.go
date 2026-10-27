package launch

// The container's time zone (CS-LNCH-213..217, CS-CASC-050; plan
// host-timezone 00..02).
//
// The base image is UTC (/etc/localtime -> Etc/UTC) while host claude reads
// the host's zone, so a sandbox showed tomorrow's date to a US evening
// session, +0000 commits and UTC logs. The launcher passes the host's zone as
// one "-e TZ=<value>": TZ wins over /etc/localtime for glibc, Go, Python, git
// and Node/Bun's ICU (Claude Code) alike. A bind of the host's /etc/localtime
// is never added (CS-LNCH-213): runc resolves the destination link inside the
// rootfs, so the host file lands on /usr/share/zoneinfo/Etc/UTC, corrupting
// UTC for every program, and ICU still reads the link name and shows UTC.
//
// The work is split in three so nothing is read or printed twice:
//   - TimezoneChoice validates the key and CLAUDE_SANDBOX_TIMEZONE in
//     launchWith before any image work (exit 2 on a bad value, CS-LNCH-215);
//   - ResolveTimezone runs once in launchWith right after the run-image
//     inspect (imagebuild.ImageIdentity), reads the host's zone through
//     TimezoneSources and returns the value, the label and at most one Note;
//   - Build only renders Inputs.Timezone and Inputs.TimezoneLabel
//     (CS-LNCH-217): attach/join drift checks and re-picks re-run Build and
//     must read no host file.
//
// Neither the value nor the key is hashed: the value is derived from the
// launcher's environment (whoever launches, from which shell), the terminal
// identity's reason (CS-LNCH-181).

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// TimezoneEnv overrides the timezone key for one launch (CS-LNCH-216).
const TimezoneEnv = "CLAUDE_SANDBOX_TIMEZONE"

// TimezoneVar is the variable the container gets.
const TimezoneVar = "TZ"

// LabelTimezone records what the container got (CS-LNCH-217): the value
// passed, or one of the TimezoneLabel* words. Never hashed.
const LabelTimezone = "claude-sandbox.timezone"

// Label values when no "-e TZ" was passed.
const (
	TimezoneLabelNone    = "none"     // off by the key or env
	TimezoneLabelEnvFile = "env-file" // an env file defines TZ
	TimezoneLabelImage   = "image"    // the run image's Config.Env defines TZ (host default)
	TimezoneLabelUnknown = "unknown"  // the host's zone could not be told
)

// HostLocaltime is the host file the link walk starts from when TZ is unset.
const HostLocaltime = "/etc/localtime"

// timezoneMax caps a value in bytes (CS-LNCH-215).
const timezoneMax = 128

// timezoneHops caps the link walk's Readlink calls (CS-LNCH-214).
const timezoneHops = 4

// TimezoneMode is the kind of choice the key or env made.
type TimezoneMode int

const (
	// TimezoneHost: the default, the host's zone.
	TimezoneHost TimezoneMode = iota
	// TimezoneOff: pass nothing; the image's own zone applies.
	TimezoneOff
	// TimezoneExplicit: Value is the TZ to pass.
	TimezoneExplicit
)

// TimezoneSetting is TimezoneChoice's answer: the mode, the explicit value
// and its source (the variable, or the key's config file) for the Notes.
type TimezoneSetting struct {
	Mode   TimezoneMode
	Value  string
	Source string
}

// TimezoneChoice parses CLAUDE_SANDBOX_TIMEZONE > the timezone key > host
// (CS-LNCH-216). keySource is the config file that set the key
// (cascade.KeySourceOf), named in errors and Notes. A bad shape or a path
// value is an error naming its source (CS-LNCH-215); it reads no host file.
func TimezoneChoice(getenv func(string) string, cfg *cascade.Config, keySource string) (TimezoneSetting, error) {
	if raw := strings.TrimSpace(getenv(TimezoneEnv)); raw != "" {
		return parseTimezoneChoice(raw, TimezoneEnv, func(why string) error {
			return fmt.Errorf("%s=%q: %s", TimezoneEnv, raw, why)
		})
	}
	if cfg != nil && cfg.Timezone != nil {
		raw := strings.TrimSpace(*cfg.Timezone)
		if raw != "" {
			src := keySource
			if src == "" {
				src = "the config cascade"
			}
			return parseTimezoneChoice(raw, src, func(why string) error {
				return fmt.Errorf("timezone: %q in %s: %s", raw, src, why)
			})
		}
	}
	return TimezoneSetting{Mode: TimezoneHost}, nil
}

func parseTimezoneChoice(raw, source string, fail func(string) error) (TimezoneSetting, error) {
	switch strings.ToLower(raw) {
	case "none", "off", "false", "0", "no":
		return TimezoneSetting{Mode: TimezoneOff, Source: source}, nil
	case "host":
		return TimezoneSetting{Mode: TimezoneHost, Source: source}, nil
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ":/") {
		return TimezoneSetting{}, fail("a path names a file on a side you cannot be sure of; use a zone name such as Europe/Berlin")
	}
	v := strings.TrimPrefix(raw, ":")
	if why := timezoneShape(v); why != "" {
		return TimezoneSetting{}, fail(why + "; use a zone name such as Europe/Berlin, UTC or a POSIX TZ string")
	}
	return TimezoneSetting{Mode: TimezoneExplicit, Value: v, Source: source}, nil
}

// timezoneShape is CS-LNCH-215's check: "" when v may ride argv and a label.
func timezoneShape(v string) string {
	switch {
	case v == "":
		return "empty"
	case len(v) > timezoneMax:
		return fmt.Sprintf("longer than %d bytes", timezoneMax)
	case strings.HasPrefix(v, "-"):
		return `starts with "-"`
	}
	for _, r := range v {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("_+-/:.,<>", r)
		if !ok {
			return "holds a character outside [A-Za-z0-9_+-/:.,<>]"
		}
	}
	for _, c := range strings.Split(v, "/") {
		if c == ".." {
			return `holds a ".." component`
		}
	}
	return ""
}

// TimezoneSources are the host reads ResolveTimezone makes, every one a seam:
// the launcher's environment (TZ, and the bare env-file lines), Readlink for
// the link walk, and /etc/timezone's contents.
type TimezoneSources struct {
	LookupEnv       func(string) (string, bool)
	Readlink        func(string) (string, error)
	ReadEtcTimezone func() ([]byte, error)
}

// TimezoneResult is what the container gets: Value for "-e TZ=" ("" = none),
// Label for LabelTimezone, and Note, the one line to print ("" = silent).
type TimezoneResult struct {
	Value string
	Label string
	Note  string
}

// ResolveTimezone decides the container's TZ (CS-LNCH-216): an env file
// defining TZ > an explicit value > the run image's ENV TZ (imageTZ) > the
// host's zone. It reads host files only for the host default with no env
// file and no image TZ.
func ResolveTimezone(choice TimezoneSetting, envSnap []cascade.EnvFile, imageTZ bool, src TimezoneSources) TimezoneResult {
	lookup := cascade.LookupEnv(src.LookupEnv)
	if cascade.EnvFilesDefine(envSnap, TimezoneVar, lookup) {
		res := TimezoneResult{Label: TimezoneLabelEnvFile}
		if choice.Mode == TimezoneExplicit {
			_, _, _, file := cascade.EnvFilesValue(envSnap, TimezoneVar, lookup)
			if file == "" {
				file = "an env file"
			}
			res.Note = fmt.Sprintf("Note: timezone: %s (%s) is overridden by TZ in %s; the sandbox uses that.", choice.Value, choice.Source, file)
		}
		return res
	}
	switch choice.Mode {
	case TimezoneOff:
		return TimezoneResult{Label: TimezoneLabelNone}
	case TimezoneExplicit:
		res := TimezoneResult{Value: choice.Value, Label: choice.Value}
		if imageTZ {
			res.Note = fmt.Sprintf("Note: timezone: %s (%s) overrides TZ set by the image.", choice.Value, choice.Source)
		}
		return res
	}
	if imageTZ {
		return TimezoneResult{Label: TimezoneLabelImage}
	}
	v, why := HostTimezone(src)
	if v == "" {
		return TimezoneResult{Label: TimezoneLabelUnknown, Note: "Note: could not tell the host's time zone (" +
			why + "); the sandbox runs in UTC. Set timezone: <Area/City> in .claude-sandbox/config.yaml."}
	}
	return TimezoneResult{Value: v, Label: v}
}

// HostTimezone resolves the host's zone (CS-LNCH-214): the launcher's TZ, then
// the link walk on /etc/localtime, then /etc/timezone. It returns the value,
// or "" and why it could not tell.
func HostTimezone(src TimezoneSources) (string, string) {
	tz := ""
	if src.LookupEnv != nil {
		tz, _ = src.LookupEnv(TimezoneVar)
	}
	if tz != "" {
		p := strings.TrimPrefix(tz, ":")
		if strings.HasPrefix(p, "/") {
			if name, ok := zoneFromPath(p, src.Readlink); ok {
				return checkedHostZone(name, "TZ="+tz)
			}
			if filepath.Clean(p) != HostLocaltime {
				return "", "TZ=" + registry.Printable(tz) + " names no zone file"
			}
			return etcTimezone(src, "TZ="+registry.Printable(tz)+" names no zone file")
		}
		return checkedHostZone(p, "TZ="+tz)
	}
	if name, ok := zoneFromPath(HostLocaltime, src.Readlink); ok {
		return checkedHostZone(name, HostLocaltime+" -> "+name)
	}
	return etcTimezone(src, "TZ is unset, "+HostLocaltime+" names no zone")
}

func checkedHostZone(v, what string) (string, string) {
	if why := timezoneShape(v); why != "" {
		return "", registry.Printable(what) + " " + why
	}
	return v, ""
}

// etcTimezone is step 3: the first line of /etc/timezone.
func etcTimezone(src TimezoneSources, before string) (string, string) {
	if src.ReadEtcTimezone == nil {
		return "", before + ", no /etc/timezone"
	}
	raw, err := src.ReadEtcTimezone()
	if err != nil {
		return "", before + ", no readable /etc/timezone"
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", before + ", /etc/timezone is empty"
	}
	return checkedHostZone(line, "/etc/timezone's "+line)
}

// zoneName takes the zone from a text naming a zoneinfo file: the part after
// the last /zoneinfo/, a leading posix/ or right/ stripped.
func zoneName(text string) (string, bool) {
	const marker = "/zoneinfo/"
	i := strings.LastIndex(text, marker)
	if i < 0 {
		return "", false
	}
	n := text[i+len(marker):]
	for _, pre := range []string{"posix/", "right/"} {
		n = strings.TrimPrefix(n, pre)
	}
	return n, n != ""
}

// zoneFromPath is the link walk (CS-LNCH-214): the path's own text first (a
// TZ naming a zone file reads nothing), then at most timezoneHops Readlinks,
// each relative text resolved against its link's directory.
func zoneFromPath(p string, readlink func(string) (string, error)) (string, bool) {
	if n, ok := zoneName(p); ok {
		return n, true
	}
	if readlink == nil {
		return "", false
	}
	cur := p
	for hop := 0; hop < timezoneHops; hop++ {
		t, err := readlink(cur)
		if err != nil {
			return "", false
		}
		if n, ok := zoneName(t); ok {
			return n, true
		}
		if !filepath.IsAbs(t) {
			// A relative text like "zoneinfo/Europe/Oslo" holds no
			// "/zoneinfo/" until it is joined; the joined path is a
			// regular file, which a Readlink would only fail on.
			t = filepath.Join(filepath.Dir(cur), t)
			if n, ok := zoneName(t); ok {
				return n, true
			}
		}
		cur = t
	}
	return "", false
}
