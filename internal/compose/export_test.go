package compose

// SetManifestHooks sets the functions the compose runs with each module's directory just
// before and just after it reads the module's manifest, until the test ends.
func SetManifestHooks(t interface{ Cleanup(func()) }, before, after func(dir string)) {
	beforeManifest, afterManifest = before, after
	t.Cleanup(func() { beforeManifest, afterManifest = nil, nil })
}
