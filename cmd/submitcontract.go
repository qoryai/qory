package cmd

import (
	"slices"
	"strings"

	"github.com/qoryai/integrations/contracts"
)

// qoryEnvPrefix starts the name of each variable that qory sets for itself. The names of the
// submit program's inputs, its output keys and its exit statuses are the exported ones of
// [contracts] in contracts/submit_program.go of the integrations module that go.mod pins.
const qoryEnvPrefix = "QORY_"

// maxSubmitCommits is the most commits in the program's one input. The pinned package does not
// export it, so a pin that changes it is read in the README's table of inputs.
const maxSubmitCommits = 100

// submitInput is what qory gives the program for one repository, as the contract's inputs.
type submitInput struct {
	System, Path string
	// Branch is the branch the run pushed, BaseBranch the branch the draft is based on, "" when
	// qory did not read it, and BaseCommit the commit the run started at.
	Branch, BaseBranch, BaseCommit string
	// Commits is each commit of the run, oldest first, the head last.
	Commits []string
	// Draft is [contracts.DraftOpen] or [contracts.SubmitDraftNone]. Title and DescriptionFile
	// are set unless Draft is [contracts.SubmitDraftNone].
	Draft           string
	Title           string
	DescriptionFile string
}

// env is the inputs as the environment of the program, over the person's environment base:
// every variable of base that starts with QORY_ is dropped first, so no access key, server
// secret, run key or status descriptor of qory, and no value of the contract's inputs that the
// person's shell has, reaches the program. The person's other variables stay: the forge login
// (GH_TOKEN, HOME), SSH_AUTH_SOCK and the proxy variables. The program never prompts: git and gh
// get GIT_TERMINAL_PROMPT=0 and GH_PROMPT_DISABLED=1.
func (in submitInput) env(base []string) []string {
	out := slices.DeleteFunc(slices.Clone(base), func(kv string) bool {
		return strings.HasPrefix(kv, qoryEnvPrefix) || strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") || strings.HasPrefix(kv, "GH_PROMPT_DISABLED=")
	})
	set := func(name, value string) { out = append(out, name+"="+value) }
	set("GIT_TERMINAL_PROMPT", "0")
	set("GH_PROMPT_DISABLED", "1")
	set(contracts.SubmitEnvSystem, in.System)
	set(contracts.SubmitEnvPath, in.Path)
	set(contracts.SubmitEnvBranch, in.Branch)
	if in.BaseBranch != "" {
		set(contracts.SubmitEnvBaseBranch, in.BaseBranch)
	}
	set(contracts.SubmitEnvBaseCommit, in.BaseCommit)
	set(contracts.SubmitEnvCommits, strings.Join(in.Commits, " "))
	set(contracts.SubmitEnvDraft, in.Draft)
	if in.Draft != contracts.SubmitDraftNone {
		set(contracts.SubmitEnvTitle, in.Title)
		set(contracts.SubmitEnvDescriptionFile, in.DescriptionFile)
	}
	return out
}
