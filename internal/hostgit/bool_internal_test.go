package hostgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitTrueReadsABooleanAsGitDoes asks git config --type=bool for each value, in a
// file of the test's: gitTrue is true where git says true, and false where git says
// false. A value git refuses is either: git then runs nothing, and which values it
// refuses differs by its version and its C library.
func TestGitTrueReadsABooleanAsGitDoes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	for _, value := range []string{
		"true", "Yes", "ON", "false", "no", "off", "", "1", "0", "-1", "+1", " 1",
		"1k", "2m", "1G", "1kk", "1x", "0x10", "0X0", "0x", "010", "09", "0b1", "0B10", "0b", "0b2", "-0b1", "1_0",
		"99999999999999999999", "8g", "-0", "2147483647", "2147483648", "-2147483648", "-2147483649", "2g", "-2g",
	} {
		if err := os.WriteFile(file, []byte("[x]\n\ty = \""+value+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("git", "config", "--file", file, "--type=bool", "--get", "x.y").Output()
		if err != nil {
			continue
		}
		want := strings.TrimSpace(string(out)) == "true"
		if got := gitTrue(value); got != want {
			t.Errorf("gitTrue(%q) = %v; git says %q, %v", value, got, strings.TrimSpace(string(out)), err)
		}
	}
}

// TestGitTrueReadsABinaryNumber pins the 0b numbers a git on glibc reads, whatever git
// runs the test: a git that refuses them runs nothing, so taking them as git on glibc
// does leaves nothing out.
func TestGitTrueReadsABinaryNumber(t *testing.T) {
	for value, want := range map[string]bool{"0b1": true, "0B10": true, "-0b1": true, "0b0": false, "0b": false, "0b2": false} {
		if got := gitTrue(value); got != want {
			t.Errorf("gitTrue(%q) = %v, want %v", value, got, want)
		}
	}
}
