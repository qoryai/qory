package codex_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestWalledArgs is each form of a walled launch of Codex: codex exec, as a run from a run
// code starts it, gets -c sandbox_mode="danger-full-access" right after exec, also with
// --yolo and with a sandbox flag after --; a start with no exec stays as given, and
// arguments that set sandbox_mode with --config or -c, in each form, before exec or after
// it, get no pair. Each --add-dir after exec and before -- is left out, with its folder. A last argument
// after exec that starts with $<name> mentions the skill by its path in the walled home,
// also beside a sandbox_mode pair; a plugin's name, a bare $, and a $<name> that is not
// the last argument stay as given.
func TestWalledArgs(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w, ok := rt.(render.Walled)
	if !ok {
		t.Fatal("codex is no render.Walled")
	}
	off := []string{"-c", `sandbox_mode="danger-full-access"`}
	with := func(parts ...[]string) []string { return slices.Concat(parts...) }
	for _, c := range []struct{ args, want []string }{
		{[]string{"exec", "--json", "$explore x"}, with([]string{"exec"}, off, []string{"--json", "[$explore](/var/lib/qory-codex-home/skills/explore/SKILL.md) x"})},
		{[]string{"exec", "$workflow-explore"}, with([]string{"exec"}, off, []string{"[$workflow-explore](/var/lib/qory-codex-home/skills/workflow-explore/SKILL.md)"})},
		{[]string{"exec", "-c", "sandbox_mode=read-only", "$explore x"}, []string{"exec", "-c", "sandbox_mode=read-only", "[$explore](/var/lib/qory-codex-home/skills/explore/SKILL.md) x"}},
		{[]string{"exec", "$pack:review x"}, with([]string{"exec"}, off, []string{"$pack:review x"})},
		{[]string{"exec", "$ x"}, with([]string{"exec"}, off, []string{"$ x"})},
		{[]string{"exec", "$explore", "x"}, with([]string{"exec"}, off, []string{"$explore", "x"})},
		{[]string{"$explore", "exec"}, with([]string{"$explore", "exec"}, off)},
		{[]string{"--model", "m", "exec", "hi"}, with([]string{"--model", "m", "exec"}, off, []string{"hi"})},
		{[]string{"exec", "--", "--sandbox"}, with([]string{"exec"}, off, []string{"--", "--sandbox"})},
		{[]string{"exec", "--", "-c", "sandbox_mode=read-only"}, with([]string{"exec"}, off, []string{"--", "-c", "sandbox_mode=read-only"})},
		{[]string{"--model", "m"}, []string{"--model", "m"}},
		{[]string{"exec", "--config", "sandbox_mode=read-only", "hi"}, []string{"exec", "--config", "sandbox_mode=read-only", "hi"}},
		{[]string{"exec", "--config=sandbox_mode=read-only", "hi"}, []string{"exec", "--config=sandbox_mode=read-only", "hi"}},
		{[]string{"exec", "-csandbox_mode=read-only", "hi"}, []string{"exec", "-csandbox_mode=read-only", "hi"}},
		{[]string{"exec", "-c=sandbox_mode=read-only", "hi"}, []string{"exec", "-c=sandbox_mode=read-only", "hi"}},
		{[]string{"exec", "-c", " sandbox_mode = read-only", "hi"}, []string{"exec", "-c", " sandbox_mode = read-only", "hi"}},
		{[]string{"-c", "sandbox_mode=read-only", "exec", "hi"}, []string{"-c", "sandbox_mode=read-only", "exec", "hi"}},
		{[]string{"exec", "-c", "sandbox_mode", "hi"}, with([]string{"exec"}, off, []string{"-c", "sandbox_mode", "hi"})},
		{[]string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}, with([]string{"exec"}, off, []string{"--dangerously-bypass-approvals-and-sandbox", "hi"})},
		{[]string{"--yolo", "exec", "hi"}, with([]string{"--yolo", "exec"}, off, []string{"hi"})},
		{[]string{"exec", "--json", "--add-dir", "/in", "hi"}, with([]string{"exec"}, off, []string{"--json", "hi"})},
		{[]string{"exec", "--add-dir=/in", "--add-dir", "/more", "hi"}, with([]string{"exec"}, off, []string{"hi"})},
		{[]string{"--add-dir", "/in", "exec", "hi"}, with([]string{"--add-dir", "/in", "exec"}, off, []string{"hi"})},
		{[]string{"exec", "--add-dir", "--json", "hi"}, with([]string{"exec"}, off, []string{"--json", "hi"})},
		{[]string{"exec", "hi", "--", "--add-dir", "/in"}, with([]string{"exec"}, off, []string{"hi", "--", "--add-dir", "/in"})},
		{[]string{"exec", "--json", "hi"}, with([]string{"exec"}, off, []string{"--json", "hi"})},
	} {
		if got, err := w.WalledArgs(c.args); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
}

