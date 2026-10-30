package secretmask_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/secretmask"
)

func TestMain(m *testing.M) {
	// No test here writes to the home or the XDG folders; they point at a temporary
	// folder all the same, so a change that does finds nothing of the person's.
	dir, err := os.MkdirTemp("", "secretmask-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		os.Setenv(name, filepath.Join(dir, strings.ToLower(name)))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// secret is a value of a run, with characters that percent-encoding and JSON escape.
const secret = `pa/ss"word&1`

// writeInPieces writes text to a writer of s in pieces of n bytes, flushes it and
// returns what reached the end.
func writeInPieces(t *testing.T, s *secretmask.Set, text string, n int) string {
	t.Helper()
	var out strings.Builder
	w := s.Writer(&out)
	for i := 0; i < len(text); i += n {
		if _, err := w.Write([]byte(text[i:min(i+n, len(text))])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// TestAValueSplitAcrossWritesIsMasked writes a line with a value in pieces of every size
// from one byte: each copy of the value is replaced whole, and the rest arrives as it
// was.
func TestAValueSplitAcrossWritesIsMasked(t *testing.T) {
	s := secretmask.New()
	s.Add(secret)
	text := "before " + secret + " between " + secret + " after\n"
	want := "before *** between *** after\n"
	for n := 1; n <= len(text); n++ {
		if got := writeInPieces(t, s, text, n); got != want {
			t.Errorf("in pieces of %d: %q, want %q", n, got, want)
		}
	}
}

// TestEncodedFormsAreMasked is the value percent-encoded and JSON-escaped, in a whole
// text and in a stream: each is replaced with ***, which a URL may contain unencoded.
func TestEncodedFormsAreMasked(t *testing.T) {
	s := secretmask.New()
	s.Add(secret)
	for _, c := range []struct{ name, text, want string }{
		{"percent-encoded", "u=pa%2Fss%22word%261.", "u=***."},
		{"JSON-escaped", `{"v":"pa/ss\"word\u00261"}`, `{"v":"***"}`},
		{"JSON-escaped without the HTML escapes", `{"v":"pa/ss\"word&1"}`, `{"v":"***"}`},
		{"JSON-escaped with / escaped", `{"v":"pa\/ss\"word&1"}`, `{"v":"***"}`},
	} {
		if got := s.Replace(c.text); got != c.want {
			t.Errorf("%s: Replace = %q, want %q", c.name, got, c.want)
		}
		if got := writeInPieces(t, s, c.text, 3); got != c.want {
			t.Errorf("%s: the writer wrote %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAShortValueIsLeftAlone is a value under 8 bytes, and an empty one: neither is in
// the list, so ordinary text that contains them stays as it is.
func TestAShortValueIsLeftAlone(t *testing.T) {
	s := secretmask.New()
	s.Add("")
	s.Add("seven77")
	if got := s.Replace("seven77 is in the text"); got != "seven77 is in the text" {
		t.Errorf("Replace = %q", got)
	}
	if v := s.Values(); len(v) != 0 {
		t.Errorf("Values = %q, want none", v)
	}
	s.Add("eight888")
	if got := s.Replace("eight888 is in the text"); got != "*** is in the text" {
		t.Errorf("Replace = %q", got)
	}
}

// TestEachLineOfAValueIsMasked is a key file of several lines: the whole value and each
// line of 8 bytes or more, without the spaces around it, are in the list, so a program
// that prints one line of it prints ***. A line under 8 bytes is not.
func TestEachLineOfAValueIsMasked(t *testing.T) {
	s := secretmask.New()
	key := "-----BEGIN KEY-----\r\n  MIIEvQIBADANBgkq  \nshort\n-----END KEY-----\n"
	s.Add(key)
	want := []string{key, "-----BEGIN KEY-----", "MIIEvQIBADANBgkq", "-----END KEY-----"}
	if got := s.Values(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Values = %q, want %q", got, want)
	}
	if got := s.Replace("line: MIIEvQIBADANBgkq; short"); got != "line: ***; short" {
		t.Errorf("Replace = %q", got)
	}
}

// TestTheListKeepsForagersLimits is the list, which Forager's masker takes: a value over
// 4096 bytes is left out, and so is every value past the 1024th, and neither is masked.
func TestTheListKeepsForagersLimits(t *testing.T) {
	s := secretmask.New()
	s.Add(strings.Repeat("a", secretmask.MaxValueLength+1))
	s.Add(strings.Repeat("b", secretmask.MaxValueLength))
	for i := range secretmask.MaxValues + 5 {
		s.Add(fmt.Sprintf("value-%06d", i))
	}
	got := s.Values()
	if len(got) != secretmask.MaxValues {
		t.Fatalf("%d values, want %d", len(got), secretmask.MaxValues)
	}
	if got[0] != strings.Repeat("b", secretmask.MaxValueLength) {
		t.Errorf("the first value has %d bytes, want the one of %d", len(got[0]), secretmask.MaxValueLength)
	}
	if last := got[len(got)-1]; last != fmt.Sprintf("value-%06d", secretmask.MaxValues-2) {
		t.Errorf("the last value is %q", last)
	}
	for _, left := range []string{strings.Repeat("a", secretmask.MaxValueLength+1), fmt.Sprintf("value-%06d", secretmask.MaxValues+4)} {
		if got := s.Replace(left); got != left {
			t.Errorf("Replace of a value left out of the list = %.20q", got)
		}
	}
}

// TestAValueSplitAtEachByteIsMasked writes a line with a value and a URL's token in two
// writes, split at each byte of the line in turn: both are replaced whole.
func TestAValueSplitAtEachByteIsMasked(t *testing.T) {
	s := secretmask.New()
	s.Add(secret)
	text := "a " + secret + " b https://blob.example/x?sig=c2lnbmF0dXJl&c=1\n"
	want := "a *** b https://blob.example/x?sig=***&c=1\n"
	for i := 0; i <= len(text); i++ {
		var out strings.Builder
		w := s.Writer(&out)
		w.Write([]byte(text[:i]))
		w.Write([]byte(text[i:]))
		w.Flush()
		if out.String() != want {
			t.Errorf("split at %d: %q, want %q", i, out.String(), want)
		}
	}
}

// TestAddURLPasswordAddsThePassword is a proxy URL with a password, and URLs with none:
// the password alone joins the list.
func TestAddURLPasswordAddsThePassword(t *testing.T) {
	s := secretmask.New()
	s.AddURLPassword("http://qory:proxy-password-1@127.0.0.1:3128")
	s.AddURLPassword("http://qory@127.0.0.1:3128")
	s.AddURLPassword("http://127.0.0.1:3128")
	s.AddURLPassword("://not a url")
	if got := fmt.Sprint(s.Values()); got != "[proxy-password-1]" {
		t.Errorf("the list is %s", got)
	}
}

// TestAWriterIsOnItsWritersTerminal is a Writer on a writer with a terminal, and on one
// without: Terminal returns that terminal, or the writer.
func TestAWriterIsOnItsWritersTerminal(t *testing.T) {
	var b strings.Builder
	if got := secretmask.New().Writer(&b).Terminal(); got != &b {
		t.Errorf("Terminal = %v, want the writer", got)
	}
	inner := secretmask.New().Writer(&b)
	if got := secretmask.New().Writer(inner).Terminal(); got != &b {
		t.Errorf("Terminal through two Writers = %v, want the first writer", got)
	}
}

// TestQueryValuesAreMasked is each query parameter name, in mixed case, after ? and &
// and after a & as JSON escapes it: its value is replaced up to the next & or space.
// A name that only ends like one, and a name with no ? or & before it, are left alone.
func TestQueryValuesAreMasked(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"https://blob.example/a?X-Amz-Signature=abc123 next", "https://blob.example/a?X-Amz-Signature=*** next"},
		{"https://blob.example/a?x-amz-date=1&x-AMZ-signature=abc123&b=2", "https://blob.example/a?x-amz-date=1&x-AMZ-signature=***&b=2"},
		{"https://blob.example/a?X-Amz-Security-Token=abc%2F12&b=2", "https://blob.example/a?X-Amz-Security-Token=***&b=2"},
		{"https://blob.example/a?SIGNATURE=abc%2B12#top", "https://blob.example/a?SIGNATURE=***#top"},
		{"https://blob.example/a?sv=1&Sig=abc", "https://blob.example/a?sv=1&Sig=***"},
		{"https://git.example/h.git?Token=abc.def", "https://git.example/h.git?Token=***"},
		{`{"url":"https://a.example/x?b=1\u0026Access_Token=abc"}`, `{"url":"https://a.example/x?b=1\u0026Access_Token=***"}`},
		{"https://a.example/x?mytoken=abc", "https://a.example/x?mytoken=abc"},
		{"token=abc in prose", "token=abc in prose"},
	} {
		if got := secretmask.New().Replace(c.text); got != c.want {
			t.Errorf("Replace(%q) = %q, want %q", c.text, got, c.want)
		}
		for n := 1; n <= len(c.text); n++ {
			if got := writeInPieces(t, secretmask.New(), c.text, n); got != c.want {
				t.Errorf("%q in pieces of %d: %q, want %q", c.text, n, got, c.want)
			}
		}
	}
	var none *secretmask.Set
	if got := none.Replace("a?token=abc"); got != "a?token=***" {
		t.Errorf("a nil Set: Replace = %q", got)
	}
}

// TestQueryValuesAreMaskedAcrossOddBytes is a parameter whose start is split across
// writes after bytes that are not ASCII: Kelvin signs, which change length in Unicode
// lower case, and bytes that are not UTF-8. The value is masked, and the bytes before it
// arrive as they were. A JSON-escaped & split inside its escape is masked too.
func TestQueryValuesAreMaskedAcrossOddBytes(t *testing.T) {
	for _, c := range []struct{ name, first, second, want string }{
		{"Kelvin signs", strings.Repeat("\u212a", 9) + "?tok", "en=SECRETVALUE\n", strings.Repeat("\u212a", 9) + "?token=***\n"},
		{"bytes that are not UTF-8", "\xff&tok", "en=SECRETVALUE\n", "\xff&token=***\n"},
		{"a JSON-escaped &", `{"u":"https://h/x?b=1\u00`, `26token=SECRETVALUE"}`, `{"u":"https://h/x?b=1\u0026token=***"}`},
	} {
		var out strings.Builder
		w := secretmask.New().Writer(&out)
		w.Write([]byte(c.first))
		w.Write([]byte(c.second))
		w.Flush()
		if out.String() != c.want {
			t.Errorf("%s: %q, want %q", c.name, out.String(), c.want)
		}
		if got := secretmask.New().Replace(c.first + c.second); got != c.want {
			t.Errorf("%s: Replace = %q, want %q", c.name, got, c.want)
		}
	}
	// A parameter name with a Kelvin sign for its k is not one of the names.
	if got := secretmask.New().Replace("?to\u212aen=abc"); got != "?to\u212aen=abc" {
		t.Errorf("Replace = %q", got)
	}
}

// TestAWriterReleasesTheListBeforeItWritesOn is an Add while the writer below a Writer
// is blocked, and a writer below that calls the Set, as qory's status writer does: the
// Add returns at once, and nothing waits on the other.
func TestAWriterReleasesTheListBeforeItWritesOn(t *testing.T) {
	s := secretmask.New()
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var out strings.Builder
	w := s.Writer(writerFunc(func(b []byte) (int, error) {
		once.Do(func() {
			close(blocked)
			<-release
		})
		s.Add("added-in-a-write")
		return out.WriteString(s.Replace(string(b)))
	}))
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		w.Write([]byte("first line\n"))
	}()
	<-blocked
	added := make(chan struct{})
	go func() {
		s.Add("added-while-blocked")
		close(added)
	}()
	select {
	case <-added:
	case <-time.After(10 * time.Second):
		t.Fatal("Add waited for the blocked write")
	}
	close(release)
	select {
	case <-wrote:
	case <-time.After(10 * time.Second):
		t.Fatal("the write did not return")
	}
	w.Write([]byte("added-while-blocked added-in-a-write\n"))
	w.Flush()
	if got, want := out.String(), "first line\n*** ***\n"; got != want {
		t.Errorf("the output %q, want %q", got, want)
	}
}

// TestAddQueryAddsTheQueryValues is a source URL with a query: each value of 8 bytes or
// more is in the list, as written and decoded, so a text that has it elsewhere, out of
// its URL, is masked too.
func TestAddQueryAddsTheQueryValues(t *testing.T) {
	s := secretmask.New()
	s.AddQuery("https://git.example/h.git?ref=main&key=abc%2Fdef123&x")
	if got, want := fmt.Sprint(s.Values()), "[abc%2Fdef123 abc/def123]"; got != want {
		t.Errorf("Values = %s, want %s", got, want)
	}
	if got := s.Replace("cloning with abc/def123 on main"); got != "cloning with *** on main" {
		t.Errorf("Replace = %q", got)
	}
}

// TestAWriterMasksAValueAddedLater adds a value while writers write from several
// goroutines: the run's values can be known after its streams are wrapped. Every line
// written after the Add is masked.
func TestAWriterMasksAValueAddedLater(t *testing.T) {
	s := secretmask.New()
	var mu sync.Mutex
	var out strings.Builder
	w := s.Writer(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return out.Write(b)
	}))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				w.Write([]byte("noise line\n"))
			}
		})
	}
	s.Add("added-later-value")
	wg.Wait()
	w.Write([]byte("now added-later-value\n"))
	w.Flush()
	if strings.Contains(out.String(), "added-later-value") || !strings.HasSuffix(out.String(), "now ***\n") {
		t.Errorf("the output ends %q", out.String()[max(0, out.Len()-40):])
	}
}

