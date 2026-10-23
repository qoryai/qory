package prebuild

import "time"

// SetFetchTimeout puts d in place of the fetch's time limit until the returned func
// runs.
func SetFetchTimeout(d time.Duration) (restore func()) {
	was := fetchTimeout
	fetchTimeout = d
	return func() { fetchTimeout = was }
}

// SetManifestTimeout puts d in place of the time limit of each read of a manifest
// until the returned func runs.
func SetManifestTimeout(d time.Duration) (restore func()) {
	was := manifestTimeout
	manifestTimeout = d
	return func() { manifestTimeout = was }
}

// CheckRun is checkRun: the check of the settings and the run before anything is made.
func CheckRun(s Settings, r Run) (string, error) { return checkRun(s, r) }