// TestWalledArgsRefuseWhatResumeDoesNotTake is each form of a flag that codex exec resume
// of Codex 0.167.2 does not take, after exec and after the prompt: each is refused with
// its text, which names the flag as given, long or short. The same flags before exec,
// which the continuation reads as the first start does, after -- and with no exec pass,
// and so do --image and -i, which exec resume takes.
func TestWalledArgsRefuseWhatResumeDoesNotTake(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"exec", "--sandbox", "read-only", "hi"}, "--sandbox"},
		{[]string{"exec", "--sandbox=read-only", "hi"}, "--sandbox"},
		{[]string{"exec", "-s", "workspace-write", "hi"}, "-s"},
		{[]string{"exec", "-sread-only", "hi"}, "-s"},
		{[]string{"exec", "-s=read-only", "hi"}, "-s"},
		{[]string{"exec", "hi", "-s", "read-only"}, "-s"},
		{[]string{"exec", "--approve-for-me", "hi"}, "--approve-for-me"},
		{[]string{"exec", "--approve-for-me=true", "hi"}, "--approve-for-me"},
		{[]string{"exec", "--not-so-yolo", "hi"}, "--not-so-yolo"},
		{[]string{"exec", "--cd", "/w", "hi"}, "--cd"},
		{[]string{"exec", "--cd=/w", "hi"}, "--cd"},
		{[]string{"exec", "-C", "/w", "hi"}, "-C"},
		{[]string{"exec", "-C/w", "hi"}, "-C"},
		{[]string{"exec", "-C=/w", "hi"}, "-C"},
		{[]string{"exec", "-m", "gpt-5", "-C", "/w", "hi"}, "-C"},
		{[]string{"exec", "--color", "never", "hi"}, "--color"},
		{[]string{"exec", "--color=never", "hi"}, "--color"},
		{[]string{"exec", "--json", "--add-dir", "/in", "--sandbox", "read-only", "--cd", "/w"}, "--sandbox"},
	} {
		got, err := w.WalledArgs(c.args)
		want := "Remove `" + c.flag + "` from the run's Codex arguments: a paused Codex run continues with `codex exec resume`, which does not take it."
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, want)
		}
	}
	for _, args := range [][]string{
		{"exec", "--", "--sandbox", "read-only", "-s", "x", "--approve-for-me", "--not-so-yolo", "--cd", "/w", "-C", "/w", "--color", "never"},
		{"--sandbox", "read-only", "-s", "x", "--approve-for-me", "--cd", "/w", "-C/w", "exec", "hi"},
		{"--color", "never"},
		{"exec", "--image", "a.png", "-i", "b.png", "hi"},
		{"exec", "--sandboxes", "hi"},
		{"exec", "--json", "--add-dir", "/in", "hi"},
	} {
		if _, err := w.WalledArgs(args); err != nil {
			t.Errorf("WalledArgs(%q): %v", args, err)
		}
	}
}

