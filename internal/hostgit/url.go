package hostgit

import "strings"

// WithoutUserinfo is the URL u as the person reads where git goes, with the user and the
// password masked as ***: everything before the last @ of the part git takes for the
// host, so that no part of either prints, whatever characters they hold. git ends that
// part as it parses u:
//   - ssh://, git://, git+ssh://, ssh+git:// and file:// are percent-decoded first, and
//     the part ends at the first / after the host, or after its [...]; u is shown
//     decoded, as git takes it.
//   - Another scheme, http:// and https:// among them, ends it at the first of /, ? and #.
//   - git's scp-like form [user@]host:path ends it at the first :, or the first after its
//     [...].
//   - <transport>::<address> is the address, masked by these rules.
//
// A local path, and a URL with no @ in that part, is as it is.
func WithoutUserinfo(u string) string {
	if n, ok := helperName(u); ok {
		return u[:n+2] + WithoutUserinfo(u[n+2:])
	}
	if scheme, rest, ok := strings.Cut(u, "://"); ok && !strings.ContainsAny(scheme, "/:") {
		switch scheme {
		case "ssh", "git", "git+ssh", "ssh+git", "file":
			if urlScheme(scheme) {
				rest = gitDecode(rest)
			}
			return scheme + "://" + maskedBefore(rest, hostPart(rest, '/'))
		}
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		return scheme + "://" + maskedBefore(rest, end)
	}
	colon, slash := strings.IndexByte(u, ':'), strings.IndexByte(u, '/')
	if colon < 0 || (slash >= 0 && slash < colon) {
		// A local path: git reaches no host.
		return u
	}
	return maskedBefore(u, hostPart(u, ':'))
}

// helperName is the length of the transport of <transport>::<address>, which git hands
// to a remote helper by that name, ok false when u is not that form. As git scans it,
// the transport is a letter or a digit, then letters, digits, +, . and -, and may be
// empty: git takes ::<address> for that form too.
func helperName(u string) (n int, ok bool) {
	for n < len(u) && schemeChar(n == 0, u[n]) {
		n++
	}
	return n, strings.HasPrefix(u[n:], "::")
}

// schemeChar reports whether git takes c in a URL's scheme, or a remote helper's
// transport: a letter or a digit, and after the first, +, . or - as well.
func schemeChar(first bool, c byte) bool {
	alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	return alnum || !first && (c == '+' || c == '.' || c == '-')
}

// urlScheme reports whether scheme is one git takes a URL by, which it then
// percent-decodes: a letter or a digit, then letters, digits, +, . and -.
func urlScheme(scheme string) bool {
	n, _ := helperName(scheme)
	return scheme != "" && n == len(scheme)
}

// gitDecode decodes s as git decodes a URL after its scheme: each % with two hex digits
// is the byte they name, but for %00, which stays as it is.
func gitDecode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, okHi := unhex(s[i+1])
			lo, okLo := unhex(s[i+2])
			if okHi && okLo && hi<<4|lo != 0 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unhex is the value of the hex digit c.
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// hostPart is where git ends the host of s, the URL after its scheme or the scp-like
// form: at the first sep from the start, or from the ] that closes a [ at the start or
// after the first @[.
func hostPart(s string, sep byte) int {
	from, start := 0, 0
	if i := strings.Index(s, "@["); i >= 0 {
		start = i + 1
	}
	if start < len(s) && s[start] == '[' {
		if j := strings.IndexByte(s[start+1:], ']'); j >= 0 {
			from = start + 1 + j
		}
	}
	if j := strings.IndexByte(s[from:], sep); j >= 0 {
		return from + j
	}
	return len(s)
}

// maskedBefore is s with everything before the last @ of s[:end] masked as ***.
func maskedBefore(s string, end int) string {
	if i := strings.LastIndexByte(s[:end], '@'); i >= 0 {
		return "***" + s[i:]
	}
	return s
}

// ShownKey is the git key key as it can be printed, see [Shown], with any URL in its
// subsection masked by [WithoutUserinfo]: the subsection of any section, a URL of
// url.<base>.*, http.<url>.*, credential.<url>.* or lfs.<url>.* and a remote named by
// one among them, and the URL an includeIf.hasconfig:remote.*.url:<url>.* key matches.
// A subsection that is no URL shows as it is.
func ShownKey(key string) string {
	section, rest, ok := strings.Cut(key, ".")
	i := strings.LastIndexByte(rest, '.')
	if !ok || i <= 0 {
		return Shown(key)
	}
	sub := rest[:i]
	if strings.EqualFold(section, "includeif") {
		// The condition in any case: masking a key git does not match costs nothing.
		if n := len(hasRemoteURL); len(sub) >= n && strings.EqualFold(sub[:n], hasRemoteURL) {
			return Shown(section + "." + sub[:n] + WithoutUserinfo(sub[n:]) + rest[i:])
		}
	}
	return Shown(section + "." + WithoutUserinfo(sub) + rest[i:])
}

// hasRemoteURL is the condition of an includeIf key that matches a remote's URL.
const hasRemoteURL = "hasconfig:remote.*.url:"
