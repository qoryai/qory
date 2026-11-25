package cmd

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/secretmask"
)

// sequences matches the escape sequences that [unescaped] removes: a control sequence, and a
// string sequence up to BEL or ESC \.
var sequences = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b[\\]PX^_][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// stripRounds is b with the matches of pattern removed one round at a time until none is left,
// which [unescaped] does in one pass.
func stripRounds(pattern *regexp.Regexp, b []byte) []byte {
	for {
		next := pattern.ReplaceAll(b, nil)
		if bytes.Equal(next, b) {
			return b
		}
		b = next
	}
}

// TestAStripInOnePassEqualsStrippingUntilNoneIsLeft: on random text of escape bytes, sequence
// bytes and other bytes, the strip in one pass and the strip round by round have the same
// result, and kept is the offset of each byte that stays.
func TestAStripInOnePassEqualsStrippingUntilNoneIsLeft(t *testing.T) {
	const chars = "\x1b\x1b\x1b\x1b[[[0;:?m1 /@~a\x07\\\\]]PX^_\x7f\x01"
	r := rand.New(rand.NewSource(3))
	for range 20000 {
		b := make([]byte, r.Intn(40))
		for i := range b {
			b[i] = chars[r.Intn(len(chars))]
		}
		want := stripRounds(sequences, b)
		got, kept, open, dead := unescaped(b, true)
		if !bytes.Equal(got, want) {
			t.Fatalf("%q: one pass %q, round by round %q", b, got, want)
		}
		if open != len(b) && (open > len(b) || b[open] != 0x1b) || dead != len(b) && (dead > len(b) || b[dead] != 0x1b) {
			t.Fatalf("%q: open at %d, dead at %d", b, open, dead)
		}
		for i, at := range kept {
			if len(kept) != len(got) || b[at] != got[i] || i > 0 && at <= kept[i-1] {
				t.Fatalf("%q: kept %v for %q", b, kept, got)
			}
		}
	}
}

// TestALineOfNestedEscapeSequencesTakesOnePass: a line of 88 KB of nested escape sequences goes
// into a step's log in well under a second, for a program's output and for qory's lines.
func TestALineOfNestedEscapeSequencesTakesOnePass(t *testing.T) {
	d := 88_000 / 4
	line := strings.Repeat("\x1b[", d) + "0m" + strings.Repeat("1m", d)
	for _, program := range []bool{true, false} {
		step := &hookStep{}
		w := &stepLines{stderr: io.Discard, step: step, program: program}
		start := time.Now()
		fmt.Fprint(w, line+"\n")
		took := time.Since(start)
		t.Logf("program %v: %v", program, took)
		if took > time.Second || len(step.lines) != 1 {
			t.Errorf("program %v: %d lines in %v", program, len(step.lines), took)
		}
	}
}

// TestAStepsLogKeepsTheTextOfAHyperlink: the string sequences of a hyperlink, a title and a
// device control string leave none of their bytes in a step's log, so the link's URL is not
// there, and the link's text stays.
func TestAStepsLogKeepsTheTextOfAHyperlink(t *testing.T) {
	const url = "https://h.example/x"
	for _, c := range []struct{ name, line, want string }{
		{"a hyperlink", "see \x1b]8;;" + url + "\x1b\\the page\x1b]8;;\x1b\\ now", "see the page now"},
		{"a hyperlink that ends with BEL", "\x1b]8;;" + url + "\x07the page\x1b]8;;\x07", "the page"},
		{"a title that ends with BEL", "\x1b]0;a title\x07text", "text"},
		{"a device control string", "a\x1bPq#0;2\x1b\\b", "ab"},
		{"SOS, PM and APC", "\x1bXs\x1b\\a\x1b^p\x07b\x1b_a\x1b\\c", "abc"},
		{"a colour inside a title", "\x1b]0;a \x1b[1mtitle\x07text", "text"},
	} {
		for _, program := range []bool{true, false} {
			step := &hookStep{}
			w := &stepLines{stderr: io.Discard, step: step, program: program}
			fmt.Fprint(w, c.line+"\n")
			if len(step.lines) != 1 || step.lines[0] != c.want+"\n" {
				t.Errorf("%s, program %v: the log %q, want %q", c.name, program, step.lines, c.want)
			}
			if strings.Contains(strings.Join(step.lines, ""), "h.example") {
				t.Errorf("%s, program %v: the log %q has the URL", c.name, program, step.lines)
			}
		}
	}
}

// randomSecret is a value for the mask list of 8 to 17 bytes, of bytes that the five forms of
// a value change, with *** in it when stars is set.
func randomSecret(r *rand.Rand, stars bool) string {
	var b strings.Builder
	for range 8 + r.Intn(10) {
		b.WriteByte(`abAB/"<&%\x`[r.Intn(11)])
	}
	s := b.String()
	if stars && r.Intn(4) == 0 {
		s = s[:3] + "***" + s[3:]
	}
	return s
}

// randomSet is a mask list of 1 to 3 random values, a value sometimes starting with the last 4
// bytes of another, so that their finds overlap.
func randomSet(r *rand.Rand, stars bool) (*secretmask.Set, []string) {
	set := secretmask.New()
	var secrets []string
	for range 1 + r.Intn(3) {
		s := randomSecret(r, stars)
		if len(secrets) > 0 && r.Intn(3) == 0 {
			o := secrets[r.Intn(len(secrets))]
			s = o[len(o)-4:] + s
		}
		secrets = append(secrets, s)
		set.Add(s)
	}
	return set, secrets
}

// forms is s in each form that the mask list masks: as it is, percent-encoded, and JSON-escaped
// with and without the escapes of < > &, and with / escaped.
func forms(s string) []string {
	var p, j strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			p.WriteByte(c)
		} else {
			fmt.Fprintf(&p, "%%%02X", c)
		}
		if c == '"' || c == '\\' {
			j.WriteByte('\\')
		}
		j.WriteByte(c)
	}
	html := strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(j.String())
	return []string{s, p.String(), html, j.String(), strings.ReplaceAll(j.String(), "/", `\/`)}
}

