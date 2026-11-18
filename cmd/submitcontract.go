package cmd

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
	submitEnvDraftNumber     = "QORY_SUBMIT_DRAFT_NUMBER"
	submitEnvTitle           = "QORY_SUBMIT_TITLE"
	submitEnvPreviousTitle   = "QORY_SUBMIT_PREVIOUS_TITLE"
	submitEnvDescriptionFile = "QORY_SUBMIT_DESCRIPTION_FILE"
)

// The values of QORY_SUBMIT_DRAFT.
const (
	submitDraftOpen   = "open"
	submitDraftUpdate = "update"
	submitDraftNone   = "none"
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

// The exit statuses of the program that qory reads its lines after: 0 it finished, 1 a call
// to the forge failed, 2 it could not use its inputs. Any other status records nothing.
//
// submitExitNoLogin is not in the contract: it is the status that qory reads as "the forge's
// command line has no login", which the contract has no word for. The contract's README has to
// state it, or qory has to read the lack of a login another way.
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
	// Draft is one of the submitDraft values. Number is the draft to update, for submitDraftUpdate
	// alone. Title and DescriptionFile are set unless Draft is submitDraftNone. PreviousTitle is
	// "" for none.
	Draft           string
	Number          int
	Title           string
	PreviousTitle   string
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
	if in.Draft == submitDraftUpdate {
		set(submitEnvDraftNumber, strconv.Itoa(in.Number))
	}
	if in.Draft != submitDraftNone {
		set(submitEnvTitle, in.Title)
		set(submitEnvDescriptionFile, in.DescriptionFile)
	}
	if in.PreviousTitle != "" {
		set(submitEnvPreviousTitle, in.PreviousTitle)
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

// parseSubmitLines reads the standard output of the program for a repository on system, and
// the commits that qory gave it, the base among them. A line that is not valid is left out and
// the lines around it count; a blank line is left out and is not listed.
func parseSubmitLines(output, system string, commits []string) submitLines {
	res := submitLines{CommitURLs: map[string]string{}}
	draft := map[string]string{}
	lines := strings.Split(output, "\n")
	last := len(lines) - 1
	if lines[last] != "" {
		res.Invalid = append(res.Invalid, submitLine{last + 1, "the line has no line feed at its end"})
	}
	for i, line := range lines[:last] {
		n := i + 1
		if line == "" {
			continue
		}
		if strings.ContainsFunc(line, isControl) {
			res.Invalid = append(res.Invalid, submitLine{n, "the line has a control character"})
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok || value == "" {
			res.Invalid = append(res.Invalid, submitLine{n, "the line is not a key and a value"})
			continue
		}
		if key == submitKeyCommitURL {
			commit, addr, ok := strings.Cut(value, " ")
			switch {
			case !ok:
				res.Invalid = append(res.Invalid, submitLine{n, "the line has no address"})
			case !slices.Contains(commits, commit):
				res.Invalid = append(res.Invalid, submitLine{n, "the commit is not one that qory gave the program"})
			case res.CommitURLs[commit] != "":
				res.Invalid = append(res.Invalid, submitLine{n, "the commit has an address already"})
			default:
				if err := submitAddress(addr, system); err != nil {
					res.Invalid = append(res.Invalid, submitLine{n, err.Error()})
					continue
				}
				res.CommitURLs[commit] = addr
			}
			continue
		}
		if err := submitDraftValue(key, value, system); err != nil {
			res.Invalid = append(res.Invalid, submitLine{n, err.Error()})
			continue
		}
		if _, dup := draft[key]; dup {
			res.Invalid = append(res.Invalid, submitLine{n, "the key is there already"})
			continue
		}
		draft[key] = value
	}
	if len(draft) == 5 {
		number, _ := strconv.Atoi(draft[submitKeyNumber])
		res.Draft = &pullRequest{Number: number, URL: draft[submitKeyURL], Title: draft[submitKeyTitle],
			State: draft[submitKeyState], Action: draft[submitKeyAction]}
	}
	return res
}

// submitDraftValue checks the value of one of the five draft keys.
func submitDraftValue(key, value, system string) error {
	switch key {
	case submitKeyNumber:
		if !submitNumberPattern.MatchString(value) {
			return fmt.Errorf("the number is not a whole number of 1 to 9 digits, 1 or more")
		}
	case submitKeyURL:
		return submitAddress(value, system)
	case submitKeyTitle:
		if n := utf8.RuneCountInString(value); n > maxSubmitTitle {
			return fmt.Errorf("the title has %d characters, over the limit of %d", n, maxSubmitTitle)
		}
	case submitKeyState:
		if value != "draft" && value != "open" {
			return fmt.Errorf("the state is neither draft nor open")
		}
	case submitKeyAction:
		if value != "opened" && value != "updated" && value != "kept" {
			return fmt.Errorf("the action is none of opened, updated and kept")
		}
	default:
		return fmt.Errorf("the key is not one of the contract's")
	}
	return nil
}

// submitAddress checks that addr is an https address of at most [maxSubmitURL] characters on
// system, with a path and with no user information, port, query or fragment.
func submitAddress(addr, system string) error {
	if len(addr) > maxSubmitURL {
		return fmt.Errorf("the address is over %d characters", maxSubmitURL)
	}
	u, err := url.Parse(addr)
	switch {
	case err != nil || strings.ContainsAny(addr, " \t"):
		return fmt.Errorf("the address is not a URL")
	case u.Scheme != "https":
		return fmt.Errorf("the address is not https")
	case u.User != nil:
		return fmt.Errorf("the address has user information")
	case u.Port() != "":
		return fmt.Errorf("the address has a port")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(addr, "#"):
		return fmt.Errorf("the address has a query or a fragment")
	case !strings.EqualFold(u.Hostname(), system):
		return fmt.Errorf("the address is not on the repository's system")
	case u.Path == "" || u.Path == "/":
		return fmt.Errorf("the address has no path")
	}
	return nil
}

// isControl reports whether r is a control character: U+0000 to U+001F or U+007F.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
