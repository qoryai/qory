package cmd

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runapi"
)

func TestReviewerRuntimeEscalation(t *testing.T) {
	if p := reviewerRuntimeEscalation("claude"); p != nil {
		t.Errorf("claude has the point %+v, want none", p)
	}
	for _, name := range []string{"codex", "gemini", "", "claude "} {
		p := reviewerRuntimeEscalation(name)
		if p == nil {
			t.Errorf("runtime %q has no point", name)
			continue
		}
		if p.Action != runapi.ActionRunAgain || p.Question != "" || !strings.Contains(p.Fix, "claude") || !strings.Contains(p.Text, "only on claude") || len(p.Text) > 500 {
			t.Errorf("runtime %q: point = %+v", name, p)
		}
		if strings.ContainsAny(p.Text, "\n\r") {
			t.Errorf("runtime %q: the point's text has a line break", name)
		}
	}
}

func TestReviewerRuntimesStartHeadless(t *testing.T) {
	for _, name := range reviewerRuntimes {
		rt, err := render.Lookup(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := rt.(render.Headless); !ok {
			t.Errorf("%s has no headless start", name)
		}
	}
}
