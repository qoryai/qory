package codex

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/module"
)

// HeadlessArgs are codex exec: the session's events as JSON lines on stdout, in added
// to the folders Codex writes, and the prompt $<name> <arg>, Codex's mention of a
// skill. Codex renders no command, so a command has no form.
// Source: codex exec --help and https://developers.openai.com/codex/noninteractive, codex-cli 0.162.1.
func (codex) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	if kind != "skills" {
		return nil, false
	}
	return []string{"exec", "--json", "--add-dir", in, "$" + name + " " + arg}, true
}

// Install is Codex's npm package at version.
func (codex) Install(version string) string { return "@openai/codex@" + version }

// WalledArgs are args with --sandbox danger-full-access after exec, so Codex runs
// commands outside a Codex sandbox: inside the wall, every other sandbox mode fails
// before it runs a command, since bubblewrap cannot create a namespace there, and the
// wall is the limit of what the agent changes. The flag is left out when args have no
// exec, an interactive start, and when any argument before -- sets a sandbox mode, in
// the forms --sandbox, --sandbox=<mode>, -s, -s=<mode> and -s<mode>, bypasses one, or
// asks for --approve-for-me or its alias --not-so-yolo, which Codex refuses beside
// --sandbox (codex-rs/utils/cli/src/shared_options.rs).
//
// The last argument after exec, the prompt, mentions the skill of [HeadlessArgs] by its
// path in the home that a walled run binds at [HomeInWall] when it starts with $<name>
// and the runtime's directory of home, the composed harness home, has skills/<name>/
// SKILL.md, see [mentionByPath], also when the arguments set a sandbox mode. Any other
// prompt, such as "$HOME is unset", stays as given.
//
// Arguments that set the model provider, a profile, a feature, web search, hook trust, a
// remote app server, or a key of [reservedKeys], [argReservedKeys] or [trustKeys] with
// --config or -c, or that skip qory's config.toml, are refused, see [checkArgs].
func (codex) WalledArgs(args []string, home string) ([]string, error) {
	if err := checkArgs(args, true); err != nil {
		return nil, err
	}
	i := slices.Index(args, "exec")
	if i < 0 {
		return args, nil
	}
	args = slices.Clone(args)
	if n := len(args) - 1; n > i {
		args[n] = mentionByPath(args[n], func(name string) bool {
			if home == "" {
				return false
			}
			info, err := os.Stat(filepath.Join(home, Runtime, "skills", name, "SKILL.md"))
			return err == nil && info.Mode().IsRegular()
		})
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if setsSandbox(a) {
			return args, nil
		}
	}
	return slices.Concat(args[:i+1], []string{"--sandbox", "danger-full-access"}, args[i+1:]), nil
}

