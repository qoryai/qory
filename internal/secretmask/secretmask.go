// Package secretmask replaces the secret values of a run with *** in what qory prints, as
// GitHub Actions masks a workflow's secrets.
//
// A [Set] is the list of a run's known values. [Set.Add] adds a value, also each line of a
// value that has several, and [Set.AddQuery] adds the query values of a URL. Forager's
// mask package does the matching, with one [mask.Masker] per Set: each value is replaced
// as it is, percent-encoded and JSON-escaped, with ***, and so is each key shape of
// [mask.Shapes]. A value shorter than [MinLength] is left out, since it would also
// replace ordinary text. [Set.Masker] is that masker, which qory run gives Forager's
// session of the run, so the run's record and events are masked with the same values.
//
// A Set also replaces the value of each URL query parameter whose name is one of
// [QueryNames], its ASCII letters in any case, since a signed URL, such as a registry's signed blob
// URL, is not one of the run's values.
//
// [Set.Writer] masks a stream, a value split across two writes included; [Set.Replace]
// masks a whole text, such as a line or an error. [Default] is the Set of this process:
// qory run adds its values to it at the start, and the main package masks the last error
// line with it.
package secretmask

import (
	"bytes"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/qoryai/forager/mask"
)

// Placeholder is what replaces a value.
const Placeholder = "***"

// MinLength is the length, in bytes, under which a value is not masked.
const MinLength = 8

// Limits of the list, which are the limits of Forager's masker of the run's events. A
// value past them is not added: a value longer than MaxValueLength, and every value once
// the list has MaxValues.
const (
	// MaxValues is the most values the list contains.
	MaxValues = 1024
	// MaxValueLength is the longest value, in bytes, that the list contains.
	MaxValueLength = 4096
)

// QueryNames are the names of the URL query parameters whose values a Set replaces in
// any text, their ASCII letters in any case.
var QueryNames = []string{"X-Amz-Signature", "X-Amz-Security-Token", "Signature", "sig", "token", "access_token"}

// Set is a run's list of known secret values. The zero Set is not usable; [New] makes
// one. A nil Set masks only the query parameters of [QueryNames]. A Set is safe for
// concurrent use, Add included.
type Set struct {
	// mu guards values, and orders an Add after a scan that started before it.
	mu     sync.RWMutex
	m      *mask.Masker
	values [][]byte
}

// New returns an empty Set.
func New() *Set { return &Set{m: mask.New()} }

// process is the Set that [Default] returns.
var process atomic.Pointer[Set]

func init() { process.Store(New()) }

// Default returns the Set of this process. qory runs one command per process, so the
// values that qory run adds are the run's, and the main package reaches them for the
// last error line.
func Default() *Set { return process.Load() }

// SetDefault makes s the Set that [Default] returns and returns the Set it replaces. A
// test uses it to start from an empty Set and to put the old one back.
func SetDefault(s *Set) *Set { return process.Swap(s) }

// Add adds value to the list, and each line of value, without the spaces around it,
// since a program often prints a key file or a certificate line by line. A value or a
// line shorter than [MinLength] bytes is left out.
func (s *Set) Add(value string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add(value)
	if strings.ContainsAny(value, "\r\n") {
		for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
			s.add(strings.TrimSpace(line))
		}
	}
}

// add adds value in each form, unless it is shorter than [MinLength], longer than
// [MaxValueLength], in the list already, or the list has [MaxValues] values: Forager's
// masker of the run's events takes no more. The caller has taken the write lock.
func (s *Set) add(value string) {
	if len(value) < MinLength || len(value) > MaxValueLength || len(s.values) == MaxValues {
		return
	}
	v := []byte(value)
	for _, have := range s.values {
		if bytes.Equal(have, v) {
			return
		}
	}
	s.m.AddSecret(v)
	s.values = append(s.values, v)
}

