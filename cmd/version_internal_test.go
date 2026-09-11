package cmd

import "testing"

// TestStampedReadsWhatGoRecorded pins how the version Go stamps into a source build is
// read: the tag without its v, a pseudo-version as it is, "(devel)" and "" as no version,
// and the +dirty metadata as the dirty mark rather than part of the version.
func TestStampedReadsWhatGoRecorded(t *testing.T) {
	tests := []struct {
		in      string
		version string
		dirty   bool
	}{
		{"", "", false},
		{"(devel)", "", false},
		{"v0.2.1", "0.2.1", false},
		{"v0.2.1+dirty", "0.2.1", true},
		{"v0.2.2-0.20260911000000-abc123456789", "0.2.2-0.20260911000000-abc123456789", false},
		{"v0.2.2-0.20260911000000-abc123456789+dirty", "0.2.2-0.20260911000000-abc123456789", true},
	}
	for _, tc := range tests {
		version, dirty := stamped(tc.in)
		if version != tc.version || dirty != tc.dirty {
			t.Errorf("stamped(%q) = %q, %v; want %q, %v", tc.in, version, dirty, tc.version, tc.dirty)
		}
	}
}

// TestPseudoTellsAPseudoVersionFromATag pins that a pseudo-version is one with or without
// its build metadata, and that a tag, even one with a long patch number, is not one.
func TestPseudoTellsAPseudoVersionFromATag(t *testing.T) {
	tests := map[string]bool{
		"v0.2.2-0.20260911000000-abc123456789":       true,
		"0.2.2-0.20260911000000-abc123456789":        true,
		"v0.2.2-0.20260911000000-abc123456789+dirty": true,
		"v0.0.0-20260911000000-abc123456789":         true,
		"v0.3.0-rc.1.0.20260911000000-abc123456789":  true,
		"v1.0.20":      false,
		"1.0.20+dirty": false,
		"0.3.0-rc.1":   false,
	}
	for in, want := range tests {
		if got := pseudo(in); got != want {
			t.Errorf("pseudo(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestTitleNamesTheVersionOrTheCommit pins the title line: the version when there is
// one, else dev and the commit, marked dirty, else dev alone.
func TestTitleNamesTheVersionOrTheCommit(t *testing.T) {
	tests := []struct {
		b    buildInfo
		want string
	}{
		{buildInfo{Version: "0.3.0", Commit: "abc123456789", Dirty: true}, "0.3.0"},
		{buildInfo{Commit: "abc123456789"}, "dev abc123456789"},
		{buildInfo{Commit: "abc123456789", Dirty: true}, "dev abc123456789-dirty"},
		{buildInfo{}, "dev"},
	}
	for _, tc := range tests {
		if got := tc.b.title(); got != tc.want {
			t.Errorf("%+v: title = %q, want %q", tc.b, got, tc.want)
		}
	}
}
