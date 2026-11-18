package compose_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// entryToken matches the name of a role or entry in a refusal, such as the reviewers of
// agents/reviewers.
var entryToken = regexp.MustCompile(`\b(?:agents|commands|skills)/([a-z][a-z0-9-]*)`)

// TestRefusalsKeepTheFixLineShortForLongNames is every fixture that expects a refusal, run
// again with each of its module names and each role or entry name its refusal prints
// renamed to a name of 90 characters, in every file and path of a copy: the refusal, when
// there still is one, has a first line of at most 100 characters, and when that line has a
// long name shortened, a later line has the whole name.
func TestRefusalsKeepTheFixLineShortForLongNames(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "*", "expected", "error.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no expected refusals under %s: %v", fixtures, err)
	}
	for _, file := range files {
		dir := filepath.Dir(filepath.Dir(file))
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, m := range entryToken.FindAllStringSubmatch(string(want), -1) {
				names[m[1]] = true
			}
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.IsDir() && filepath.Base(filepath.Dir(path)) == "modules" {
					names[d.Name()] = true
				}
				return nil
			})
			copyDir := realTemp(t)
			copyFixture(t, name, copyDir)
			long := map[string]string{}
			for n := range names {
				long[n] = n + "-" + strings.Repeat("x", 90-len(n)-1)
				rename(t, copyDir, n, long[n])
			}
			if listsHooks(t, copyDir) {
				commitAll(t, copyDir)
			}
			t.Chdir(copyDir)
			checkout := ""
			if listsHooks(t, copyDir) {
				checkout = copyDir
			}
			_, err = load(checkout)
			if err == nil {
				return
			}
			lines := strings.Split(err.Error(), "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, " ") {
					continue
				}
				if n := utf8.RuneCountInString(line); n > 100 {
					t.Errorf("line %d is a fix line of %d characters: %s", i+1, n, line)
				}
			}
			for _, l := range long {
				if strings.Contains(lines[0], exports.Short(l)) && !strings.Contains(strings.Join(lines[1:], "\n"), l) {
					t.Errorf("the first line has %s shortened, and no later line has it whole:\n%s", exports.Short(l), err)
				}
			}
		})
	}
}

// TestTheFixLineOfANoteHasAtMost98CharactersWithNamesOf24Characters is a note in a skill
// and a module whose names have 24 characters, the longest that no refusal shortens: line 1
// of each refusal of a note has at most 98 characters, so at most 100 under the "✗ " that
// qory prints before it, and the whole names are on a later line when line 1 has them
// shortened.
func TestTheFixLineOfANoteHasAtMost98CharactersWithNamesOf24Characters(t *testing.T) {
	skill, mod := "python-naming-rules-0001", "technology-python-0001"
	mod += strings.Repeat("x", 24-len(mod))
	if len(skill) != 24 || len(mod) != 24 {
		t.Fatalf("names have %d and %d characters", len(skill), len(mod))
	}
	for _, c := range []struct{ name, front string }{
		{"a role no agent fills", "notes_for: tester\npaths: [\"*.py\"]\n"},
		{"a notes_for that is no role", "notes_for: {a: b}\npaths: [\"*.py\"]\n"},
		{"no paths", "notes_for: developer\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTree(t, map[string]string{
				stack.FileName:                                      "apiVersion: qory.dev/v1alpha1\nbind:\n  agents:\n    developer: dev\nmodules:\n  - name: " + mod + "\n    source:\n      path: modules/" + mod + "\n",
				"modules/" + mod + "/agents/dev.md":                 "---\nname: dev\ndescription: d\n---\n\nWrites.\n",
				"modules/" + mod + "/skills/" + skill + "/SKILL.md": "---\nname: " + skill + "\ndescription: s\n" + c.front + "---\n\nNotes.\n",
			})
			if err == nil {
				t.Fatal("composed without an error")
			}
			lines := strings.Split(err.Error(), "\n")
			if n := utf8.RuneCountInString(lines[0]); n > 98 {
				t.Errorf("line 1 has %d characters, want at most 98: %s", n, lines[0])
			}
			rest := strings.Join(lines[1:], "\n")
			for _, name := range []string{skill, mod} {
				if !strings.Contains(lines[0], name) && !strings.Contains(rest, name) {
					t.Errorf("line 1 has %s shortened, and no later line has it whole:\n%s", name, err)
				}
			}
		})
	}
}

// rename replaces the word old with new in every file under dir, and renames each file or
// folder that old names, deepest first.
func rename(t *testing.T, dir, old, new string) {
	t.Helper()
	word := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(old) + `($|[^A-Za-z0-9_-])`)
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		paths = append(paths, path)
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(data)
			for {
				next := word.ReplaceAllString(text, "${1}"+new+"${2}")
				if next == text {
					break
				}
				text = next
			}
			if text != string(data) {
				return os.WriteFile(path, []byte(text), 0o644)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if base := filepath.Base(paths[i]); base == old || strings.TrimSuffix(base, filepath.Ext(base)) == old && filepath.Ext(base) != "" {
			if err := os.Rename(paths[i], filepath.Join(filepath.Dir(paths[i]), strings.Replace(base, old, new, 1))); err != nil {
				t.Fatal(err)
			}
		}
	}
}
