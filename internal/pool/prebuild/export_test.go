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

// SetPolicySource puts source in place of the read of the policy's source in the run's
// record until the returned func runs.
func SetPolicySource(source func(record string) string) (restore func()) {
	was := policySourceOf
	policySourceOf = source
	return func() { policySourceOf = was }
}

// PolicySource is policySource: the source of the run's policy in its record.
func PolicySource(record string) string { return policySource(record) }

// ReadResult is readResult: the step's result in out, as Launch reads it.
func ReadResult(out string) error { _, err := readResult(out); return err }