// writerFunc is a function as an io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// TestKeyShapesSplitAtEachByteAreMasked is a key of each shape that Forager's
// mask.Shapes lists, at a real length, in a line written in two writes, split at each
// byte in turn: qory's Writer replaces the whole key with ***, and a Set with no value
// does so too.
func TestKeyShapesSplitAtEachByteAreMasked(t *testing.T) {
	alnum := "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4Y5z6A7b8C9d0E1f2G3h4I5j6K7l8M9n0O1p2"
	for _, key := range []string{
		"sk-ant-api03-" + alnum + "_-AbCdEfGhIjKlMnOpQrSAA",
		"ghp_" + alnum[:36],
		"ghs_" + alnum[:36],
		"gho_" + alnum[:36],
		"github_pat_11ABCDEFG0" + alnum[:72],
		"AKIAIOSFODNN7EXAMPLE",
		"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	} {
		text := "key " + key + " end\n"
		for i := 0; i <= len(text); i++ {
			var out strings.Builder
			w := secretmask.New().Writer(&out)
			w.Write([]byte(text[:i]))
			w.Write([]byte(text[i:]))
			w.Flush()
			if out.String() != "key *** end\n" {
				t.Errorf("%.12s split at %d: %q", key, i, out.String())
				break
			}
		}
	}
}

// TestTheMaskerHasTheSetsValues is the masker that qory run gives Forager's session: it
// replaces each value the Set has, and a value Forager adds to it is masked by the Set's
// writers too.
func TestTheMaskerHasTheSetsValues(t *testing.T) {
	s := secretmask.New()
	s.Add("set-secret-value-1")
	m := s.Masker()
	if got := m.Replace("a set-secret-value-1 b"); got != "a *** b" {
		t.Errorf("the masker replaced to %q", got)
	}
	m.AddSecret([]byte("server-variable-value"))
	if got := s.Replace("c server-variable-value d"); got != "c *** d" {
		t.Errorf("the Set replaced a value Forager added to %q", got)
	}
	if (*secretmask.Set)(nil).Masker() != nil {
		t.Error("a nil Set has a masker")
	}
}
