package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/source"
)

// TestAFetchLineMasksTheSourcesQuery is the fetch of a harness source whose URL has a
// token and another query value: the fetch line prints *** for each, and so does an
// error of the fetch that quotes the other value out of its URL, since the source's
// query values join the run's mask list before the fetch.
func TestAFetchLineMasksTheSourcesQuery(t *testing.T) {
	isolate(t)
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const src = "https://git.example/acme/harness.git?token=tok-0123456789&key=fetch%2Fkey%2F42"
	var out strings.Builder
	git := proxyGit(hostProxy{}, &out)
	git.Fetching(src)
	git.Fetched(src, "main", "", strings.Repeat("a", 40), 1200*time.Millisecond)
	want := "qory run: fetched https://git.example/acme/harness.git?token=***&key=***#main at aaaaaaaaaaaa in 1.2s\n"
	if out.String() != want {
		t.Errorf("the line:\n%q\nwant\n%q", out.String(), want)
	}

	err := &source.FetchError{Source: src + "#main", URL: src, Ref: "main", Output: "fatal: the remote has no fetch/key/42", Err: errors.New("exit status 128")}
	line := (&runLineError{err}).Error()
	if strings.Contains(line, "fetch/key/42") || strings.Contains(line, "tok-0123456789") {
		t.Errorf("the error prints a query value: %s", line)
	}

	out.Reset()
	named := namedGit(hostProxy{}, &out, nil)
	named.Fetched(src, "main", "core ?Token=tok-0123456789", strings.Repeat("b", 40), time.Second)
	if strings.Contains(out.String(), "tok-0123456789") {
		t.Errorf("the named line prints the token: %s", out.String())
	}
}
