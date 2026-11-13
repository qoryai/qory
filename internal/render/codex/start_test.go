package codex_test

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestWalledArgs is each form of a walled launch of Codex: codex exec, as a run from a run
// code starts it, gets --sandbox danger-full-access right after exec, also with a sandbox
// flag after --; a start with no exec, and arguments that set a sandbox mode, bypass one
// or ask for --approve-for-me, before exec or after it, stay as given. The prompt
// $<name> stays as given.
func TestWalledArgs(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w, ok := rt.(render.Walled)
	if !ok {
		t.Fatal("codex is no render.Walled")
	}
	for _, c := range []struct{ args, want []string }{
		{[]string{"exec", "--json", "$explore x"}, []string{"exec", "--sandbox", "danger-full-access", "--json", "$explore x"}},
		{[]string{"exec", "--sandbox", "read-only", "$explore x"}, []string{"exec", "--sandbox", "read-only", "$explore x"}},
		{[]string{"$explore", "exec"}, []string{"$explore", "exec", "--sandbox", "danger-full-access"}},
		{[]string{"--model", "m", "exec", "hi"}, []string{"--model", "m", "exec", "--sandbox", "danger-full-access", "hi"}},
		{[]string{"exec", "--", "--sandbox"}, []string{"exec", "--sandbox", "danger-full-access", "--", "--sandbox"}},
		{[]string{"--model", "m"}, []string{"--model", "m"}},
		{[]string{"exec", "--sandbox", "read-only", "hi"}, []string{"exec", "--sandbox", "read-only", "hi"}},
		{[]string{"exec", "-s", "workspace-write", "hi"}, []string{"exec", "-s", "workspace-write", "hi"}},
		{[]string{"exec", "--sandbox=read-only", "hi"}, []string{"exec", "--sandbox=read-only", "hi"}},
		{[]string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}, []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}},
		{[]string{"exec", "--yolo", "hi"}, []string{"exec", "--yolo", "hi"}},
		{[]string{"exec", "-sread-only", "hi"}, []string{"exec", "-sread-only", "hi"}},
		{[]string{"exec", "-s=read-only", "hi"}, []string{"exec", "-s=read-only", "hi"}},
		{[]string{"--sandbox", "read-only", "exec", "hi"}, []string{"--sandbox", "read-only", "exec", "hi"}},
		{[]string{"-s", "read-only", "exec", "hi"}, []string{"-s", "read-only", "exec", "hi"}},
		{[]string{"--yolo", "exec", "hi"}, []string{"--yolo", "exec", "hi"}},
		{[]string{"exec", "--approve-for-me", "hi"}, []string{"exec", "--approve-for-me", "hi"}},
		{[]string{"exec", "--not-so-yolo", "hi"}, []string{"exec", "--not-so-yolo", "hi"}},
		{[]string{"--approve-for-me", "exec", "hi"}, []string{"--approve-for-me", "exec", "hi"}},
		{[]string{"exec", "--search", "hi"}, []string{"exec", "--sandbox", "danger-full-access", "--search", "hi"}},
	} {
		if got, err := w.WalledArgs(c.args); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
}

