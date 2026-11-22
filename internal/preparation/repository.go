package preparation

// branchPrefix starts the name of every work item's branch.
const branchPrefix = "qory/"

// WorkItemRef is the name of the branch of the work item with the id workItemID.
func WorkItemRef(workItemID string) string { return branchPrefix + workItemID }

// Repository is one repository of a work item's branch, with the commit of the branch in
// it: where a run starts and, after [Push], what the push moved.
type Repository struct {
	// System is the host the repository is on, such as github.com, and Path the
	// repository's path on the host, such as acme/web.
	System, Path string
	// Commit is the head of the branch: a full commit id, as [CommitID] has it, in lower case.
	Commit string
	// Base is the commit the run started at, nil when the run pushed nothing.
	Base *Base
	// Commits are the commits from Base to Commit, oldest first.
	Commits []Commit
}

// Base is the commit a run started at in a repository, with the name of the repository's
// default branch when qory fetched it: the branch a pull request of the run is based on.
type Base struct {
	Commit, Branch string
}

// Commit is one commit that a run pushed.
type Commit struct {
	Commit string
}
