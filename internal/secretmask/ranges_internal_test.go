package secretmask

import (
	"bytes"
	"math/rand"
	"net/url"
	"strings"
	"testing"
)

// replaced is text with each range replaced by its put.
func replaced(text []byte, rs []Range) []byte {
	var out []byte
	from := 0
	for _, r := range rs {
		out = append(append(out, text[from:r.Start]...), r.Put...)
		from = r.End
	}
	return append(out, text[from:]...)
}

// randomPiece is a piece of text for the ranges test: a value of secrets in one of its forms or
// a part of one, a key shape, a query parameter's start, or a few other bytes.
func randomPiece(r *rand.Rand, secrets []string) string {
	pieces := []string{"Bearer  ", "bearer\t", "ghp_", "AKIA", "?token=", "&sig=", "\\u0026token=", "&", "?", "***", "\x1b[0m"}
	switch r.Intn(6) {
	case 0, 1:
		s := secrets[r.Intn(len(secrets))]
		forms := []string{s, url.PathEscape(s), strings.ReplaceAll(s, `"`, `\"`), strings.ReplaceAll(s, "/", `\/`), strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(s)}
		f := forms[r.Intn(len(forms))]
		if r.Intn(3) == 0 {
			a := r.Intn(len(f))
			f = f[a : a+1+r.Intn(len(f)-a)]
		}
		return f
	case 2:
		return pieces[r.Intn(len(pieces))]
	case 3:
		const token = "ABCDEFGHIJKLMNOPQRSTUVWXYZabc0123456789"
		var b strings.Builder
		for range 10 + r.Intn(50) {
			b.WriteByte(token[r.Intn(len(token))])
		}
		return b.String()
	default:
		const other = "ab/\"<&%\\x .=*"
		var b strings.Builder
		for range 1 + r.Intn(4) {
			b.WriteByte(other[r.Intn(len(other))])
		}
		return b.String()
	}
}

// TestTheRangesAreWhatReplaceReplaces: on random text with values of the list in each form,
// key shapes, query values and proxy passwords, the text with the ranges of each step replaced
// equals the masker's Replace and the Set's, and the ranges are the scan's, not the fallback.
func TestTheRangesAreWhatReplaceReplaces(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for iter := range 3000 {
		set := New()
		var secrets []string
		for range 1 + r.Intn(3) {
			const chars = "abAB/\"<&%\\x"
			var b strings.Builder
			for range 8 + r.Intn(10) {
				b.WriteByte(chars[r.Intn(len(chars))])
			}
			if len(secrets) > 0 && r.Intn(3) == 0 {
				o := secrets[r.Intn(len(secrets))]
				b.WriteString(o[len(o)-4:])
			}
			secrets = append(secrets, b.String())
			set.Add(b.String())
		}
		if iter%10 == 0 {
			set.Masker().AddProxy("user", "proxy-pass-word")
			secrets = append(secrets, "proxy-pass-word")
		}
		var b strings.Builder
		for b.Len() < 200 {
			b.WriteString(randomPiece(r, secrets))
		}
		text := []byte(b.String())
		known, query := set.Ranges(text)
		out := []byte(set.Masker().Replace(string(text)))
		if got := replaced(text, known); !bytes.Equal(got, out) {
			t.Fatalf("%q: with the known ranges replaced %q, the masker's Replace %q", text, got, out)
		}
		if got, want := replaced(out, query), set.Replace(string(text)); string(got) != want {
			t.Fatalf("%q: with the ranges replaced %q, Replace %q", text, got, want)
		}
		if _, ok := withPuts(text, out, scanRanges(set.Masker().Finds(), text)); !ok && !bytes.Equal(text, out) {
			t.Fatalf("%q: with the scan's ranges replaced the text differs from the masker's %q", text, out)
		}
	}
}