// randomLine is a line of n bytes or more for the properties of [keptLine]: forms of the
// secrets and parts of them, key shapes, query parameters, escape sequences and other bytes.
func randomLine(r *rand.Rand, secrets []string, n int) string {
	extras := []string{"Bearer ", "bearer\t", "ghp_", "sk-ant-", "AKIA", "?token=", "&sig=", `\u0026token=`, "&", "?",
		"\x1b[", "\x1b[0m", "\x1b[1m", "\x1b]8;;u\x1b\\", "\x1b]0;t\x07", "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"\x1b]0;", "\x1bP", "\a", "\x1b\\", "\x1b]0;a\x1b[1mb\x07", "AKIA0000000000000000",
		"ghp_" + strings.Repeat("a1B", 12), "Bearer 0123456789abcdefXYZ"}
	const other = "abAB/\"<&%\\ \x1b[0m"
	var b strings.Builder
	for b.Len() < n {
		switch r.Intn(5) {
		case 0, 1:
			f := forms(secrets[r.Intn(len(secrets))])[r.Intn(5)]
			if r.Intn(3) == 0 {
				a := r.Intn(len(f))
				f = f[a : a+1+r.Intn(len(f)-a)]
			}
			b.WriteString(f)
		case 2:
			b.WriteString(extras[r.Intn(len(extras))])
		default:
			for range 1 + r.Intn(4) {
				b.WriteByte(other[r.Intn(len(other))])
			}
		}
	}
	line := b.String()
	if r.Intn(4) == 0 {
		// Copies of one form or key after a query parameter's start or the start of an escape
		// sequence: the cut and the end of the buffer can fall at the same place in two copies,
		// and the masks of the copies, ***, can keep a sequence open past the buffer.
		f := forms(secrets[r.Intn(len(secrets))])[r.Intn(5)]
		if r.Intn(3) == 0 {
			f = extras[21+r.Intn(3)]
		}
		at := r.Intn(len(line))
		start := []string{extras[5+r.Intn(3)], "\x1b[", "\x1b]0;"}[r.Intn(3)]
		line = line[:at] + start + strings.Repeat(f, 3+r.Intn(20)) + line[at:]
	}
	if r.Intn(2) == 0 {
		line = strings.NewReplacer("\x1b[0m", "\x1b[\x1b[0m1m", "\x1b[1m", "\x1b[38:5:1m").Replace(line)
	}
	return line
}

