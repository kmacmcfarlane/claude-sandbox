package registry

import (
	"strings"
	"unicode"
)

// JSTrim is String.prototype.trim: invalid UTF-8 first becomes U+FFFD (as
// the utf8 decode does), then the ECMAScript WhiteSpace and LineTerminator
// code points are trimmed from both ends — a BOM goes, U+0085 stays, which
// strings.TrimSpace gets the other way round. The resume guard reads the
// machine-id with it (CS-SESS-068) and the resume label a --resume value
// (CS-LNCH-110), each as Claude Code 2.1.290 trims them.
func JSTrim(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\v', '\f', ' ', '\u00a0', '\ufeff', '\n', '\r', '\u2028', '\u2029':
			return true
		}
		return unicode.Is(unicode.Zs, r)
	})
}
