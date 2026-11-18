package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The submit program contract of the integrations repository, contracts/run/v1/submit-program/
// README.md, in one place: the names of the inputs that qory sets, the keys of the lines that
// the program prints, the exit statuses, and the limits. A change of a name in the contract is
// a change here.
const (
	submitEnvPrefix          = "QORY_SUBMIT_"
	submitEnvSystem          = "QORY_SUBMIT_SYSTEM"
	submitEnvPath            = "QORY_SUBMIT_PATH"
	submitEnvBranch          = "QORY_SUBMIT_BRANCH"
	submitEnvBaseBranch      = "QORY_SUBMIT_BASE_BRANCH"
	submitEnvBaseCommit      = "QORY_SUBMIT_BASE_COMMIT"
	submitEnvCommits         = "QORY_SUBMIT_COMMITS"
	submitEnvDraft           = "QORY_SUBMIT_DRAFT"
	submitEnvTitle           = "QORY_SUBMIT_TITLE"
	submitEnvDescriptionFile = "QORY_SUBMIT_DESCRIPTION_FILE"
)

// The values of QORY_SUBMIT_DRAFT.
const (
	submitDraftOpen = "open"
	submitDraftNone = "none"
)

// The keys of the lines that the program prints on its standard output.
const (
	submitKeyNumber    = "draft_number"
	submitKeyURL       = "draft_url"
	submitKeyTitle     = "draft_title"
	submitKeyState     = "draft_state"
	submitKeyAction    = "draft_action"
	submitKeyCommitURL = "commit_url"
)

// The exit statuses of the program that qory reads: 0 it finished, 1 a call to the forge
// failed, 2 it could not use its inputs; qory reads its lines after these. 3 means the person has
// no login of the forge's command line: the program prints the login command as the first line
// of its standard error, and qory reads no line. After any other status, a signal, a time-out,
// or when the program did not start, qory reads no line.
const (
	submitExitDone    = 0
	submitExitForge   = 1
	submitExitInput   = 2
	submitExitNoLogin = 3
)

// The limits of the contract: commits in the one input, characters of a title, bytes of an
// address, and characters of a description.
const (
	maxSubmitCommits = 100
	maxSubmitTitle   = 256
	maxSubmitURL     = 2048
)

// submitInput is what qory gives the program for one repository, as the contract's inputs.
type submitInput struct {
	System, Path string
	// Branch is the branch the run pushed, BaseBranch the branch the draft is based on, "" when
	// qory did not read it, and BaseCommit the commit the run started at.
	Branch, BaseBranch, BaseCommit string
	// Commits is each commit of the run, oldest first, the head last.
	Commits []string
	// Draft is one of the submitDraft values. Title and DescriptionFile are set unless Draft is
	// submitDraftNone.
	Draft           string
	Title           string
	DescriptionFile string
}

// env is the inputs as the environment of the program, over the person's environment base:
// every variable of base with the prefix of the contract's inputs is dropped first, so a value
// set in the person's shell never reaches the program.
func (in submitInput) env(base []string) []string {
	out := slices.DeleteFunc(slices.Clone(base), func(kv string) bool { return strings.HasPrefix(kv, submitEnvPrefix) })
	set := func(name, value string) { out = append(out, name+"="+value) }
	set(submitEnvSystem, in.System)
	set(submitEnvPath, in.Path)
	set(submitEnvBranch, in.Branch)
	if in.BaseBranch != "" {
		set(submitEnvBaseBranch, in.BaseBranch)
	}
	set(submitEnvBaseCommit, in.BaseCommit)
	set(submitEnvCommits, strings.Join(in.Commits, " "))
	set(submitEnvDraft, in.Draft)
	if in.Draft != submitDraftNone {
		set(submitEnvTitle, in.Title)
		set(submitEnvDescriptionFile, in.DescriptionFile)
	}
	return out
}

// submitLine is a line of the program's output that qory leaves out, with its number from 1 and
// the reason.
type submitLine struct {
	N      int
	Reason string
}

// submitLines is what qory records from the lines of the program: the draft, nil when the five
// draft lines are not all there once and valid, the address of each commit, and the lines left
// out.
type submitLines struct {
	Draft      *pullRequest
	CommitURLs map[string]string
	Invalid    []submitLine
}

var (
	submitNumberPattern = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
)

// submitDraftKeys are the keys of the five lines that make a draft.
var submitDraftKeys = []string{submitKeyNumber, submitKeyURL, submitKeyTitle, submitKeyState, submitKeyAction}

