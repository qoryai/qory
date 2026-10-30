package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/link"
	"github.com/qoryai/forager/wall"
)

// recordSocketDirs is the pattern of the folders of Forager's record sockets, which no
// walled run mounts. Forager keeps the pattern internal: see dirPrefix and Dirs in
// session/internal/socket/socket.go of the Forager module.
var recordSocketDirs = filepath.Join(os.TempDir(), "qory-run-*")

// TestARunCodesInputsFolderIsNoneOfForagersFolders checks the temporary folder that the
// inputs of a run from a run code are written to: its path, as made and with links
// resolved, matches none of the patterns of the temporary folders Forager reserves,
// regardless of case, as Forager compares them, so the wall mounts the inputs.
func TestARunCodesInputsFolderIsNoneOfForagersFolders(t *testing.T) {
	in, remove, err := (&codeRun{inputs: []byte("{}\n")}).writeInputs()
	t.Cleanup(remove)
	if err != nil {
		t.Fatal(err)
	}
	top := filepath.Dir(in)
	paths := []string{filepath.Join(os.TempDir(), filepath.Base(top)), top}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reserved := []string{
		wall.TempDirs(),
		filepath.Join(os.TempDir(), link.ToolDirPrefix+"*"),
		filepath.Join(os.TempDir(), link.LinkDirPrefix+"*"),
		recordSocketDirs,
	}
	for _, pattern := range reserved {
		patterns := []string{pattern, filepath.Join(tmp, strings.TrimPrefix(pattern, os.TempDir()))}
		for _, p := range patterns {
			for _, path := range paths {
				matched, err := filepath.Match(strings.ToLower(p), strings.ToLower(path))
				if err != nil {
					t.Fatal(err)
				}
				if matched {
					t.Errorf("the inputs folder %s matches %s, a pattern of Forager's folders", path, p)
				}
			}
		}
	}
}
