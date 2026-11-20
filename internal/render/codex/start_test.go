package codex_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestWalledArgs is each form of a walled launch of Codex: codex exec, as a run from a run
// code starts it, gets --sandbox danger-full-access right after exec, also with a sandbox
// flag after --; a start with no exec, and arguments that set a sandbox mode, bypass one
// or ask for --approve-for-me, before exec or after it, stay as given. A last argument
// after exec that starts with $<name> of a skill of the composed home mentions the skill
// by its path in the walled home, also beside a sandbox flag; a $<name> that is no skill
// there, such as $HOME, a plugin's name, a bare $, and a $<name> that is not the last
// argument stay as given.
func TestWalledArgs(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w, ok := rt.(render.Walled)
	if !ok {
		t.Fatal("codex is no render.Walled")
	}
	home := t.TempDir()
	for _, name := range []string{"explore", "workflow-explore"} {
		p := filepath.Join(home, "codex", "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ args, want []string }{
		{[]string{"exec", "--json", "$explore x"}, []string{"exec", "--sandbox", "danger-full-access", "--json", "[$explore](/var/lib/qory-codex-home/skills/explore/SKILL.md) x"}},
		{[]string{"exec", "$workflow-explore"}, []string{"exec", "--sandbox", "danger-full-access", "[$workflow-explore](/var/lib/qory-codex-home/skills/workflow-explore/SKILL.md)"}},
		{[]string{"exec", "--sandbox", "read-only", "$explore x"}, []string{"exec", "--sandbox", "read-only", "[$explore](/var/lib/qory-codex-home/skills/explore/SKILL.md) x"}},
		{[]string{"exec", "$pack:review x"}, []string{"exec", "--sandbox", "danger-full-access", "$pack:review x"}},
		{[]string{"exec", "$ x"}, []string{"exec", "--sandbox", "danger-full-access", "$ x"}},
		{[]string{"exec", "$HOME is unset"}, []string{"exec", "--sandbox", "danger-full-access", "$HOME is unset"}},
		{[]string{"exec", "$review x"}, []string{"exec", "--sandbox", "danger-full-access", "$review x"}},
		{[]string{"exec", "$explore", "x"}, []string{"exec", "--sandbox", "danger-full-access", "$explore", "x"}},
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
		{[]string{"exec", "--json", "hi"}, []string{"exec", "--sandbox", "danger-full-access", "--json", "hi"}},
	} {
		if got, err := w.WalledArgs(c.args, home); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
}

// TestWalledArgsRefuseAReservedConfigKey is each form of --config and -c that sets a key
// qory reserves in a walled run: the key itself, a key under it, a table above it,
// model_reasoning_effort, use_legacy_landlock, which Codex reads as
// features.use_legacy_landlock, and a key under hooks. Each is refused with the key as
// given, also before exec and with spaces around the key; a table above a reserved key,
// such as feedback, with the reserved key it contains. An allowed key, a reserved key
// after --, a flag with no = and a -c whose value Codex rejects with an error pass.
func TestWalledArgsRefuseAReservedConfigKey(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w, home := rt.(render.Walled), ""
	for _, c := range []struct {
		args      []string
		flag, key string
	}{
		{[]string{"exec", "--config", "model_provider=x", "hi"}, "--config", "model_provider"},
		{[]string{"exec", "--config=model_provider=x", "hi"}, "--config", "model_provider"},
		{[]string{"exec", "-c", "model_provider=x", "hi"}, "-c", "model_provider"},
		{[]string{"exec", "-cmodel_provider=x", "hi"}, "-c", "model_provider"},
		{[]string{"exec", "-c=model_provider=x", "hi"}, "-c", "model_provider"},
		{[]string{"-c", "model_provider=x", "exec", "hi"}, "-c", "model_provider"},
		{[]string{"exec", "hi", "-c", "model_provider=x"}, "-c", "model_provider"},
		{[]string{"-c", "model_provider=x"}, "-c", "model_provider"},
		{[]string{"exec", "-c", " model_provider = x", "hi"}, "-c", "model_provider"},
		{[]string{"exec", "-c", "model_providers.x.base_url=\"https://example.com\"", "hi"}, "-c", "model_providers.x.base_url"},
		{[]string{"exec", "-c", "model_providers.x={}", "hi"}, "-c", "model_providers.x"},
		{[]string{"exec", "-c", "features.shell_snapshot=true", "hi"}, "-c", "features.shell_snapshot"},
		{[]string{"exec", "-c", "features={shell_snapshot=true}", "hi"}, "-c", "features"},
		{[]string{"exec", "-c", "hooks={state={}}", "hi"}, "-c", "hooks"},
		{[]string{"exec", "-c", "hooks.state.x=1", "hi"}, "-c", "hooks.state.x"},
		{[]string{"exec", "--config=hooks.Stop=[]", "hi"}, "--config", "hooks.Stop"},
		{[]string{"exec", "-c", "experimental_realtime_ws_base_url=x", "hi"}, "-c", "experimental_realtime_ws_base_url"},
		{[]string{"exec", "-c", "model_reasoning_effort=high", "hi"}, "-c", "model_reasoning_effort"},
		{[]string{"exec", "-c", "use_legacy_landlock=true", "hi"}, "-c", "use_legacy_landlock"},
		{[]string{"exec", "-c", "model_verbosity=low", "-c", "web_search=live", "hi"}, "-c", "web_search"},
	} {
		got, err := w.WalledArgs(c.args, home)
		want := "Remove the `" + c.flag + "` flag and its value from the run's Codex arguments.\n  In this run, qory reserves `" + c.key + "`; the run takes no value for it from the arguments."
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, want)
		}
	}
	want := "Remove the `-c` flag and its value from the run's Codex arguments.\n  `feedback` is a table that contains `feedback.enabled`, which qory reserves in this run."
	if _, err := w.WalledArgs([]string{"exec", "-c", "feedback={}", "hi"}, home); err == nil || err.Error() != want {
		t.Errorf("WalledArgs(-c feedback={}): %v, want %q", err, want)
	}
	for _, args := range [][]string{
		{"exec", "-c", "model_verbosity=low", "hi"},
		{"exec", "-m", "gpt-5", "-C", "/w", "hi"},
		{"exec", "--", "--oss", "--local-provider=x", "--profile", "x", "-px", "--enable", "apps", "--search", "--ignore-user-config"},
		{"exec", "--enable", "--json", "hi"},
		{"exec", "--disable=", "hi"},
		{"exec", "-c", "model=gpt-5", "hi"},
		{"exec", "-c", "hooks_x=1", "hi"},
		{"exec", "-c", "model_provider", "hi"},
		{"exec", "-c", "--json", "model_provider=x"},
		{"exec", "--", "-c", "model_provider=x"},
		{"exec", "--", "--config=features.x=true"},
		{"exec", "-C", "model_provider=x"},
		{"--model", "m"},
	} {
		if _, err := w.WalledArgs(args, home); err != nil {
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
	w, home := rt.(render.Walled), ""
	provider := func(flag string) string {
		return "Remove `" + flag + "` from the run's Codex arguments.\n  A Codex run takes its model provider from qory and Forager, not from the arguments."
	}
	profile := "Remove `--profile` from the run's Codex arguments.\n  A Codex run takes its settings from qory and Forager, not from a profile."
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
		got, err := w.WalledArgs(c.args, home)
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, c.want)
		}
	}
}

