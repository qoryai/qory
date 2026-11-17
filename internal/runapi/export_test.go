package runapi

// EncodePushedCommits exposes the encoder that sizes and sends the body of a pushed-commits
// call, so that a test measures the bytes the client sends.
func EncodePushedCommits(commits []PushedCommit) ([]byte, error) {
	return encodePushedCommits(commits)
}
