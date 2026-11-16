package runtimeinstall

// SetLimits sets the most bytes of a tarball, of its program and of all it unpacks to,
// until the returned restore runs.
func SetLimits(tarball, program, unpacked int64) (restore func()) {
	t, p, u := maxTarball, maxProgram, maxUnpacked
	maxTarball, maxProgram, maxUnpacked = tarball, program, unpacked
	return func() { maxTarball, maxProgram, maxUnpacked = t, p, u }
}