// StartDirs are the folders that args, a walled run's Codex arguments, start Codex in:
// the value of each -C and --cd before --, in the forms -C <dir>, -C<dir>, -C=<dir>,
// --cd <dir> and --cd=<dir>, in the order of args. Codex starts in the folder of the
// last one (codex-rs/utils/cli/src/shared_options.rs, cwd); each one is returned, so
// that each gets a trust entry. A flag with an empty value, or with no argument after
// it, adds none.
func StartDirs(args []string) []string {
	var dirs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var dir string
		switch {
		case a == "--":
			return dirs
		case a == "-C" || a == "--cd":
			if i+1 == len(args) {
				return dirs
			}
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--cd="):
			dir = strings.TrimPrefix(a, "--cd=")
		case strings.HasPrefix(a, "-C"):
			dir = strings.TrimPrefix(strings.TrimPrefix(a, "-C"), "=")
		default:
			continue
		}
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// textConfigArg is the refusal of a run whose Codex arguments set a key of [trustKeys],
// or, in a walled run, of [reservedKeys] or [argReservedKeys], or a key under one, with
// --config or -c, with the flag as typed, --config or -c, and the key as given for its two
// %s. --enable and --disable of a feature of [trustFeatures] get it too, with the flag and
// features.<feature>. The key is on the cause line, so line 1 is at most as long as with
// --config.
const textConfigArg = "Remove the `%s` flag and its value from the run's Codex arguments.\n  In this run, qory reserves `%s`; the run takes no value for it from the arguments."

// textConfigAboveArg is the refusal of a run whose Codex arguments set a key above a key
// of [trustKeys], or, in a walled run, of [reservedKeys] or [argReservedKeys], with the
// flag as typed, the key as given and the reserved key under it for its three %s. The
// text states only that the table contains the reserved key.
const textConfigAboveArg = "Remove the `%s` flag and its value from the run's Codex arguments.\n  `%s` is a table that contains `%s`, which qory reserves in this run."

// textProviderArg is the refusal of a walled run whose Codex arguments contain --oss or
// --local-provider, with the flag for its %s.
const textProviderArg = "Remove `%s` from the run's Codex arguments.\n  A Codex run takes its model provider from qory and Forager, not from the arguments."

// textFeatureArg is the refusal of a walled run whose Codex arguments contain --enable or
// --disable of a feature outside [trustFeatures], with the flag and the feature as given
// for its two %s. The feature is on the cause line, so line 1 is at most as long as with
// --disable.
const textFeatureArg = "Remove the `%s` flag and its value from the run's Codex arguments.\n  A walled Codex run takes no feature from its arguments, `%s` included."

// textProfileArg is the refusal of a walled run whose Codex arguments contain --profile
// or -p.
const textProfileArg = "Remove `--profile` from the run's Codex arguments.\n  A Codex run takes its settings from qory and Forager, not from a profile."

// textIgnoreConfigArg is the refusal of a walled run whose Codex arguments contain
// --ignore-user-config.
const textIgnoreConfigArg = "Remove `--ignore-user-config` from the run's Codex arguments.\n  A Codex run takes its settings from qory and Forager, not from the arguments."

// textSearchArg is the refusal of a walled run whose Codex arguments contain --search.
const textSearchArg = "Remove `--search` from the run's Codex arguments.\n  A Codex run takes its web search setting from qory and Forager, not from the arguments."

// textHookTrustArg is the refusal of a run whose Codex arguments contain
// --dangerously-bypass-hook-trust.
const textHookTrustArg = "Remove `--dangerously-bypass-hook-trust` from the run's Codex arguments.\n  A Codex run takes its hook trust from qory and Forager, not from the arguments."

// textRemoteArg is the refusal of a walled run whose Codex arguments contain --remote or
// --remote-auth-token-env, with the flag for its %s.
const textRemoteArg = "Remove `%s` from the run's Codex arguments.\n  A Codex run takes its app server from qory and Forager, not from the arguments."

// argReservedKeys are the key paths that the arguments of a walled run may not set
// beside [reservedKeys]. Forager sets model_reasoning_effort from the run's effort as a
// --config flag; a harness's config.toml may still set it as a default. skills and
// cloud.skills turn skills on or off and list skill folders
// (codex-rs/config/src/config_toml.rs, SkillsConfig and CloudToml), and the composed
// harness sets which skills a run has.
var argReservedKeys = []reservedKey{{"model_reasoning_effort"}, {"skills"}, {"cloud", "skills"}}

// trustKeys are the key paths that the Codex arguments of every run may not set, walled
// or not: the hooks table, whose state contains the trusted hashes and whose events add
// hooks, and the features that run hooks and that keep a shell with a terminal off,
// unified_exec_tty and hooks with its legacy name codex_hooks
// (codex-rs/features/src/lib.rs, FEATURES, and legacy.rs, ALIASES). A shell with a
// terminal takes commands through write_stdin, for which Codex 0.170.0 runs no
// PreToolUse hook.
var trustKeys = []reservedKey{{"hooks"}, {"features", "hooks"}, {"features", "codex_hooks"}, {"features", "unified_exec_tty"}}

// trustFeatures are the features of [trustKeys] as the values of --enable and --disable.
var trustFeatures = []string{"hooks", "codex_hooks", "unified_exec_tty"}

// CheckArgs returns an error for args, the Codex arguments of a run without a wall, as
// [checkArgs] does for every run. A walled run gets the whole check from [codex.WalledArgs].
func (codex) CheckArgs(args []string) error {
	return checkArgs(args, false)
}

// checkArgs returns an error for args, a run's Codex arguments, when one before --
// changes the hooks that qory sets for the run, naming the first in args:
//
//   - --dangerously-bypass-hook-trust, with which Codex runs hooks that were never
//     trusted (codex-rs/utils/cli/src/shared_options.rs, bypass_hook_trust);
//   - --config or -c that sets a key of [trustKeys], in the forms --config <k=v>,
//     --config=<k=v>, -c <k=v>, -c<k=v> and -c=<k=v>
//     (codex-rs/utils/cli/src/config_override.rs);
//   - --enable or --disable of a feature of [trustFeatures], in the forms
//     --enable <feature> and --enable=<feature>, which Codex reads as --config
//     features.<feature>=true or false (codex-rs/cli/src/main.rs, FeatureToggles).
//
// With walled, it also returns an error for args when one before -- changes what Forager
// and qory set for a walled run:
//
//   - --oss, which starts Codex on an open-source model provider, or --local-provider,
//     which names one, in the forms --local-provider <name> and --local-provider=<name>
//     (codex-rs/utils/cli/src/shared_options.rs, oss and oss_provider);
//   - --ignore-user-config, with which codex exec skips $CODEX_HOME/config.toml, the
//     file qory writes (codex-rs/exec/src/cli.rs, ignore_user_config);
//   - --search, with which an interactive codex turns on web search
//     (codex-rs/tui/src/cli.rs, web_search);
//   - --profile, which lays $CODEX_HOME/<name>.config.toml over the settings, in the
//     forms --profile <name>, --profile=<name>, -p <name>, -p<name> and -p=<name>
//     (the same file, config_profile_v2). -p is --profile in codex and in codex exec;
//   - --remote, which connects Codex to another app server, in the forms --remote <addr>
//     and --remote=<addr>, and --remote-auth-token-env, which names the variable of that
//     server's token, in the same two forms (codex-rs/cli/src/main.rs,
//     InteractiveRemoteOptions). Neither flag has a short form;
//   - --config or -c that sets a key of [reservedKeys] or [argReservedKeys], in the
//     forms --config <k=v>, --config=<k=v>, -c <k=v>, -c<k=v> and -c=<k=v>
//     (codex-rs/utils/cli/src/config_override.rs);
//   - --enable or --disable, in the forms --enable <feature> and --enable=<feature>,
//     which Codex reads as --config features.<feature>=true or false: features is a
//     key of [reservedKeys], so every feature is refused, also a legacy name such as
//     connectors for apps (codex-rs/cli/src/main.rs, FeatureToggles). Neither flag has
//     a short form.
//
// Forager puts its --config flags ahead of these arguments, and Codex 0.167.2 takes the
// last flag for a key, so such an argument would replace Forager's value. Codex reads
// the key of --config as the text before the first =, without spaces at either end,
// with use_legacy_landlock read as features.use_legacy_landlock, as a dotted path
// (codex-rs/config/src/overrides.rs). A key at a reserved path or under one is refused.
// A key above one is refused whatever its value: the value replaces the whole table,
// Forager's --config values in it too. Codex refuses a --config, -c,
// --enable or --disable with no value after it, or with a value that starts with -, so
// such a flag passes here. --oss, --ignore-user-config and --search take no value, and
// Codex refuses one given with =; such a form is refused here as well. Codex reads no
// long flag by a shortened name: its parser does not turn on infer_long_args. Of its
// short flags, only -h and -V take no value, and both print and exit, so no other flag
// comes before -c or -p in one word.
func checkArgs(args []string, walled bool) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		var pair, flag string
		switch {
		case a == "--":
			return nil
		case a == "--dangerously-bypass-hook-trust" || strings.HasPrefix(a, "--dangerously-bypass-hook-trust="):
			return errors.New(textHookTrustArg)
		case !walled && !strings.HasPrefix(a, "--enable") && !strings.HasPrefix(a, "--disable") && !strings.HasPrefix(a, "--config") && !strings.HasPrefix(a, "-c"):
			continue
		case a == "--oss" || strings.HasPrefix(a, "--oss="):
			return fmt.Errorf(textProviderArg, "--oss")
		case a == "--ignore-user-config" || strings.HasPrefix(a, "--ignore-user-config="):
			return errors.New(textIgnoreConfigArg)
		case a == "--search" || strings.HasPrefix(a, "--search="):
			return errors.New(textSearchArg)
		case a == "--local-provider" || strings.HasPrefix(a, "--local-provider="):
			return fmt.Errorf(textProviderArg, "--local-provider")
		case a == "--remote" || strings.HasPrefix(a, "--remote="):
			return fmt.Errorf(textRemoteArg, "--remote")
		case a == "--remote-auth-token-env" || strings.HasPrefix(a, "--remote-auth-token-env="):
			return fmt.Errorf(textRemoteArg, "--remote-auth-token-env")
		case a == "--profile" || strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "-p"):
			return errors.New(textProfileArg)
		case a == "--enable" || a == "--disable":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				if err := featureArg(a, args[i], walled); err != nil {
					return err
				}
			}
			continue
		case strings.HasPrefix(a, "--enable=") || strings.HasPrefix(a, "--disable="):
			if flag, feature, _ := strings.Cut(a, "="); feature != "" {
				if err := featureArg(flag, feature, walled); err != nil {
					return err
				}
			}
			continue
		case a == "--config" || a == "-c":
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
				continue
			}
			i++
			pair, flag = args[i], a
		case strings.HasPrefix(a, "--config="):
			pair, flag = strings.TrimPrefix(a, "--config="), "--config"
		case strings.HasPrefix(a, "-c"):
			pair, flag = strings.TrimPrefix(strings.TrimPrefix(a, "-c"), "="), "-c"
		default:
			continue
		}
		key, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if reserved, above, ok := reservedArg(key, walled); ok && above {
			return fmt.Errorf(textConfigAboveArg, flag, shownArg(key), shownArg(strings.Join(reserved, ".")))
		} else if ok {
			return fmt.Errorf(textConfigArg, flag, shownArg(key))
		}
	}
	return nil
}

