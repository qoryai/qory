#!/bin/sh
# Print the CHANGELOG.md section of one release, for the release body on GitHub.
#
#   scripts/changelog-section.sh 0.3.0            # the section, without its heading
#   scripts/changelog-section.sh 0.3.0 notes.md   # the same, written to a file
#
# The section starts at the line "## [0.3.0] - <date>" and ends before the next "## "
# heading or the link list at the foot. A version with no section is an error, which is
# what stops a tag from publishing without one.
set -eu

version=${1:?usage: changelog-section.sh <version> [file]}
out=${2:-/dev/stdout}
file=$(dirname "$0")/../CHANGELOG.md

section=$(awk -v v="$version" '
	$0 ~ "^## \\[" v "\\] " { found = 1; next }
	found && /^## / { exit }
	found && /^\[/ && /\]: / { exit }
	found { print }
' "$file")

if [ -z "$section" ]; then
	echo "changelog-section: CHANGELOG.md has no section [$version]; add one before tagging v$version" >&2
	exit 1
fi
printf '%s\n' "$section" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' >"$out"
