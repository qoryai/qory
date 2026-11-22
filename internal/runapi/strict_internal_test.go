package runapi

import (
	"reflect"
	"strings"
	"testing"
)

// TestEveryAnswerMemberIsReadByExactCase walks each type that qory decodes from an answer
// of the run API, through its structs, pointers, slices and maps: every exported field
// has a json tag with case:strict, or the tag "-". A field without one is read in any
// letter case, and the test names its path from the answer type, such as
// runapi.Snapshot.Stage.Rounds.
func TestEveryAnswerMemberIsReadByExactCase(t *testing.T) {
	seen := map[reflect.Type]bool{}
	// walk checks typ, reached at path.
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(typ.Elem(), path)
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag != "-" && !strings.Contains(","+tag+",", ",case:strict,") {
				t.Errorf("%s.%s has the json tag %q, want case:strict", path, f.Name, tag)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	for _, v := range []any{
		Snapshot{}, VersionRef{}, PushedEntry{}, Input{}, Output{}, GivenVerdict{}, Index{},
		Artifact{}, Version{}, Writer{}, File{}, Approval{}, Pushed{}, Point{}, Track{},
		errorBody{}, storedAnswer{}, answerHead{},
	} {
		walk(reflect.TypeOf(v), reflect.TypeOf(v).String())
	}
}
