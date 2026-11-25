package hostgit_test

import (
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// TestSettingsAreEachEntryWithItsValue: an entry with no value, with an empty one, with one of
// two lines, and the two entries of one key are each a setting, in the order of the list.
func TestSettingsAreEachEntryWithItsValue(t *testing.T) {
	want := []hostgit.Setting{
		{Key: "core.bare"},
		{Key: "core.bare", HasValue: true},
		{Key: "branch.a=b.merge", Value: "x\ny", HasValue: true},
		{Key: "core.bare", Value: " = ", HasValue: true},
	}
	got := hostgit.Settings("core.bare\x00core.bare\n\x00branch.a=b.merge\nx\ny\x00core.bare\n = \x00")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the settings are %+v, want %+v", got, want)
	}
	if got := hostgit.Settings(""); got != nil {
		t.Errorf("the settings of no list are %+v", got)
	}
}

// TestWrittenIsTheKeyWithItsValue: extensions.refstorage counts with one of its two values alone,
// and each other key counts with any value and with none.
func TestWrittenIsTheKeyWithItsValue(t *testing.T) {
	for _, value := range []string{"files", "reftable"} {
		if !hostgit.Written(hostgit.Setting{Key: "extensions.refstorage", Value: value, HasValue: true}) {
			t.Errorf("refstorage %q does not count", value)
		}
		if hostgit.Written(hostgit.Setting{Key: "extensions.refstorage", Value: value}) {
			t.Errorf("refstorage %q with no value counts", value)
		}
		if hostgit.Written(hostgit.Setting{Key: "extensions.x.refstorage", Value: value, HasValue: true}) {
			t.Errorf("refstorage %q in a subsection counts", value)
		}
	}
	for _, value := range []string{"", "other", "Files", "REFTABLE", "files:other", "reftable:other", "files ", "files\n", "filesx", "reftable,files"} {
		if hostgit.Written(hostgit.Setting{Key: "extensions.refstorage", Value: value, HasValue: true}) {
			t.Errorf("refstorage %q counts", value)
		}
	}
	for _, key := range []string{"core.bare", "core.repositoryformatversion", "extensions.objectformat", "remote.a.b.fetch", "branch.x.qory-base"} {
		if !hostgit.Written(hostgit.Setting{Key: key}) || !hostgit.Written(hostgit.Setting{Key: key, Value: "x", HasValue: true}) {
			t.Errorf("%s does not count", key)
		}
	}
	for _, key := range []string{"core", "core.x", "remote.fetch", "remote..fetch", "filter.x.clean"} {
		if hostgit.Written(hostgit.Setting{Key: key, Value: "x", HasValue: true}) {
			t.Errorf("%s counts", key)
		}
	}
}