// Masker returns the Set's masker, for OpenSpec.Masker of Forager's session. Forager adds
// the server's variables and the passwords of the run's forwarders to it, so qory's
// writers mask those too. A nil Set has none.
func (s *Set) Masker() *mask.Masker {
	if s == nil {
		return nil
	}
	return s.m
}

// AddQuery adds the value of each query parameter of rawURL, as [Set.Add] adds a value,
// both as the URL contains it and decoded. A URL that does not parse adds nothing.
func (s *Set) AddQuery(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery == "" {
		return
	}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		_, raw, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		s.Add(raw)
		if decoded, err := url.QueryUnescape(raw); err == nil && decoded != raw {
			s.Add(decoded)
		}
	}
}

// AddURLPassword adds the password of rawURL, such as a proxy URL
// http://qory:<password>@127.0.0.1:3128, as [Set.Add] adds a value. A URL that does not
// parse, or has no password, adds nothing.
func (s *Set) AddURLPassword(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return
	}
	if password, ok := u.User.Password(); ok {
		s.Add(password)
	}
}

// Values returns the list: the values in the order they were added, within the limits
// [MaxValues] and [MaxValueLength].
func (s *Set) Values() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.values))
	for _, v := range s.values {
		out = append(out, string(v))
	}
	return out
}

// Replace returns text with each known value, and the value of each query parameter of
// [QueryNames], replaced with [Placeholder]. A nil Set replaces only the query values.
func (s *Set) Replace(text string) string {
	if s != nil {
		s.mu.RLock()
		text = s.m.Replace(text)
		s.mu.RUnlock()
	}
	out, _, _ := maskQuery(nil, []byte(text), true)
	return string(out)
}

// Writer returns a writer that writes to w what is written to it, masked as
// [Set.Replace] masks it. A value added to s after the writer is made is masked from the
// next write on. Call [Writer.Flush] when the stream ends.
func (s *Set) Writer(w io.Writer) *Writer {
	m := s
	if m == nil {
		m = New()
	}
	mw := &Writer{set: m, w: w}
	mw.query.w = &mw.out
	mw.known = mask.NewWriter(&mw.query, m.m)
	return mw
}

// Writer masks a stream for a [Set]. It keeps back the end of each write that a value or
// the start of a query parameter could begin in, and writes it with the next write or on
// Flush. It masks each write into a buffer under the Set's read lock and writes the
// buffer to w after it releases that lock, so a slow w delays no [Set.Add], and w may
// call the Set. It is safe for concurrent writes, which reach w in the order they were
// masked.
type Writer struct {
	set *Set
	w   io.Writer
	// mu orders the writes: a write is masked and written to w before the next starts.
	mu    sync.Mutex
	known *mask.Writer
	query queryWriter
	// out is what the masking of one write produced, for w.
	out bytes.Buffer
}

// Write writes b, masked, keeping back the end in which a value could start.
func (w *Writer) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.set.mu.RLock()
	_, err := w.known.Write(b)
	w.set.mu.RUnlock()
	if err == nil {
		err = w.drain()
	}
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// Flush writes the bytes kept back: the stream is over.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.set.mu.RLock()
	err := w.known.Flush()
	w.set.mu.RUnlock()
	w.query.flush()
	if derr := w.drain(); err == nil {
		err = derr
	}
	return err
}

// Terminal is w's terminal, so qory's ui on the writer colours its text as it would on
// w: the masking leaves a colour's escape sequence as it is.
func (w *Writer) Terminal() io.Writer {
	if term, ok := w.w.(interface{ Terminal() io.Writer }); ok {
		return term.Terminal()
	}
	return w.w
}

// drain writes the masked bytes in out to w. The caller holds mu.
func (w *Writer) drain() error {
	if w.out.Len() == 0 {
		return nil
	}
	_, err := w.w.Write(w.out.Bytes())
	w.out.Reset()
	return err
}

// queryOpen matches the start of a query parameter whose value is masked, up to its =,
// in a text in lower case: a ?, a &, or a & as JSON escapes it, then one of [QueryNames].
var queryOpen = regexp.MustCompile(`(?:\?|&|\\u0026)(?:x-amz-signature|x-amz-security-token|signature|sig|token|access_token)=`)

