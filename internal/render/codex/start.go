package codex

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
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
// commands with no sandbox of its own: inside the wall, every other sandbox mode fails
// before it runs a command, since bubblewrap cannot create a namespace there, and the
// wall is the limit of what the agent changes. The flag is left out when args have no
// exec, an interactive start, and when any argument before -- sets a sandbox mode, in
// the forms --sandbox, --sandbox=<mode>, -s, -s=<mode> and -s<mode>, bypasses one, or
// asks for --approve-for-me or its alias --not-so-yolo, which Codex refuses beside
// --sandbox (codex-rs/utils/cli/src/shared_options.rs). The prompt stays $<name>: no run
// binds the home at [HomeInWall] yet, so a mention by that path, see [mentionByPath],
// would lead to no skill. Arguments that set the model provider, a profile, a feature,
// web search, or a key of [reservedKeys] or [argReservedKeys] with --config or -c, or
// that skip qory's config.toml, are refused, see [checkArgs].
func (codex) WalledArgs(args []string) ([]string, error) {
	if err := checkArgs(args); err != nil {
		return nil, err
	}
	i := slices.Index(args, "exec")
	if i < 0 {
		return args, nil
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

// textConfigArg is the refusal of a walled run whose Codex arguments set a key of
// [reservedKeys] or [argReservedKeys] with --config or -c, with the key as given for its
// %s.
const textConfigArg = "Remove `--config %s` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."

// textProviderArg is the refusal of a walled run whose Codex arguments contain --oss or
// --local-provider, with the flag for its %s.
const textProviderArg = "Remove `%s` from the run's Codex arguments: a Codex run takes its model provider from qory and Forager, not from the arguments."

// textFeatureArg is the refusal of a walled run whose Codex arguments contain --enable or
// --disable, with the flag and the feature as given for its two %s.
const textFeatureArg = "Remove `%s %s` from the run's Codex arguments: a Codex run takes this key from qory and Forager, not from the arguments."

// textProfileArg is the refusal of a walled run whose Codex arguments contain --profile
// or -p.
const textProfileArg = "Remove `--profile` from the run's Codex arguments: a Codex run takes its settings from qory and Forager, not from a profile."

// textIgnoreConfigArg is the refusal of a walled run whose Codex arguments contain
// --ignore-user-config.
const textIgnoreConfigArg = "Remove `--ignore-user-config` from the run's Codex arguments: a Codex run takes its settings from qory and Forager, not from the arguments."

// textSearchArg is the refusal of a walled run whose Codex arguments contain --search.
const textSearchArg = "Remove `--search` from the run's Codex arguments: a Codex run takes its web search setting from qory and Forager, not from the arguments."

// argReservedKeys are the key paths that the arguments of a walled run may not set
// beside [reservedKeys]. Forager sets model_reasoning_effort from the run's effort as a
// --config flag; a harness's config.toml may still set it as a default.
var argReservedKeys = []reservedKey{{"model_reasoning_effort"}}

// checkArgs refuses args, a walled run's Codex arguments, when one before -- changes
// what Forager and qory set for the run, naming the first in args:
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
// (codex-rs/config/src/overrides.rs). A key at a reserved path, under one, or above one,
// whose table value sets the reserved path, is refused. Codex refuses a --config, -c,
// --enable or --disable with no value after it, or with a value that starts with -, so
// such a flag passes here. --oss, --ignore-user-config and --search take no value, and
// Codex refuses one given with =; such a form is refused here as well. Codex reads no
// long flag by a shortened name: its parser does not turn on infer_long_args. Of its
// short flags, only -h and -V take no value, and both print and exit, so no other flag
// comes before -c or -p in one word.
func checkArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		var pair string
		switch {
		case a == "--":
			return nil
		case a == "--oss" || strings.HasPrefix(a, "--oss="):
			return fmt.Errorf(textProviderArg, "--oss")
		case a == "--ignore-user-config" || strings.HasPrefix(a, "--ignore-user-config="):
			return errors.New(textIgnoreConfigArg)
		case a == "--search" || strings.HasPrefix(a, "--search="):
			return errors.New(textSearchArg)
		case a == "--local-provider" || strings.HasPrefix(a, "--local-provider="):
			return fmt.Errorf(textProviderArg, "--local-provider")
		case a == "--profile" || strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "-p"):
			return errors.New(textProfileArg)
		case a == "--enable" || a == "--disable":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return fmt.Errorf(textFeatureArg, a, args[i+1])
			}
			continue
		case strings.HasPrefix(a, "--enable=") || strings.HasPrefix(a, "--disable="):
			if flag, feature, _ := strings.Cut(a, "="); feature != "" {
				return fmt.Errorf(textFeatureArg, flag, feature)
			}
			continue
		case a == "--config" || a == "-c":
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
				continue
			}
			i++
			pair = args[i]
		case strings.HasPrefix(a, "--config="):
			pair = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-c"):
			pair = strings.TrimPrefix(strings.TrimPrefix(a, "-c"), "=")
		default:
			continue
		}
		key, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if key = strings.TrimSpace(key); reservedArg(key) {
			return fmt.Errorf(textConfigArg, key)
		}
	}
	return nil
}

// reservedArg reports whether key, a dotted key path of a --config flag, is a path of
// [reservedKeys] or [argReservedKeys], lies under one, or lies above one.
func reservedArg(key string) bool {
	if key == "use_legacy_landlock" {
		key = "features.use_legacy_landlock"
	}
	names := strings.Split(key, ".")
	for _, k := range slices.Concat(reservedKeys, argReservedKeys) {
		if overlaps(k, names) {
			return true
		}
	}
	return false
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
// of the skill <name> in the home in the wall, and prompt as it is otherwise. Codex
// selects a skill mentioned by path by that path alone, and a skill mentioned by $<name>
// by the first enabled skill of that name, which may be one of the repository's
// (codex-rs/ext/skills/src/selection.rs). [WalledArgs] calls it once a walled run binds
// the home at [HomeInWall]. A name is
// one or more of the characters Codex reads in a mention, A-Z, a-z, 0-9, _ and -, and
// the colon of a plugin's name, which has no folder of its own in the home, is none.
func mentionByPath(prompt string) string {
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
	if end == 0 || end < len(rest) && rest[end] != ' ' {
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
