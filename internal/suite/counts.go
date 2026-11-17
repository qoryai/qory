package suite

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

// Counts are the numbers of a suite's result file: the tests it ran, the ones that
// failed, and the ones that did not run.
type Counts struct {
	Tests   int `json:"tests"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// MaxResultBytes is the largest result file qory reads.
const MaxResultBytes = 16 << 20

// ErrMismatch is the error of a result file that counts more tests, failures or skipped
// tests in a testsuite element than its testcase elements have, or has a failure outside
// every testcase: the file cannot show what ran.
var ErrMismatch = errors.New("the result file's totals do not match its testcases")

// ErrNotJUnit is the error of a result file that has no testsuite or testsuites element.
var ErrNotJUnit = errors.New("the result file is not JUnit XML")

// ParseJUnit reads the counts of text, a JUnit XML file, the form most test runners
// write: each testcase element is a test; it failed when it has a failure or error
// element, and was skipped when it has a skipped element and did not fail. Comments and
// CDATA sections count as no element.
func ParseJUnit(data []byte) (Counts, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// A declared encoding is not read: the harness's checks ignore it, and so do we.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	var (
		counts  Counts
		suites  bool
		inCase  int
		failed  bool
		skipped bool
		stated  []Counts
		outside bool
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Counts{}, ErrNotJUnit
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testsuite", "testsuites":
				suites = true
				stated = append(stated, statedCounts(t))
			case "testcase":
				if inCase == 0 {
					failed, skipped = false, false
				}
				inCase++
			case "failure", "error":
				if inCase == 0 {
					outside = true
				} else {
					failed = true
				}
			case "skipped":
				if inCase > 0 {
					skipped = true
				}
			}
		case xml.EndElement:
			if t.Name.Local == "testcase" && inCase > 0 {
				inCase--
				if inCase == 0 {
					counts.Tests++
					switch {
					case failed:
						counts.Failed++
					case skipped:
						counts.Skipped++
					}
				}
			}
		}
	}
	if !suites {
		return Counts{}, ErrNotJUnit
	}
	if outside {
		return counts, ErrMismatch
	}
	for _, s := range stated {
		if s.Tests > counts.Tests || s.Failed > counts.Failed || s.Skipped > counts.Skipped {
			return counts, ErrMismatch
		}
	}
	return counts, nil
}

// statedCounts is what the attributes of a testsuite element state: tests, failures plus
// errors, and skipped. An attribute that is no whole number counts as 0.
func statedCounts(e xml.StartElement) Counts {
	number := func(name string) int {
		for _, a := range e.Attr {
			if a.Name.Local != name {
				continue
			}
			n := 0
			for _, c := range a.Value {
				if c < '0' || c > '9' || n > 1<<30 {
					return 0
				}
				n = n*10 + int(c-'0')
			}
			return n
		}
		return 0
	}
	return Counts{Tests: number("tests"), Failed: number("failures") + number("errors"), Skipped: number("skipped")}
}

// Stamp separates a result file that the suite wrote from one that the commit had, by its
// modification time and the SHA-256 of its content. The zero Stamp is no file.
type Stamp struct {
	ModTime time.Time
	Sum     [sha256.Size]byte
	Exists  bool
}

// CleanResultPath reports whether file is a path that a result file can have: relative,
// with no .. part, and made of letters, digits and . _ / @ + - alone.
func CleanResultPath(file string) bool {
	if file == "" || path.IsAbs(file) || path.Clean(file) != file {
		return false
	}
	for _, part := range strings.Split(file, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	for _, r := range file {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._/@+-", r):
		default:
			return false
		}
	}
	return file[0] != '-'
}

// ReadResult reads the result file at file below dir, without a link on the way and
// without a file that is not a regular one, so that a file the suite's code made does
// not lead the read out of dir or block it. It returns the file's stamp and bytes; a
// file that is missing, larger than [MaxResultBytes], a link or no regular file is the
// zero Stamp and nil bytes, and not an error.
func ReadResult(dir, file string) (Stamp, []byte) {
	if !CleanResultPath(file) {
		return Stamp{}, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Stamp{}, nil
	}
	defer root.Close()
	info, err := root.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxResultBytes {
		return Stamp{}, nil
	}
	f, err := root.Open(file)
	if err != nil {
		return Stamp{}, nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxResultBytes+1))
	if err != nil || len(data) > MaxResultBytes {
		return Stamp{}, nil
	}
	return Stamp{ModTime: info.ModTime(), Sum: sha256.Sum256(data), Exists: true}, data
}

// Fresh reports whether after is a file that the suite wrote: it exists, and it is not
// the file the commit had, by modification time and content.
func Fresh(before, after Stamp) bool {
	return after.Exists && (!before.Exists || !after.ModTime.Equal(before.ModTime) || after.Sum != before.Sum)
}
