package exports

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/qoryai/forager/accesskey"
)

// runCodePhrase takes the place of a value that contains a run code and neither an
// access key secret nor a run key, as [accesskey.ShownValue] prints one.
const runCodePhrase = "(a value that contains a run code)"

// containsSecret reports whether v contains an access key secret, a run key, a run code
// or an enrolment code: qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ in any letter
// case, as [accesskey.ContainsSecret] checks.
func containsSecret(v string) bool {
	return accesskey.ContainsSecret(v) || strings.Contains(strings.ToLower(v), accesskey.CodePrefixRun)
}

// Value is a value a person wrote, as a message prints it: quoted when quote is true,
// bare when it is false, and, when it contains an access key secret, a run key, a run
// code or an enrolment code, qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ in any
// letter case, the phrase [accesskey.ShownValue] prints in its place, which is
// [runCodePhrase] for a run code alone.
// A value is what a key is set to, such as an env value, a command, a path or a URL; a
// name a person wrote prints as [accesskey.ShownName] prints it.
func Value(v string, quote bool) string {
	if !accesskey.ContainsSecret(v) && containsSecret(v) {
		return runCodePhrase
	}
	return accesskey.ShownValue(v, quote)
}

// Values is each value as [Value] prints it bare, in order.
func Values(vs []string) []string {
	if vs == nil {
		return nil
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = Value(v, false)
	}
	return out
}

// URL is a git URL a person wrote, as a message prints it: as [Value] prints it bare
// when it contains an access key secret, a run key, a run code or an enrolment code, as
// written or with its letters percent-encoded, and otherwise with the password in its
// user part printed as (hidden), as in https://user:(hidden)@git.example/h.git.
func URL(u string) string {
	if secret(u) {
		return shown(u)
	}
	return HideURLs(u)
}

// secret reports whether v contains an access key secret, a run key, a run code or an
// enrolment code, as [containsSecret] checks, as written or after each %XX escape in it
// is decoded, as git decodes a URL's user part, path, query and fragment.
func secret(v string) bool {
	return containsSecret(v) || containsSecret(unescaped(v))
}

// shown is v, which [secret] reports contains a secret, as [Value] prints it bare. The
// phrase is chosen from v with its escapes decoded, since v as written may contain
// none of qak_, qrk_, qecr_, qecn_, qecp_, qecg_ and qeci_.
func shown(v string) string { return Value(unescaped(v), false) }

// unescaped is v with each %XX escape decoded, X a hex digit, and every other byte kept.
func unescaped(v string) string {
	if !strings.Contains(v, "%") {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && i+2 < len(v) {
			if c, err := strconv.ParseUint(v[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(c))
				i += 2
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// password matches what a URL in a text carries up to its host, scheme://user:password@,
// with the scheme and the user in group 1. The password ends at the last @ before the
// first /, ?, # or space after the scheme, the rule a URL's authority follows.
var password = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/?#@\s:]*):[^/?#\s]*@`)

// HideURLs is text with the password in the user part of each URL printed as (hidden):
// https://user:token@git.example/h.git as https://user:(hidden)@git.example/h.git. A URL
// with a user and no password, and one with no user part, stay as they are.
func HideURLs(text string) string {
	return password.ReplaceAllString(text, "$1:(hidden)@")
}

// HideValue is text with each copy of value, and each word of text that still contains
// an access key secret, a run key, a run code or an enrolment code, printed as [Value]
// prints it bare, when value contains one of them, as [containsSecret] checks; text as
// [HideValues] prints it otherwise.
// A message whose text includes a value a person wrote, such as the error of a command
// that quotes the command, passes it through HideValue with that value.
func HideValue(text, value string) string {
	if value != "" && containsSecret(value) {
		text = strings.ReplaceAll(text, value, Value(value, false))
	}
	return HideValues(text)
}

// HideValues is text with each word that contains an access key secret, a run key, a
// run code or an enrolment code, as written or with its letters percent-encoded,
// printed as [Value] prints it bare. A word is what stands between spaces and quotes,
// or a string quoted as Go's %q quotes one, with its quotes; the colons, semicolons and
// commas at its end stay after the phrase.
func HideValues(text string) string {
	if !secret(text) {
		return text
	}
	return word.ReplaceAllStringFunc(text, func(w string) string {
		if !secret(w) {
			return w
		}
		end := strings.TrimRight(w, ":;,")
		return shown(end) + w[len(end):]
	})
}

// HideValueError is err with its text printed as [HideValue] prints it with value, when
// the text contains an access key secret, a run key, a run code or an enrolment code,
// as [containsSecret] checks. The result unwraps to err, so a
// caller still matches an [*exec.ExitError] or another error under it with errors.Is and
// errors.As. An error with no secret, and nil, are returned as they are.
func HideValueError(err error, value string) error {
	if err == nil || !containsSecret(err.Error()) {
		return err
	}
	return hiddenError{HideValue(err.Error(), value), err}
}

// HideURLError is err with the password in each URL of its text printed as (hidden),
// as [HideURLs] prints it, and each word that contains an access key secret, a run key
// or a run code printed as [HideValues] prints it. The result unwraps to err; an error whose text
// needs neither, and nil, are returned as they are.
func HideURLError(err error) error {
	if err == nil {
		return nil
	}
	text := HideValues(HideURLs(err.Error()))
	if text == err.Error() {
		return err
	}
	return hiddenError{text, err}
}