// TestWalledArgsRefuseAFeature is each form of --enable and --disable, before exec, after
// it, repeated, and with the legacy name connectors: each is refused with the flag and the
// first such feature, as one a walled run takes from no argument. A feature of the hook
// trust is refused as the key features.<feature> that qory reserves in every run.
func TestWalledArgsRefuseAFeature(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w, home := rt.(render.Walled), ""
	for _, c := range []struct {
		args          []string
		flag, feature string
	}{
		{[]string{"exec", "--enable", "shell_snapshot", "hi"}, "--enable", "shell_snapshot"},
		{[]string{"exec", "--enable=shell_snapshot", "hi"}, "--enable", "shell_snapshot"},
		{[]string{"exec", "--disable", "plugins", "hi"}, "--disable", "plugins"},
		{[]string{"exec", "--disable=plugins", "hi"}, "--disable", "plugins"},
		{[]string{"--enable", "apps", "exec", "hi"}, "--enable", "apps"},
		{[]string{"exec", "--enable", "connectors", "hi"}, "--enable", "connectors"},
		{[]string{"exec", "--enable", "a", "--enable", "b", "hi"}, "--enable", "a"},
		{[]string{"exec", "hi", "--disable", "apps"}, "--disable", "apps"},
		{[]string{"--enable", "apps"}, "--enable", "apps"},
	} {
		want := "Remove the `" + c.flag + "` flag and its value from the run's Codex arguments.\n  A walled Codex run takes no feature from its arguments, `" + c.feature + "` included."
		got, err := w.WalledArgs(c.args, home)
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("WalledArgs(%q) = %q, %v, want %q", c.args, got, err, want)
		}
	}
	for _, c := range []struct {
		args      []string
		flag, key string
	}{
		{[]string{"exec", "--enable", "hooks", "hi"}, "--enable", "features.hooks"},
		{[]string{"--disable=unified_exec_tty"}, "--disable", "features.unified_exec_tty"},
	} {
		want := "Remove the `" + c.flag + "` flag and its value from the run's Codex arguments.\n  In this run, qory reserves `" + c.key + "`; the run takes no value for it from the arguments."
		if _, err := w.WalledArgs(c.args, home); err == nil || err.Error() != want {
			t.Errorf("WalledArgs(%q): %v, want %q", c.args, err, want)
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
	w, home := rt.(render.Walled), ""
	ignore := "Remove `--ignore-user-config` from the run's Codex arguments.\n  A Codex run takes its settings from qory and Forager, not from the arguments."
	search := "Remove `--search` from the run's Codex arguments.\n  A Codex run takes its web search setting from qory and Forager, not from the arguments."
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
		got, err := w.WalledArgs(c.args, home)
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
	w, home := rt.(render.Walled), ""
	hooks := "Remove `--dangerously-bypass-hook-trust` from the run's Codex arguments.\n  A Codex run takes its hook trust from qory and Forager, not from the arguments."
	remote := "Remove `--remote` from the run's Codex arguments.\n  A Codex run takes its app server from qory and Forager, not from the arguments."
	token := "Remove `--remote-auth-token-env` from the run's Codex arguments.\n  A Codex run takes its app server from qory and Forager, not from the arguments."
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
		got, err := w.WalledArgs(c.args, home)
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
		if _, err := w.WalledArgs(args, home); err != nil {
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
	w, home := rt.(render.Walled), ""
	for _, c := range []struct{ pair, key, cause string }{
		{"skills.bundled.enabled=true", "skills.bundled.enabled", ""},
		{"skills.config=[]", "skills.config", ""},
		{"skills={}", "skills", ""},
		{"cloud.skills.enabled=true", "cloud.skills.enabled", ""},
		{"cloud.skills={}", "cloud.skills", ""},
		{"cloud={}", "cloud", "  `cloud` is a table that contains `cloud.skills`, which qory reserves in this run."},
	} {
		if c.cause == "" {
			c.cause = "  In this run, qory reserves `" + c.key + "`; the run takes no value for it from the arguments."
		}
		for _, f := range []struct {
			flag string
			args []string
		}{{"-c", []string{"-c", c.pair}}, {"--config", []string{"--config=" + c.pair}}, {"-c", []string{"-c=" + c.pair}}} {
			want := "Remove the `" + f.flag + "` flag and its value from the run's Codex arguments.\n" + c.cause
			got, err := w.WalledArgs(f.args, home)
			if err == nil || err.Error() != want || got != nil {
				t.Errorf("WalledArgs(%q) = %q, %v, want %q", f.args, got, err, want)
			}
		}
	}
	for _, pair := range []string{"cloud.other=true", "skills_extra=true", "model_verbosity=low", "tools.web_search=false"} {
		if _, err := w.WalledArgs([]string{"-c", pair}, home); err != nil {
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
	w, home := rt.(render.Walled), ""
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", "features.x\n\x1b[2Jy=1"}, "Remove the `-c` flag and its value from the run's Codex arguments.\n  In this run, qory reserves `features.x\\n\\x1b[2Jy`; the run takes no value for it from the arguments."},
		{[]string{"--enable", "x\ay"}, "Remove the `--enable` flag and its value from the run's Codex arguments.\n  A walled Codex run takes no feature from its arguments, `x\\ay` included."},
		{[]string{"--disable=x\ry"}, "Remove the `--disable` flag and its value from the run's Codex arguments.\n  A walled Codex run takes no feature from its arguments, `x\\ry` included."},
	} {
		_, err := w.WalledArgs(c.args, home)
		if err == nil || err.Error() != c.want {
			t.Errorf("WalledArgs(%q): %v, want %q", c.args, err, c.want)
		}
	}
}

// TestArgsRefusalsKeepALongKeyOffLineOne is a refused key of 200 characters with -c and
// with --config, and a refused feature of 200 characters with --enable and --disable, in a
// run without a wall and in a walled run: line 1 of the text has the flag as typed and is
// at most 100 characters behind "qory run: ", and the cause line has the whole key or
// feature.
func TestArgsRefusalsKeepALongKeyOffLineOne(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	long := "hooks." + strings.Repeat("x", 194)
	feature := strings.Repeat("f", 200)
	check := func(name string, err error, flag, value string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: no error, want a stop", name)
		}
		first, cause, _ := strings.Cut(err.Error(), "\n")
		if !strings.Contains(first, "`"+flag+"`") {
			t.Errorf("%s: line 1 %q, want it to contain `%s`", name, first, flag)
		}
		if line := "qory run: " + first; len(line) > 100 {
			t.Errorf("%s: line 1 of %d characters behind qory run:, want at most 100: %q", name, len(line), line)
		}
		if !strings.Contains(cause, "`"+value+"`") {
			t.Errorf("%s: cause line %q, want it to contain `%s`", name, cause, value)
		}
	}
	c := rt.(render.ArgsChecker)
	check("CheckArgs -c", c.CheckArgs([]string{"-c", long + "=1"}), "-c", long)
	check("CheckArgs --config", c.CheckArgs([]string{"--config", long + "=1"}), "--config", long)
	w := rt.(render.Walled)
	_, err = w.WalledArgs([]string{"-c" + long + "=1"}, "")
	check("WalledArgs -c", err, "-c", long)
	_, err = w.WalledArgs([]string{"--config=" + long + "=1"}, "")
	check("WalledArgs --config", err, "--config", long)
	_, err = w.WalledArgs([]string{"--enable", feature}, "")
	check("WalledArgs --enable", err, "--enable", feature)
	_, err = w.WalledArgs([]string{"--disable=" + feature}, "")
	check("WalledArgs --disable", err, "--disable", feature)
}

// TestCheckArgsRefuseTheHookSettingsWithoutAWall is each argument that the check of a run
// without a wall refuses: --dangerously-bypass-hook-trust in both forms, --config and
// -c at features.unified_exec_tty, features.codex_hooks, a key under hooks and the
// features table above them, and --enable and --disable of unified_exec_tty,
// codex_hooks and hooks. Each is refused with the text of a walled run, with the flag as
// typed and the whole key or feature, and line 1 is at most 100 characters behind
// "qory run: ". --search, another feature, another key and every argument after -- pass,
// as a walled run alone refuses them.
func TestCheckArgsRefuseTheHookSettingsWithoutAWall(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	c := rt.(render.ArgsChecker)
	config := func(flag, key string) string {
		return "Remove the `" + flag + "` flag and its value from the run's Codex arguments.\n  In this run, qory reserves `" + key + "`; the run takes no value for it from the arguments."
	}
	feature := func(flag, name string) string {
		return "Remove the `" + flag + "` flag and its value from the run's Codex arguments.\n  In this run, qory reserves `features." + name + "`; the run takes no value for it from the arguments."
	}
	above := func(flag, key, reserved string) string {
		return "Remove the `" + flag + "` flag and its value from the run's Codex arguments.\n  `" + key + "` is a table that contains `" + reserved + "`, which qory reserves in this run."
	}
	hooks := "Remove `--dangerously-bypass-hook-trust` from the run's Codex arguments.\n  A Codex run takes its hook trust from qory and Forager, not from the arguments."
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"exec", "--dangerously-bypass-hook-trust", "hi"}, hooks},
		{[]string{"--dangerously-bypass-hook-trust=true", "exec", "hi"}, hooks},
		{[]string{"exec", "-c", "features.unified_exec_tty=true", "hi"}, config("-c", "features.unified_exec_tty")},
		{[]string{"exec", "--config=hooks.x=1", "hi"}, config("--config", "hooks.x")},
		{[]string{"exec", "-c", "features={unified_exec_tty=true}", "hi"}, above("-c", "features", "features.hooks")},
		{[]string{"exec", "-c", "features.codex_hooks=true", "hi"}, config("-c", "features.codex_hooks")},
		{[]string{"exec", "-cfeatures.hooks=false", "hi"}, config("-c", "features.hooks")},
		{[]string{"exec", "--enable", "unified_exec_tty", "hi"}, feature("--enable", "unified_exec_tty")},
		{[]string{"exec", "--disable=codex_hooks", "hi"}, feature("--disable", "codex_hooks")},
		{[]string{"exec", "--enable", "codex_hooks", "hi"}, feature("--enable", "codex_hooks")},
		{[]string{"exec", "--disable", "hooks", "hi"}, feature("--disable", "hooks")},
		{[]string{"--search", "exec", "--enable", "x", "-c", "hooks.Stop=[]"}, config("-c", "hooks.Stop")},
	} {
		err := c.CheckArgs(tc.args)
		if err == nil || err.Error() != tc.want {
			t.Errorf("CheckArgs(%q) = %v, want %q", tc.args, err, tc.want)
			continue
		}
		if line := "qory run: " + strings.Split(err.Error(), "\n")[0]; len(line) > 100 {
			t.Errorf("CheckArgs(%q): line 1 of %d characters behind qory run:, want at most 100: %q", tc.args, len(line), line)
		}
	}
	for _, args := range [][]string{
		{"exec", "--search", "hi"},
		{"exec", "--enable", "some_other_feature", "hi"},
		{"exec", "-c", "model=x", "hi"},
		{"exec", "-c", "features.apps=false", "--oss", "--profile", "p", "hi"},
		{"exec", "--", "--dangerously-bypass-hook-trust", "-c", "hooks.x=1"},
	} {
		if err := c.CheckArgs(args); err != nil {
			t.Errorf("CheckArgs(%q): %v", args, err)
		}
	}
}