// featureArg returns the refusal of flag, --enable or --disable, with feature, when the
// feature is one of [trustFeatures], as the key features.<feature>, or when walled.
func featureArg(flag, feature string, walled bool) error {
	switch {
	case slices.Contains(trustFeatures, feature):
		return fmt.Errorf(textConfigArg, flag, shownArg("features."+feature))
	case walled:
		return fmt.Errorf(textFeatureArg, flag, shownArg(feature))
	}
	return nil
}

// shownArg is a name from a run's Codex arguments as a refusal prints it: as
// [accesskey.ShownName] prints it, with each control character written as a Go escape,
// see [module.Escape], so the line reaches the terminal as one line of plain text.
func shownArg(name string) string { return module.Escape(accesskey.ShownName(name, false)) }

// reservedArg returns the first key path of [trustKeys], or with walled of [reservedKeys]
// and [argReservedKeys], that key, a dotted key path of a --config flag, is or lies under,
// with ok. Without one, it returns the first such path that key lies above, with above and
// ok.
func reservedArg(key string, walled bool) (reserved reservedKey, above, ok bool) {
	if key == "use_legacy_landlock" {
		key = "features.use_legacy_landlock"
	}
	names := strings.Split(key, ".")
	keys := trustKeys
	if walled {
		keys = slices.Concat(trustKeys, reservedKeys, argReservedKeys)
	}
	for _, k := range keys {
		if len(names) >= len(k) && overlaps(k, names) {
			return k, false, true
		}
	}
	for _, k := range keys {
		if overlaps(k, names) {
			return k, true, true
		}
	}
	return nil, false, false
}

