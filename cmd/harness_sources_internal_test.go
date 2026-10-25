package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/compose"
)

// TestHarnessSourcesListsTheGitSources is the list a session records of a compose: the
// base first when it is a git source, then each module's git source in stack order, each
// source once, with the password of a URL's user part hidden; path sources are left out,
// and a compose with no git source lists nothing.
func TestHarnessSourcesListsTheGitSources(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 40)
	res := &compose.Result{
		Base: &compose.Base{Source: "https://git.example/org/base#main stack software", Commit: a},
		Modules: []compose.Module{
			{Source: "https://git.example/org/base#main module core", Commit: a},
			{Source: "./local", Pin: "working-tree"},
			{Source: "https://user:token@git.example/org/tools#v2:mods/lint", Commit: b},
			{Source: "https://user:token@git.example/org/tools#v2:mods/lint", Commit: b},
			{Source: "https://git.example/org/more#" + c, Commit: c},
		},
	}
	want := []session.HarnessSource{
		{Source: "https://git.example/org/base#main stack software", Commit: a},
		{Source: "https://git.example/org/base#main module core", Commit: a},
		{Source: "https://user:(hidden)@git.example/org/tools#v2:mods/lint", Commit: b},
		{Source: "https://git.example/org/more#" + c, Commit: c},
	}
	if got := harnessSources(res, nil); !slices.Equal(got, want) {
		t.Errorf("the list is %v\nwant %v", got, want)
	}
	paths := &compose.Result{Base: &compose.Base{Source: "../base", Pin: "working-tree"}, Modules: []compose.Module{{Source: "./local", Pin: "working-tree"}}}
	if got := harnessSources(paths, nil); got != nil {
		t.Errorf("a compose of paths lists %v", got)
	}
}
