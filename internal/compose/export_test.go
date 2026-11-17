package compose

// SetManifestHooks sets the functions the compose runs with each module's directory just
// before and just after it reads the module's manifest, until the test ends.
func SetManifestHooks(t interface{ Cleanup(func()) }, before, after func(dir string)) {
	beforeManifest, afterManifest = before, after
	t.Cleanup(func() { beforeManifest, afterManifest = nil, nil })
}

// AndList and OrList are the lists a refusal makes of names.
var (
	AndList = andList
	OrList  = orList
)

// SetMergeHook sets the function the compose runs with the path of each settings fragment
// just before it reads the fragment to merge it, until the test ends.
func SetMergeHook(t interface{ Cleanup(func()) }, before func(path string)) {
	beforeMerge = before
	t.Cleanup(func() { beforeMerge = nil })
}