// TestWalledArgsRefuseAReservedConfigKey is each form of --config and -c that sets a key
// Forager or qory sets for a walled run: the key itself, a key under it, a table above
// it, model_reasoning_effort, and use_legacy_landlock, which Codex reads as
// features.use_legacy_landlock. Each is refused with the key as given, also before exec
// and with spaces around the key. An allowed key, a reserved key after --, a flag with
// no = and a -c whose value Codex refuses pass.
func TestWalledArgsRefuseAReservedConfigKey(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	for _, c := range []struct {
		args []string
		key  string
	}{
		{[]string{"exec", "--config", "model_provider=x", "hi"}, "model_provider"},
		{[]string{"exec", "--config=model_provider=x", "hi"}, "model_provider"},
		{[]string{"exec", "-c", "model_provider=x", "hi"}, "model_provider"},
		{[]string{"exec", "-cmodel_provider=x", "hi"}, "model_provider"},
		{[]string{"exec", "-c=model_provider=x", "hi"}, "model_provider"},
		{[]string{"-c", "model_provider=x", "exec", "hi"}, "model_provider"},
		{[]string{"exec", "hi", "-c", "model_provider=x"}, "model_provider"},
		{[]string{"-c", "model_provider=x"}, "model_provider"},
		{[]string{"exec", "-c", " model_provider = x", "hi"}, "model_provider"},
		{[]string{"exec", "-c", "model_providers.x.base_url=\"https://example.com\"", "hi"}, "model_providers.x.base_url"},
		{[]string{"exec", "-c", "model_providers.x={}", "hi"}, "model_providers.x"},
		{[]string{"exec", "-c", "features.shell_snapshot=true", "hi"}, "features.shell_snapshot"},
		{[]string{"exec", "-c", "features={shell_snapshot=true}", "hi"}, "features"},
		{[]string{"exec", "-c", "hooks={state={}}", "hi"}, "hooks"},
		{[]string{"exec", "-c", "hooks.state.x=1", "hi"}, "hooks.state.x"},
		{[]string{"exec", "-c", "experimental_realtime_ws_base_url=x", "hi"}, "experimental_realtime_ws_base_url"},
		{[]string{"exec", "-c", "model_reasoning_effort=high", "hi"}, "model_reasoning_effort"},
		{[]string{"exec", "-c", "use_legacy_landlock=true", "hi"}, "use_legacy_landlock"},
		{[]string{"exec", "-c", "model_verbosity=low", "-c", "web_search=live", "hi"}, "web_search"},
	} {
		got, err := w.WalledArgs(c.args)
		want := "Remove `--config " + c.key + "` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, want)
		}
	}
	for _, args := range [][]string{
		{"exec", "-c", "model_verbosity=low", "hi"},
		{"exec", "-m", "gpt-5", "-C", "/w", "hi"},
		{"exec", "--", "--oss", "--local-provider=x", "--profile", "x", "-px", "--enable", "apps"},
		{"exec", "--enable", "--json", "hi"},
		{"exec", "--disable=", "hi"},
		{"exec", "--config=hooks.Stop=[]", "hi"},
		{"exec", "-c", "model=gpt-5", "hi"},
		{"exec", "-c", "hooks_x=1", "hi"},
		{"exec", "-c", "model_provider", "hi"},
		{"exec", "-c", "--json", "model_provider=x"},
		{"exec", "--", "-c", "model_provider=x"},
		{"exec", "--", "--config=features.x=true"},
		{"exec", "-C", "model_provider=x"},
		{"--model", "m"},
	} {
		if _, err := w.WalledArgs(args); err != nil {
			t.Errorf("WalledArgs(%q): %v", args, err)
		}
	}
}

// TestWalledArgsRefuseAProviderOrAProfile is each form of --oss, --local-provider and
// --profile, before exec, after it and after the prompt: each is refused with its text,
// which writes -p as --profile. The first such argument is the one refused.
func TestWalledArgsRefuseAProviderOrAProfile(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	provider := func(flag string) string {
		return "Remove `" + flag + "` from the run's Codex arguments: a Codex run takes its model provider from qory and Forager, not from the arguments."
	}
	profile := "Remove `--profile` from the run's Codex arguments: a Codex run takes its settings from qory and Forager, not from a profile."
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"exec", "--oss", "hi"}, provider("--oss")},
		{[]string{"--oss", "exec", "hi"}, provider("--oss")},
		{[]string{"--oss"}, provider("--oss")},
		{[]string{"exec", "--local-provider", "ollama", "hi"}, provider("--local-provider")},
		{[]string{"exec", "--local-provider=ollama", "hi"}, provider("--local-provider")},
		{[]string{"exec", "hi", "--local-provider", "ollama"}, provider("--local-provider")},
		{[]string{"exec", "--profile", "x", "hi"}, profile},
		{[]string{"exec", "--profile=x", "hi"}, profile},
		{[]string{"exec", "-p", "x", "hi"}, profile},
		{[]string{"exec", "-px", "hi"}, profile},
		{[]string{"exec", "-p=x", "hi"}, profile},
		{[]string{"-p", "x", "exec", "hi"}, profile},
		{[]string{"-p", "x"}, profile},
		{[]string{"exec", "--profile", "x", "--oss", "hi"}, profile},
		{[]string{"exec", "--oss", "-c", "model_provider=x", "hi"}, provider("--oss")},
	} {
		got, err := w.WalledArgs(c.args)
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
}

// TestWalledArgsRefuseAFeature is each form of --enable and --disable, before exec, after
// it, repeated, and with the legacy name connectors: each is refused in the --config
// form of the flag, with the first such argument named.
func TestWalledArgsRefuseAFeature(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"exec", "--enable", "shell_snapshot", "hi"}, "--enable shell_snapshot"},
		{[]string{"exec", "--enable=shell_snapshot", "hi"}, "--enable shell_snapshot"},
		{[]string{"exec", "--disable", "plugins", "hi"}, "--disable plugins"},
		{[]string{"exec", "--disable=plugins", "hi"}, "--disable plugins"},
		{[]string{"--enable", "apps", "exec", "hi"}, "--enable apps"},
		{[]string{"exec", "--enable", "connectors", "hi"}, "--enable connectors"},
		{[]string{"exec", "--enable", "a", "--enable", "b", "hi"}, "--enable a"},
		{[]string{"exec", "hi", "--disable", "apps"}, "--disable apps"},
		{[]string{"--enable", "apps"}, "--enable apps"},
	} {
		want := "Remove `" + c.flag + "` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."
		got, err := w.WalledArgs(c.args)
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, want)
		}
	}
}
