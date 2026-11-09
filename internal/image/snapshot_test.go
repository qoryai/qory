package image_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// debianSnapshot is the default of DEBIAN_SNAPSHOT in a Dockerfile.
var debianSnapshot = regexp.MustCompile(`(?m)^ARG DEBIAN_SNAPSHOT=(\S+)$`)

// TestDebianSnapshotIsOneDate reads the three Dockerfiles that install Debian's packages
// from a snapshot: an image FROM the agent image installs from the agent image's date.
func TestDebianSnapshotIsOneDate(t *testing.T) {
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, t.TempDir())
	}

	const first = "agent"
	var want string
	for _, image := range []string{first, "agent-docker", "agent-browser"} {
		path := filepath.Join("..", "..", "images", image, "Dockerfile")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		found := debianSnapshot.FindAllSubmatch(data, -1)
		if len(found) != 1 {
			t.Fatalf("Set one default of DEBIAN_SNAPSHOT in %s. It has %d.", path, len(found))
		}
		date := string(found[0][1])
		if image == first {
			want = date
			continue
		}
		if date != want {
			t.Errorf("Set DEBIAN_SNAPSHOT in %s to %s, the date in images/%s/Dockerfile. It is %s.", path, want, first, date)
		}
	}
}
