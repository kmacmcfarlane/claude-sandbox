package launch

import (
	"fmt"
	"strings"
	"unicode"
)

// Label values (CS-LNCH-224..226). Discovery reads every label from one
// docker ps row whose fields are joined by the unit separator, so a label
// value holding it — or any other control character — could shift the
// fields after it and make discovery read values a session chose
// (CS-SESS-118). No label the launcher writes may hold a control character
// (Cc: the separator, newline, CR and tab among them) or a Bidi_Control
// character, which would make a printed value read differently from what it
// is.

// isBidiControl is Unicode's Bidi_Control property (Go's unicode package has
// no table for it): ALM; LRM, RLM; LRE, RLE, PDF, LRO, RLO; LRI, RLI, FSI, PDI.
func isBidiControl(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// LabelValueProblem says why v cannot be a label value, "" when it can.
func LabelValueProblem(v string) string {
	for _, r := range v {
		switch {
		case unicode.IsControl(r):
			return fmt.Sprintf("a control character (U+%04X)", r)
		case isBidiControl(r):
			return fmt.Sprintf("a bidi control character (U+%04X)", r)
		}
	}
	return ""
}

// CheckLabels is Build's backstop (CS-LNCH-226): every "key=value" label
// spec must have a value LabelValueProblem accepts. The values that come
// from configuration are checked earlier, before any image work, with a
// message naming their source (CheckConfiguredLabels); this catches what the
// launcher derives (paths, the version) and anything added later.
func CheckLabels(labels []string) error {
	for _, l := range labels {
		key, val, _ := strings.Cut(l, "=")
		if why := LabelValueProblem(val); why != "" {
			return fmt.Errorf("refusing to create the container: the value of its label %s holds %s, which would corrupt session discovery", key, why)
		}
	}
	return nil
}

// CheckMounts is the mount half of Build's backstop (CS-LNCH-226): no -v
// spec (source and destination) and no --mount source may hold a control or
// bidi character. Discovery JSON-decodes {{.Mounts}}, so this is defence in
// depth, and it keeps such a path out of every listing that prints mounts.
func CheckMounts(volumes []string, extra ...string) error {
	for _, v := range append(append([]string{}, volumes...), extra...) {
		if why := LabelValueProblem(v); why != "" {
			return fmt.Errorf("refusing to create the container: a bind mount's path holds %s", why)
		}
	}
	return nil
}

// ConfiguredLabelValue is one launch value that becomes a label and whose
// source the operator controls, with that source named for the message.
type ConfiguredLabelValue struct {
	// What names the value ("model", "memoryLimit", "the project directory").
	What string
	// Source names where it came from: a config file path, a CLI flag, an
	// environment variable. "" for none worth naming.
	Source string
	Value  string
}

// CheckConfiguredLabels refuses (CS-LNCH-224) the first value that cannot be
// a label value, naming the value and its source. Called before any image
// work, so a bad value never costs a build.
func CheckConfiguredLabels(vals []ConfiguredLabelValue) error {
	for _, v := range vals {
		why := LabelValueProblem(v.Value)
		if why == "" {
			continue
		}
		src := ""
		if v.Source != "" {
			src = " (" + v.Source + ")"
		}
		return fmt.Errorf("%s%s holds %s; a value with control or bidi characters is never passed to docker or claude. Remove the character and launch again", v.What, src, why)
	}
	return nil
}

// escapeBidi writes every Bidi_Control character of a JSON text as a \uXXXX
// escape. JSON decodes it to the same rune; the label text holds none
// (CS-LNCH-226). encoding/json already escapes control characters.
func escapeBidi(s string) string {
	if strings.IndexFunc(s, isBidiControl) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isBidiControl(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
