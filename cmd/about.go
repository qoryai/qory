package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
)

// maxSubjects is how many --subject a run carries, the Forager contract's bound.
const maxSubjects = 16

// maxDetailsRead is the most --details reads, a file or stdin. The details are at most
// 8192 bytes compacted, so a larger input is refused before it is held to that.
const maxDetailsRead = 1 << 20

// aboutFrom is what the run is about, from --kind, --title, --subject and --details,
// held to Forager's rules: nil when every one is empty, so the run started event
// carries no about. detailsIn is stdin, read for --details -, and terminal says whether
// it is a terminal. Every error is an input error that names the flag.
func aboutFrom(kind, title string, subjects []string, details string, detailsIn io.Reader, terminal bool) (*session.About, error) {
	subs, err := parseSubjects(subjects)
	if err != nil {
		return nil, err
	}
	d, err := readDetails(details, detailsIn, terminal)
	if err != nil {
		return nil, err
	}
	about := &session.About{Kind: kind, Title: title, Subjects: subs, Details: d}
	if err := session.CheckAbout(about); err != nil {
		return nil, aboutInput(err, subjects, details)
	}
	if about.Empty() {
		return nil, nil
	}
	return about, nil
}

// parseSubjects reads each --subject, type=<type>,ref=<ref>[,url=<url>][,title=<title>],
// in the order given. The title takes the rest of the value, commas, = and quotes
// included, from the first ,title= on, or the whole value after a leading title=; the
// rest is split at commas into type, ref and url, each once, type and ref required. An
// empty url or title is none. Forager checks what each may hold; qory checks the
// count and that no two have the same type and ref, which it names by the values given.
func parseSubjects(values []string) ([]session.Subject, error) {
	if len(values) > maxSubjects {
		return nil, input(fmt.Errorf("--subject is given %d times; a run carries at most %d", len(values), maxSubjects))
	}
	var out []session.Subject
	for j, v := range values {
		s, err := parseSubject(v)
		if err != nil {
			return nil, err
		}
		for i, earlier := range out {
			if earlier.Type == s.Type && earlier.Ref == s.Ref {
				return nil, input(fmt.Errorf("--subject %s and --subject %s have the same type and ref", exports.Value(values[i], false), exports.Value(values[j], false)))
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// parseSubject reads one --subject value; see [parseSubjects].
func parseSubject(v string) (session.Subject, error) {
	var s session.Subject
	if v == "" {
		return s, input(fmt.Errorf("--subject is empty"))
	}
	head, titled := v, false
	if rest, ok := strings.CutPrefix(v, "title="); ok {
		head, s.Title, titled = "", rest, true
	} else if i := strings.Index(v, ",title="); i >= 0 {
		head, s.Title, titled = v[:i], v[i+len(",title="):], true
	}
	seen := map[string]bool{}
	if head != "" {
		for _, part := range strings.Split(head, ",") {
			key, value, ok := strings.Cut(part, "=")
			switch {
			case part == "":
				return s, input(fmt.Errorf("--subject %s: an empty part is not key=value", exports.Value(v, false)))
			case !ok || key == "":
				return s, input(fmt.Errorf("--subject %s: %s is not key=value", exports.Value(v, false), exports.Value(part, false)))
			case key != "type" && key != "ref" && key != "url":
				return s, input(fmt.Errorf("--subject %s: %s is not type, ref, url or title", exports.Value(v, false), accesskey.ShownName(key, false)))
			case seen[key]:
				return s, input(fmt.Errorf("--subject %s sets %s twice", exports.Value(v, false), key))
			}
			seen[key] = true
			switch key {
			case "type":
				s.Type = value
			case "ref":
				s.Ref = value
			default:
				s.URL = value
			}
		}
	}
	for _, key := range []string{"type", "ref"} {
		if seen[key] {
			continue
		}
		// A title= before the key took it into the title.
		if titled && strings.Contains(","+s.Title, ","+key+"=") {
			return s, input(fmt.Errorf("--subject %s has no %s; title= takes the rest of the value, so it comes last", exports.Value(v, false), key))
		}
		return s, input(fmt.Errorf("--subject %s has no %s", exports.Value(v, false), key))
	}
	return s, nil
}

// readDetails reads --details: none for "", stdin to its end for -, which a terminal
// cannot be, and otherwise the file it names, relative to the working directory. It
// checks the input is one JSON value of at most maxDetailsRead bytes; Forager checks
// that it is an object, its size compacted, its depth and its keys.
func readDetails(name string, stdin io.Reader, terminal bool) (json.RawMessage, error) {
	var r io.Reader
	switch name {
	case "":
		return nil, nil
	case "-":
		if terminal {
			return nil, input(fmt.Errorf("--details - reads stdin, and stdin is a terminal; pass a file, or pipe the details in"))
		}
		r = stdin
	default:
		f, err := os.Open(name)
		if err != nil {
			return nil, input(fmt.Errorf("--details %s: %w", exports.Value(name, false), unwrapPath(err)))
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxDetailsRead+1))
	if err != nil {
		return nil, input(fmt.Errorf("--details %s: %w", exports.Value(name, false), unwrapPath(err)))
	}
	if len(b) > maxDetailsRead {
		return nil, input(fmt.Errorf("--details %s is larger than 1 MiB; the details are at most 8192 bytes compacted", exports.Value(name, false)))
	}
	var d json.RawMessage
	if err := json.Unmarshal(b, &d); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, input(fmt.Errorf("--details %s is not JSON at byte %d: %w", exports.Value(name, false), syntax.Offset, err))
		}
		return nil, input(fmt.Errorf("--details %s is not JSON: %w", exports.Value(name, false), err))
	}
	return d, nil
}

// unwrapPath is the error a [fs.PathError] carries, without the operation and the path
// the line names already.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// subjectField is the field of a subject in an [session.AboutError], with its index
// and, when the error is about one member, the member.
var subjectField = regexp.MustCompile(`^about\.subjects\[(\d+)\](?:\.(type|ref|url|title))?$`)

// aboutInput is Forager's refusal of an About as the input error of the flag that
// gave the field: --kind, --title, --details with its argument, or --subject with its
// value as given and the member. Any other error passes as it is.
func aboutInput(err error, subjects []string, details string) error {
	var ae *session.AboutError
	if !errors.As(err, &ae) {
		return input(err)
	}
	switch ae.Field {
	case "about.kind":
		return input(fmt.Errorf("--kind %s", ae.Reason))
	case "about.title":
		return input(fmt.Errorf("--title %s", ae.Reason))
	case "about.details":
		return input(fmt.Errorf("--details %s %s", exports.Value(details, false), ae.Reason))
	}
	if m := subjectField.FindStringSubmatch(ae.Field); m != nil && m[2] != "" {
		if i, convErr := strconv.Atoi(m[1]); convErr == nil && i < len(subjects) {
			return input(fmt.Errorf("--subject %s: the %s %s", exports.Value(subjects[i], false), m[2], ae.Reason))
		}
	}
	return input(err)
}