// TestAWalledLaunchResumesWithEveryArgument is the launch of a paused walled Codex run that
// continues, built as Forager builds it: qory's walled arguments, then the descriptor's
// resume arguments, resume <id>, right after exec. exec resume of Codex 0.167.2 takes
// none of --sandbox, -s, --approve-for-me, --not-so-yolo, --cd, -C, --add-dir and
// --color after resume <id>, and the launch has none of them; the sandbox setting is a
// -c pair, which exec resume takes, and the prompt follows.
func TestAWalledLaunchResumesWithEveryArgument(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	headless, ok := rt.(render.Headless).HeadlessArgs("skills", "hello", "/in/inputs.json", "/in")
	if !ok {
		t.Fatal("codex has no headless start of a skill")
	}
	walled, err := rt.(render.Walled).WalledArgs(slices.Concat([]string{"--model", "gpt-5"}, headless))
	if err != nil {
		t.Fatal(err)
	}
	at := slices.Index(walled, "exec") + 1
	resumed := slices.Concat(walled[:at], []string{"resume", "0199a213-81c0-7800-8aa1-bbab2a035a53"}, walled[at:])
	want := []string{"--model", "gpt-5", "exec", "resume", "0199a213-81c0-7800-8aa1-bbab2a035a53", "-c", `sandbox_mode="danger-full-access"`, "--json", "[$hello](/var/lib/qory-codex-home/skills/hello/SKILL.md) /in/inputs.json"}
	if !slices.Equal(resumed, want) {
		t.Errorf("the continuation's arguments %q, want %q", resumed, want)
	}
	for _, a := range resumed[at:] {
		if f := strings.SplitN(a, "=", 2)[0]; slices.Contains([]string{"--sandbox", "-s", "--approve-for-me", "--not-so-yolo", "--cd", "-C", "--add-dir", "--color"}, f) {
			t.Errorf("the continuation has %s after exec resume", a)
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
		{"exec", "--", "--oss", "--local-provider=x", "--profile", "x", "-px", "--enable", "apps", "--search", "--ignore-user-config"},
		{"exec", "--enable", "--json", "hi"},
		{"exec", "--disable=", "hi"},
		{"exec", "--config=hooks.Stop=[]", "hi"},
		{"exec", "-c", "model=gpt-5", "hi"},
		{"exec", "-c", "hooks_x=1", "hi"},
		{"exec", "-c", "model_provider", "hi"},
		{"exec", "-c", "--json", "model_provider=x"},
		{"exec", "--", "-c", "model_provider=x"},
		{"exec", "--", "--config=features.x=true"},
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

// TestWalledArgsRefuseSkippedSettingsAndSearch is --ignore-user-config and --search,
// bare and with =, before exec, after it and after the prompt: each is refused with its
// text.
func TestWalledArgsRefuseSkippedSettingsAndSearch(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	ignore := "Remove `--ignore-user-config` from the run's Codex arguments: a Codex run takes its settings from qory and Forager, not from the arguments."
	search := "Remove `--search` from the run's Codex arguments: a Codex run takes its web search setting from qory and Forager, not from the arguments."
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"exec", "--ignore-user-config", "hi"}, ignore},
		{[]string{"exec", "--ignore-user-config=true", "hi"}, ignore},
		{[]string{"--ignore-user-config", "exec", "hi"}, ignore},
		{[]string{"exec", "hi", "--ignore-user-config"}, ignore},
		{[]string{"--search"}, search},
		{[]string{"--search=true", "hi"}, search},
		{[]string{"exec", "--search", "hi"}, search},
		{[]string{"hi", "--search"}, search},
	} {
		got, err := w.WalledArgs(c.args)
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
}

// TestWalledArgsRefuseHookTrustAndRemote is --dangerously-bypass-hook-trust, --remote and
// --remote-auth-token-env before --, each in every form Codex accepts and with =, and the
// same words after --, which reach the prompt and pass.
func TestWalledArgsRefuseHookTrustAndRemote(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	hooks := "Remove `--dangerously-bypass-hook-trust` from the run's Codex arguments: a Codex run takes its hook trust from qory and Forager, not from the arguments."
	remote := "Remove `--remote` from the run's Codex arguments: a Codex run takes its app server from qory and Forager, not from the arguments."
	token := "Remove `--remote-auth-token-env` from the run's Codex arguments: a Codex run takes its app server from qory and Forager, not from the arguments."
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"exec", "--dangerously-bypass-hook-trust", "hi"}, hooks},
		{[]string{"--dangerously-bypass-hook-trust", "exec", "hi"}, hooks},
		{[]string{"--dangerously-bypass-hook-trust=true"}, hooks},
		{[]string{"--remote", "ws://example.com:4000"}, remote},
		{[]string{"--remote=unix://"}, remote},
		{[]string{"resume", "--remote", "unix:///run/app.sock"}, remote},
		{[]string{"--remote-auth-token-env", "TOKEN"}, token},
		{[]string{"--remote-auth-token-env=TOKEN", "hi"}, token},
	} {
		got, err := w.WalledArgs(c.args)
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
	for _, args := range [][]string{
		{"exec", "--", "--dangerously-bypass-hook-trust"},
		{"--", "--remote", "ws://example.com:4000"},
		{"exec", "--", "--remote-auth-token-env=TOKEN"},
		{"exec", "use --remote"},
	} {
		if _, err := w.WalledArgs(args); err != nil {
			t.Errorf("WalledArgs(%q): %v", args, err)
		}
	}
}

// TestWalledArgsRefuseSkills is a --config key at skills or cloud.skills, under one, or
// above one, as cloud is, refused with the --config text; a key beside them passes.
func TestWalledArgsRefuseSkills(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	for _, c := range []struct{ pair, key string }{
		{"skills.bundled.enabled=true", "skills.bundled.enabled"},
		{"skills.config=[]", "skills.config"},
		{"skills={}", "skills"},
		{"cloud.skills.enabled=true", "cloud.skills.enabled"},
		{"cloud.skills={}", "cloud.skills"},
		{"cloud={}", "cloud"},
	} {
		want := "Remove `--config " + c.key + "` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."
		for _, args := range [][]string{{"-c", c.pair}, {"--config=" + c.pair}, {"-c=" + c.pair}} {
			got, err := w.WalledArgs(args)
			if err == nil || err.Error() != want || got != nil {
				t.Errorf("WalledArgs(%q) = %q, %v, want %q", args, got, err, want)
			}
		}
	}
	for _, pair := range []string{"cloud.other=true", "skills_extra=true", "model_verbosity=low", "tools.web_search=false"} {
		if _, err := w.WalledArgs([]string{"-c", pair}); err != nil {
			t.Errorf("WalledArgs(-c %q): %v", pair, err)
		}
	}
}

// TestWalledArgsRefusalsEscapeControlCharacters is a refused --config key and a refused
// feature with a line end, an escape and a bell in them: the line has each as a Go
// escape, so nothing of the argument but plain text reaches the terminal.
func TestWalledArgsRefusalsEscapeControlCharacters(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w := rt.(render.Walled)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", "features.x\n\x1b[2Jy=1"}, "Remove `--config features.x\\n\\x1b[2Jy` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."},
		{[]string{"--enable", "x\ay"}, "Remove `--enable x\\ay` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."},
		{[]string{"--disable=x\ry"}, "Remove `--disable x\\ry` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."},
	} {
		_, err := w.WalledArgs(c.args)
		if err == nil || err.Error() != c.want {
			t.Errorf("WalledArgs(%q): %v, want %q", c.args, err, c.want)
		}
	}
}
