package cmd

import (
	"testing"

	"github.com/qoryai/forager/wall"
)

// TestAnEmptyWallUserIsQorysUser is [oneUser] with no wall.user, which passes for an ordinary
// user and stops root. It needs no uid of this process, so it also runs as root.
func TestAnEmptyWallUserIsQorysUser(t *testing.T) {
	for uid, stops := range map[int]bool{0: true, 1000: false, 2000: false} {
		t.Cleanup(SetQoryUID(uid))
		if err := oneUser(&wall.Docker{}, false); (err != nil) != stops {
			t.Errorf("uid %d: %v, want a stop: %t", uid, err, stops)
		}
	}
}