// jsonAmp is a & as JSON escapes it.
const jsonAmp = "\\u0026"

// queryOpeners are the texts queryOpen matches, for the end of a write that could be the
// start of one.
var queryOpeners = func() []string {
	var out []string
	for _, sep := range []string{"?", "&", jsonAmp} {
		for _, name := range QueryNames {
			out = append(out, sep+strings.ToLower(name)+"=")
		}
	}
	return out
}()

// longestOpener is the length of the longest of queryOpeners.
var longestOpener = func() int {
	n := 0
	for _, o := range queryOpeners {
		n = max(n, len(o))
	}
	return n
}()

// valueByte reports whether c can be part of a query value: any byte but a space, a
// control character, and the characters that end a value in a URL, in JSON, in HTML or
// in a shell line.
func valueByte(c byte) bool {
	if c <= ' ' || c == 0x7f {
		return false
	}
	return !strings.ContainsRune("&#\"'<>\\`,;()[]{}", rune(c))
}

// lowerASCII is b with the ASCII letters in lower case and every other byte as it is,
// so each byte stays at its place, as in Forager's mask package. Only ASCII letters
// fold: a parameter name with another letter that folds to one of them, such as the
// Kelvin sign, is not matched, in a whole text and across writes alike.
func lowerASCII(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// maskQuery appends b to dst with the value of each query parameter of [QueryNames]
// replaced with [Placeholder]. Unless final, it keeps back the end of b that could be the
// start of such a parameter, before its =; hold is where those bytes start in b. open
// reports that b ends inside a value: the value's next bytes, at the start of the next
// write, are not written.
func maskQuery(dst, b []byte, final bool) (out []byte, hold int, open bool) {
	lower := lowerASCII(b)
	i := 0
	for _, loc := range queryOpen.FindAllIndex(lower, -1) {
		if loc[0] < i {
			continue
		}
		dst = append(append(dst, b[i:loc[1]]...), Placeholder...)
		i = loc[1]
		for i < len(b) && valueByte(b[i]) {
			i++
		}
		if i == len(b) {
			return dst, len(b), !final
		}
	}
	hold = len(b)
	if !final {
		for s := max(i, len(b)-longestOpener+1); s < len(b); s++ {
			if startsOpener(lower[s:]) {
				hold = s
				break
			}
		}
	}
	return append(dst, b[i:hold]...), hold, false
}

// startsOpener reports whether one of queryOpeners starts with b, b in lower case.
func startsOpener(b []byte) bool {
	for _, o := range queryOpeners {
		if strings.HasPrefix(o, string(b)) {
			return true
		}
	}
	return false
}

// queryWriter writes to w with the value of each query parameter of [QueryNames]
// replaced, across writes. Its [Writer] orders the calls.
type queryWriter struct {
	w io.Writer
	// tail is the end of the last write that could start a parameter.
	tail []byte
	// open is set when the last write ended inside a value: the next write's first
	// value bytes are the rest of it.
	open bool
}

// Write writes b with each value replaced, keeping back the end that could start a
// parameter.
func (q *queryWriter) Write(b []byte) (int, error) {
	buf := append(q.tail, b...)
	if q.open {
		i := 0
		for i < len(buf) && valueByte(buf[i]) {
			i++
		}
		if i == len(buf) {
			// The whole write is more of the value.
			q.tail = nil
			return len(b), nil
		}
		buf = buf[i:]
		q.open = false
	}
	out, hold, open := maskQuery(nil, buf, false)
	q.tail = append([]byte(nil), buf[hold:]...)
	q.open = open
	if len(out) == 0 {
		return len(b), nil
	}
	if _, err := q.w.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

// flush writes the bytes kept back, which start no parameter, to w, a buffer: the
// stream is over.
func (q *queryWriter) flush() {
	q.open = false
	if len(q.tail) > 0 {
		q.w.Write(q.tail)
		q.tail = nil
	}
}
