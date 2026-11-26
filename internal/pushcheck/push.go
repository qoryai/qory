package pushcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/qoryai/forager/session"
)

// errAmbiguous is the error of a push body that JSON readers can read in more than one
// way: a member twice in one object, or a member whose name differs from one of the push
// only in case, which encoding/json would take for it.
var errAmbiguous = errors.New("the push has a member twice, or one whose name differs from a member of the push only in case")

// The members of a push that the check reads, and of each of its files.
var (
	pushMembers = []string{"type", "name", "note", "withdraw", "files"}
	fileMembers = []string{"filename", "content_type", "content_base64"}
)

// readPush reads body, a POST of /artifacts as the run API reads it: its type, whether it
// is a withdrawal, and its files with their names and decoded contents. A withdrawal, a
// push with `withdraw` true, may leave out `files` or send null. A body that is not a push
// in that form is an error, whose text says which part is not; a body whose members can
// be read in more than one way is [errAmbiguous].
func readPush(body []byte) (artifactType string, files []File, withdraw bool, err error) {
	top, err := members(body, pushMembers)
	if err != nil {
		return "", nil, false, err
	}
	if err := json.Unmarshal(top["type"], &artifactType); err != nil {
		return "", nil, false, errors.New("type is missing or not a string")
	}
	if raw, ok := top["withdraw"]; ok {
		if err := json.Unmarshal(raw, &withdraw); err != nil {
			return "", nil, false, errors.New("withdraw is not true or false")
		}
	}
	var items []json.RawMessage
	raw, present := top["files"]
	if !withdraw || (present && string(bytes.TrimSpace(raw)) != "null") {
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", nil, false, errors.New("files is missing or not a list")
		}
	}
	files = make([]File, 0, len(items))
	for i, item := range items {
		m, err := members(item, fileMembers)
		if errors.Is(err, errAmbiguous) {
			return "", nil, false, err
		}
		if err != nil {
			return "", nil, false, fmt.Errorf("files[%d]: %w", i, err)
		}
		var f File
		if err := json.Unmarshal(m["filename"], &f.Name); err != nil {
			return "", nil, false, fmt.Errorf("files[%d].filename is missing or not a string", i)
		}
		if err := json.Unmarshal(m["content_base64"], &f.Content); err != nil {
			return "", nil, false, fmt.Errorf("files[%d].content_base64 is missing or not base64", i)
		}
		files = append(files, f)
	}
	return artifactType, files, withdraw, nil
}

// members are the members of the JSON object raw by name. A name twice, or a name that
// differs from one of known only in case, is [errAmbiguous]; a raw that is not one object
// is another error. A member that is absent is nil.
func members(raw []byte, known []string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("it is not a JSON object")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, notJSON(err)
		}
		name, ok := t.(string)
		if !ok {
			return nil, errors.New("the object has a name that is not a string")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, notJSON(err)
		}
		if _, twice := out[name]; twice {
			return nil, errAmbiguous
		}
		for _, k := range known {
			if name != k && strings.EqualFold(name, k) {
				return nil, errAmbiguous
			}
		}
		out[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, notJSON(err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("it has more after the JSON object")
	}
	return out, nil
}

// notJSON is the error of a decoder's err: a value that is not JSON.
func notJSON(err error) error {
	return fmt.Errorf("it is not JSON: %w", err)
}

// errPathSpelling is the error of a POST whose path spells /artifacts or /verdict in
// another way than the path itself.
var errPathSpelling = errors.New("the path has a trailing or doubled slash, a percent-escape or a query")

// postKind is what a POST to the run API is, as [readRunAPIPost] reads it.
type postKind int

const (
	// postOther is a POST that is neither a verdict nor a push of artifacts.
	postOther postKind = iota
	// postVerdict is a POST of /verdict.
	postVerdict
	// postPush is a POST of /artifacts.
	postPush
)

// runAPIPost is a POST to the run API as [readRunAPIPost] reads it.
type runAPIPost struct {
	kind postKind
	// document is the members of the body of a verdict, by name.
	document map[string]json.RawMessage
	// artifactType, files and withdraw are the type, the files and the withdrawal of a push.
	artifactType string
	files        []File
	withdraw     bool
}

// readRunAPIPost is the one strict read of a POST to the run API, for every rule of
// [session.Run.SetRunAPICheck] that depends on what the POST is. It reads req's path as
// the run API's gateway does: the gateway sends on as a verdict, or as a push, only the
// exact path /verdict or /artifacts, and Forager decodes each segment of the path and
// drops a query before it passes the path here.
//
// The kind of the result is [postVerdict] for a path that is /verdict, [postPush] for
// /artifacts, and [postOther] for every other path. For a verdict, document has the
// members of the body, which [members] reads; for a push, artifactType, files and
// withdraw are those that [readPush] reads. No field is set when err is not nil, but kind
// is, so a caller whose rule applies to a kind stops a POST that cannot be read.
//
// err is not nil when the POST cannot be read in one way: a member twice in a body, or a
// member of another case ([errAmbiguous]); a body that is no JSON object, or no push in
// the run API's form; or a path that spells /verdict or /artifacts with a trailing or
// doubled slash, a percent-escape or a query ([errPathSpelling]), which a program that
// reads the path in another way than the gateway would take for a verdict or a push. A
// caller stops such a POST with [unreadPush] when a rule applies to it, and lets the
// POST pass when none does. A POST of another path is never read.
func readRunAPIPost(req session.RunAPIRequest) (runAPIPost, error) {
	kind := postOther
	var parts []string
	path, _, _ := strings.Cut(req.Path, "?")
	path, _, _ = strings.Cut(path, "#")
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			// The gateway reads no path with a segment that does not decode.
			return runAPIPost{}, nil
		}
		if decoded != "" {
			parts = append(parts, decoded)
		}
	}
	if len(parts) == 1 {
		switch parts[0] {
		case "verdict":
			kind = postVerdict
		case "artifacts":
			kind = postPush
		}
	}
	post := runAPIPost{kind: kind}
	var err error
	switch {
	case kind == postOther:
		return post, nil
	case req.Path != "/"+parts[0]:
		return post, errPathSpelling
	case kind == postVerdict:
		post.document, err = members(req.Body, verdictMembers)
	default:
		post.artifactType, post.files, post.withdraw, err = readPush(req.Body)
	}
	return post, err
}

// The first line and the cause of the failure for a POST whose path is another spelling
// of /verdict or /artifacts.
const (
	textPathFix   = "Send the POST to the run's /verdict or /artifacts path."
	textPathCause = "The path of the POST is another spelling of it."
)

// unreadPush is the failure for a POST that [readRunAPIPost] could not read, with err as
// its error: the same refusal for every rule that applies to the POST.
func unreadPush(err error) *Failure {
	if errors.Is(err, errPathSpelling) {
		return failureThen(textPathFix, "", textPathCause)
	}
	if errors.Is(err, errAmbiguous) {
		return failure(textMembers, "")
	}
	return failure(textNoPush, fmt.Sprintf(textNoPushCause, cut(cleanLine(strings.TrimSuffix(err.Error(), ".")))))
}
