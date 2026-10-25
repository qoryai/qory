package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestEnrolmentCodeFileReads is gateway.server.enrolment_code_file read as written,
// resolved under forager.yaml's directory, and listed by qory config when set; a server
// without it lists no row, and an empty value is an error that starts with its fix.
func TestEnrolmentCodeFileReads(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, "gateway:\n  server:\n    url: https://apiary.example\n    enrolment_code_file: enrolment/code\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if r.Server.EnrolmentCodeFile != "enrolment/code" {
		t.Fatalf("read %q", r.Server.EnrolmentCodeFile)
	}
	if got, want := r.Path(r.Server.EnrolmentCodeFile), filepath.Join(filepath.Dir(path), "enrolment", "code"); got != want {
		t.Errorf("resolves to %s, want %s", got, want)
	}
	if row := rowsOf(r)["gateway.server.enrolment_code_file"]; row.Value != "enrolment/code" || row.Origin != path {
		t.Errorf("row %+v", row)
	}
	foragerFile(t, "gateway:\n  server:\n    url: https://apiary.example\n")
	if r, err = config.LoadForager(); err != nil {
		t.Fatal(err)
	}
	if _, ok := rowsOf(r)["gateway.server.enrolment_code_file"]; ok || r.Server.EnrolmentCodeFile != "" {
		t.Errorf("a server without the setting reads %q and lists its row", r.Server.EnrolmentCodeFile)
	}
	foragerFile(t, "gateway:\n  server:\n    url: https://apiary.example\n    enrolment_code_file: \"\"\n")
	_, err = config.LoadForager()
	if want := path + ": set gateway.server.enrolment_code_file to the path of the gateway's enrolment code, or remove it: the value is empty"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}

// TestTheForagerSchemaTakesAnEnrolmentCodeFile holds forager.schema.json to what the
// reader takes of gateway.server.enrolment_code_file: a path, never empty.
func TestTheForagerSchemaTakesAnEnrolmentCodeFile(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"gateway: {server: {url: \"https://qory.example\", enrolment_code_file: /run/qory/enrolment/code}}\n", true},
		{"gateway: {server: {url: \"https://qory.example\", enrolment_code_file: \"\"}}\n", false},
		{"gateway: {server: {url: \"https://qory.example\", enrolment_code_file: [code]}}\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}

// TestCheckWritersHoldsAFileAndItsFolders is the rule on a file of the user's in a
// folder of the user's: it passes; once every user, or the folder's group, may write the
// folder, the folder's path is in an error that starts with its fix, as root too.
func TestCheckWritersHoldsAFileAndItsFolders(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "code")
	if err := os.WriteFile(file, []byte("x\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.CheckWriters("gateway.server.enrolment_code_file", file); err != nil {
		t.Fatalf("a file of the user's alone: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err = config.CheckWriters("gateway.server.enrolment_code_file", file)
	if want := "remove the write permission of other users from " + dir + ": every user may write it, and only root and the user running qory may write gateway.server.enrolment_code_file and its folders, except a folder that root owns with the sticky bit set"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	err = config.CheckWriters("gateway.server.enrolment_code_file", file)
	if want := "remove the group's write permission from " + dir + ": the group "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("a folder its group may write: %v, want %q", err, want)
	}
	if err := config.CheckWriters("gateway.server.enrolment_code_file", filepath.Join(dir, "none")); err == nil || !strings.Contains(err.Error(), "none") {
		t.Errorf("a missing file: %v", err)
	}
}