// The cut of the property tests, and the lookahead: 6 bytes for each byte of the longest value
// of randomSet, 4 bytes of another value, 17 bytes and ***.
const propertyAhead = 6 * (4 + 17 + 3)

// loggedLine is the log of line as a stepLines of a program with keep and ahead in place of
// [maxStepLine] and [lookahead] writes it, with set as the mask list, without the line break,
// and buf the bytes of line that the writer keeps.
func loggedLine(set *secretmask.Set, line string, keep int) (logged, buf string) {
	old := secretmask.SetDefault(set)
	defer secretmask.SetDefault(old)
	buf, more := line, false
	if len(line) > keep+propertyAhead {
		buf, more = line[:keep+propertyAhead], true
	}
	out, lost := keptLine([]byte(buf), more, keep, propertyAhead)
	if more || lost {
		out = append(out, textLineCut...)
	}
	return string(logText(out)), buf
}

// maskedWith is logText of line with set as the mask list.
func maskedWith(set *secretmask.Set, line string) string {
	old := secretmask.SetDefault(set)
	defer secretmask.SetDefault(old)
	return string(logText([]byte(line)))
}

// firstMaskedWith is line after the first mask of [logLine], with set as the run's secrets.
func firstMaskedWith(set *secretmask.Set, line string) string {
	old := secretmask.SetDefault(set)
	defer secretmask.SetDefault(old)
	m, _ := firstMask([]byte(line), false)
	return string(m)
}

// TestALongLinesLogIsAPrefixOfTheWholeLineMasked: on random lines, mask lists and cuts, the log
// of a line, without the mark of the cut and a final ***, is a prefix of the whole line masked,
// so it has no byte of a secret that the whole line masks.
func TestALongLinesLogIsAPrefixOfTheWholeLineMasked(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	fails := 0
	for range 20000 {
		set, secrets := randomSet(r, true)
		keep := 20 + r.Intn(40)
		line := randomLine(r, secrets, keep+r.Intn(250))
		logged, _ := loggedLine(set, line, keep)
		whole := maskedWith(set, line)
		o := strings.TrimSuffix(logged, textLineCut)
		if !strings.HasPrefix(whole, o) {
			o = strings.TrimRight(o, "*")
		}
		if !strings.HasPrefix(whole, o) {
			t.Errorf("secrets %q, cut at %d\nline   %q\nlogged %q\nwhole  %q", secrets, keep, line, logged, whole)
			if fails++; fails == 3 {
				return
			}
		}
	}
}

// TestALongLinesLogDependsOnlyOnTheMaskedLine: the log is the same for two mask lists, random or
// one with a value that starts before the cut, that mask a random line and its kept bytes the
// same way, in the first round too, so the log does not show which bytes could start a secret.
func TestALongLinesLogDependsOnlyOnTheMaskedLine(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	fails, compared := 0, 0
	for range 20000 {
		set1, secrets := randomSet(r, false)
		set2, _ := randomSet(r, false)
		keep := 20 + r.Intn(40)
		line := randomLine(r, secrets, keep+r.Intn(250))
		if r.Intn(2) == 0 && len(line) > keep {
			// set2 has set1's values and one that starts with the bytes before the cut.
			set2 = secretmask.New()
			for _, s := range secrets {
				set2.Add(s)
			}
			set2.Add(line[keep-1-r.Intn(8):keep] + randomSecret(r, false))
		}
		logged1, buf := loggedLine(set1, line, keep)
		if maskedWith(set1, line) != maskedWith(set2, line) || maskedWith(set1, buf) != maskedWith(set2, buf) ||
			firstMaskedWith(set1, buf) != firstMaskedWith(set2, buf) {
			continue
		}
		compared++
		if logged2, _ := loggedLine(set2, line, keep); logged1 != logged2 {
			t.Errorf("secrets %q, cut at %d\nline  %q\nlog 1 %q\nlog 2 %q", secrets, keep, line, logged1, logged2)
			if fails++; fails == 3 {
				return
			}
		}
	}
	t.Logf("%d lines compared", compared)
	if compared < 1000 {
		t.Errorf("%d lines compared, want 1000 or more", compared)
	}
}