// parseSubmitLines reads the standard output of the program for a repository on system, and the
// commits that qory gave it, the base among them. draft is the value that qory gave the program
// in QORY_SUBMIT_DRAFT: with submitDraftNone every draft line is left out. qory strips one
// carriage return at the end of each line first. A line that is not valid is left out and the
// lines around it count; a blank line is left out and is not listed. The draft is set only when
// the five lines are there and valid.
func parseSubmitLines(output, system, draft string, commits []string) submitLines {
	res := submitLines{CommitURLs: map[string]string{}}
	values, at := map[string]string{}, map[string]int{}
	bad := func(n int, reason string) { res.Invalid = append(res.Invalid, submitLine{n, reason}) }
	lines := strings.Split(output, "\n")
	last := len(lines) - 1
	if lines[last] != "" {
		bad(last+1, "the line has no line feed at its end")
	}
	for i, line := range lines[:last] {
		n := i + 1
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if strings.ContainsFunc(line, isControl) {
			bad(n, "the line has a control character")
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok || value == "" {
			bad(n, "the line is not a key and a value")
			continue
		}
		if key == submitKeyCommitURL {
			commit, addr, ok := strings.Cut(value, " ")
			switch {
			case !ok:
				bad(n, "the line has no address")
			case !slices.Contains(commits, commit):
				bad(n, "the commit is not one that qory gave the program")
			case res.CommitURLs[commit] != "":
				bad(n, "the commit has an address already")
			default:
				if err := submitAddress(addr, system); err != nil {
					bad(n, err.Error())
					continue
				}
				res.CommitURLs[commit] = addr
			}
			continue
		}
		if draft == submitDraftNone && slices.Contains(submitDraftKeys, key) {
			bad(n, submitEnvDraft+" is none, so qory records no draft")
			continue
		}
		if err := submitDraftValue(key, value, system); err != nil {
			bad(n, err.Error())
			continue
		}
		if _, dup := values[key]; dup {
			bad(n, "the key is there already")
			continue
		}
		values[key], at[key] = value, n
	}
	switch {
	case len(values) == len(submitDraftKeys):
		if (values[submitKeyState] == "closed" || values[submitKeyState] == "merged") && values[submitKeyAction] != "kept" {
			bad(at[submitKeyAction], "a draft that is closed or merged has the action kept")
			break
		}
		number, _ := strconv.Atoi(values[submitKeyNumber])
		res.Draft = &pullRequest{Number: number, URL: values[submitKeyURL], Title: values[submitKeyTitle],
			State: values[submitKeyState], Action: values[submitKeyAction]}
	case len(values) > 0:
		var missing []string
		for _, key := range submitDraftKeys {
			if _, ok := values[key]; !ok {
				missing = append(missing, key)
			}
		}
		for _, n := range at {
			bad(n, "the draft needs five lines, and "+strings.Join(missing, ", ")+" is missing; the line is left out")
		}
	}
	slices.SortFunc(res.Invalid, func(a, b submitLine) int { return a.N - b.N })
	return res
}

// submitDraftValue checks the value of one of the five draft keys.
func submitDraftValue(key, value, system string) error {
	switch key {
	case submitKeyNumber:
		if !submitNumberPattern.MatchString(value) {
			return errors.New("the number is not a whole number of 1 to 9 digits, 1 or more")
		}
	case submitKeyURL:
		return submitAddress(value, system)
	case submitKeyTitle:
		if !utf8.ValidString(value) {
			return errors.New("the title is not UTF-8")
		}
		if n := utf8.RuneCountInString(value); n > maxSubmitTitle {
			return fmt.Errorf("the title has %d characters, over the limit of %d", n, maxSubmitTitle)
		}
		if !plainTitle(value) {
			return errors.New("the title has a control, bidirectional or invisible character, or only spaces")
		}
	case submitKeyState:
		if !slices.Contains([]string{"draft", "open", "closed", "merged"}, value) {
			return errors.New("the state is none of draft, open, closed and merged")
		}
	case submitKeyAction:
		if value != "opened" && value != "updated" && value != "kept" {
			return errors.New("the action is none of opened, updated and kept")
		}
	default:
		return errors.New("the key is not one of the contract's")
	}
	return nil
}

// plainTitle reports whether title has no control, bidirectional control, line or paragraph
// separator or invisible format character, and one character that is not a space. It is the
// rule of $defs.draft_title in the contract's definitions.schema.json, as Unicode's classes
// give it.
func plainTitle(title string) bool {
	visible := false
	for _, r := range title {
		switch {
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r), r == '\u2028', r == '\u2029':
			return false
		case !slices.Contains(titleSpaces, r):
			visible = true
		}
	}
	return visible
}

// titleSpaces are the characters that do not count as a character of a title.
var titleSpaces = []rune{' ', '\u00a0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
	'\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000'}

// submitAddress checks that addr is an https address of at most [maxSubmitURL] characters on
// system, with a path and with no user information, port, query or fragment, of ASCII
// characters that RFC 3986 allows and with a lower-case scheme. The contract states the same as
// a pattern in definitions.schema.json, $defs.url, which qory does not have until it pins the
// contract.
func submitAddress(addr, system string) error {
	if len(addr) > maxSubmitURL {
		return fmt.Errorf("the address is over %d characters", maxSubmitURL)
	}
	if !strings.HasPrefix(addr, "https://") || !uriCharacters(addr) {
		return errors.New("the address is not an https URL as definitions.schema.json describes it")
	}
	u, err := url.Parse(addr)
	switch {
	case err != nil:
		return errors.New("the address is not a URL")
	case u.User != nil:
		return errors.New("the address is not an https URL as definitions.schema.json describes it")
	case u.Port() != "" || strings.HasSuffix(u.Host, ":"):
		return errors.New("the address has a port")
	case u.RawQuery != "" || u.ForceQuery || strings.Contains(addr, "#"):
		return errors.New("the address has a query or a fragment")
	case !strings.EqualFold(u.Hostname(), system):
		return errors.New("the address is not on the repository's system")
	case u.Path == "" || u.Path == "/":
		return errors.New("the address has no path")
	}
	return nil
}

// uriCharacters reports whether s has only the characters that RFC 3986 allows, every % being
// the start of a percent-encoding.
func uriCharacters(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("-._~:/?#[]@!$&'()*+,;=", c) >= 0:
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// isControl reports whether r is a control character: U+0000 to U+001F or U+007F.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
