package provisioner_test

import (
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
)

// TestReadDocumentsStopsEachFixtureThatBreaksARuleOfCheck reads each documents fixture of
// run/v1 fixtures/check, documents that pass the schema and break a rule that
// [contracts.Documents.Check] checks: each is a [*provisioner.BrokenRule], whose text is
// that of documents that break the schema and whose detail is the rule. A repeated deliver
// type is named, cut to 19 characters; the other rules have one detail with no value.
func TestReadDocumentsStopsEachFixtureThatBreaksARuleOfCheck(t *testing.T) {
	names, err := fs.Glob(contracts.RunFS, "fixtures/check/documents-*.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, name := range names {
		t.Run(path.Base(name), func(t *testing.T) {
			b, err := fs.ReadFile(contracts.RunFS, name)
			if err != nil {
				t.Fatal(err)
			}
			_, err = provisioner.ReadDocuments(b)
			var broken *provisioner.BrokenRule
			if !errors.As(err, &broken) {
				t.Fatalf("the error is %v, want a broken rule", err)
			}
			if err.Error() != "it breaks documents.schema.json" {
				t.Errorf("the text is %q", err.Error())
			}
			want := "read_targets or the destinations list one entry twice"
			if strings.Contains(name, "deliver") {
				want = "deliver lists code-change twice"
			}
			if broken.Detail != want {
				t.Errorf("the detail is %q, want %q", broken.Detail, want)
			}
		})
	}
}

// TestABrokenRuleCutsALongDeliverTypeTo19Characters is documents whose deliver lists a
// type of 64 characters twice: the detail has its first 19 characters.
func TestABrokenRuleCutsALongDeliverTypeTo19Characters(t *testing.T) {
	long := strings.Repeat("abcdefgh-", 7) + "x"
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/check/documents-deliver-type-twice.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provisioner.ReadDocuments([]byte(strings.ReplaceAll(string(b), `"code-change"`, `"`+long+`"`)))
	var broken *provisioner.BrokenRule
	if !errors.As(err, &broken) || broken.Detail != "deliver lists "+long[:19]+" twice" {
		t.Errorf("the error is %v, want the detail with %s", err, long[:19])
	}
}

// TestDocumentsWithOneDeliverEntryForEachTypePass is the deliver fixture with its second
// entry of another type: Check passes it.
func TestDocumentsWithOneDeliverEntryForEachTypePass(t *testing.T) {
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/check/documents-deliver-type-twice.json")
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(b), `"code-change"`, `"pull-request"`, 1)
	if _, err := provisioner.ReadDocuments([]byte(fixed)); err != nil {
		t.Errorf("%v", err)
	}
}