// overlaps reports whether each name of the shorter of k and names matches the
// pattern of k at the same place: names is then k, a path under k, or a path above k.
func overlaps(k reservedKey, names []string) bool {
	for i := range min(len(k), len(names)) {
		if ok, _ := path.Match(k[i], names[i]); !ok {
			return false
		}
	}
	return true
}

// mentionByPath is prompt with a leading $<name> written as [$<name>](<path>), the path
// of the skill <name> in the home in the wall, when isSkill(<name>), and prompt as it is
// otherwise. Codex
// selects a skill mentioned by path by that path alone, and a skill mentioned by $<name>
// by the first enabled skill of that name, which may be one of the repository's
// (codex-rs/ext/skills/src/selection.rs). A name is one or more of the characters Codex
// reads in a mention, A-Z, a-z, 0-9, _ and -; a plugin's name has a colon and no folder in
// the home, so it is none.
func mentionByPath(prompt string, isSkill func(string) bool) string {
	rest, ok := strings.CutPrefix(prompt, "$")
	if !ok {
		return prompt
	}
	end := strings.IndexFunc(rest, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
	if end < 0 {
		end = len(rest)
	}
	if end == 0 || end < len(rest) && rest[end] != ' ' || !isSkill(rest[:end]) {
		return prompt
	}
	name := rest[:end]
	return "[$" + name + "](" + path.Join(HomeInWall, "skills", name, "SKILL.md") + ")" + rest[end:]
}

// setsSandbox reports whether the argument a sets Codex's sandbox mode, bypasses the
// sandbox, or asks for a mode that conflicts with --sandbox.
func setsSandbox(a string) bool {
	switch a {
	case "--sandbox", "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--approve-for-me", "--not-so-yolo":
		return true
	}
	return strings.HasPrefix(a, "--sandbox=") || strings.HasPrefix(a, "-s") && !strings.HasPrefix(a, "--")
}
