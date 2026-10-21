#!/bin/sh
# Build the image of qory's static Linux build, /qory/qory, from a commit of this
# repository, for linux/amd64 and linux/arm64, and print the digest of each platform's
# image and of the image index that names both.
#
#   scripts/build-image.sh                    # the commit HEAD names
#   scripts/build-image.sh 543f055            # another commit
#   scripts/build-image.sh --local-replace    # a go.mod that replaces a module with a path
#
# The image is built from a clone of the commit, never from the working tree, and a tree
# with uncommitted or untracked changes is refused, so the image is the commit and qory
# version in it prints that commit. It is written as an OCI layout to
# dist/image/sha-<the commit's first 7 characters>/, replacing one built there before. It
# needs a docker buildx builder that builds for both platforms and exports OCI, such as
# one docker buildx create made; BUILDX_BUILDER names it.
#
# A go.mod that replaces a module with a local path is refused: the path is on this
# machine alone, and the build cannot read it. With --local-replace go mod vendor copies
# every module the build needs into the clone, the replaced ones from their local paths,
# and the image, in dist/image/sha-<7>-local/, is for local tests only: qory version in
# it marks the commit dirty.
set -eu

usage="usage: build-image.sh [--local-replace] [commit]"
local_replace=
commit=
for arg in "$@"; do
	case $arg in
	--local-replace) local_replace=1 ;;
	-h | --help) echo "$usage"; exit 0 ;;
	-*) echo "build-image: unknown flag $arg; $usage" >&2; exit 2 ;;
	*)
		if [ -n "$commit" ]; then
			echo "build-image: more than one commit given; $usage" >&2
			exit 2
		fi
		commit=$arg
		;;
	esac
done
commit=${commit:-HEAD}

top=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
sha=$(git -C "$top" rev-parse --verify --quiet "$commit^{commit}") || {
	echo "build-image: $commit names no commit" >&2
	exit 1
}
short=$(printf '%s' "$sha" | cut -c1-7)

if [ -n "$(git -C "$top" status --porcelain)" ]; then
	echo "build-image: the tree has uncommitted changes; commit or stash them first" >&2
	exit 1
fi

# The modules go.mod at the commit replaces with a local path: one beginning with /, ./
# or ../, on a replace line or in a replace block.
replaced=$(git -C "$top" show "$sha:go.mod" | awk '
	/^replace[ \t]*\(/ { block = 1; next }
	block && /^\)/ { block = 0; next }
	block || /^replace[ \t]/ {
		line = $0
		sub(/^replace[ \t]+/, "", line)
		if (split(line, side, "=>") != 2) next
		split(side[1], left, " ")
		split(side[2], right, " ")
		if (right[1] ~ /^(\/|\.\/|\.\.\/)/) print left[1], right[1]
	}')
mods=$(printf '%s\n' "$replaced" | awk 'NF { printf "%s%s", sep, $1; sep = " " }')

out=$top/dist/image/sha-$short
if [ -n "$replaced" ]; then
	if [ -z "$local_replace" ]; then
		for m in $mods; do
			echo "build-image: go.mod at $short replaces $m with a local path, which the build cannot read" >&2
		done
		echo "build-image: an image of it is for local tests only; give --local-replace to build one" >&2
		exit 1
	fi
	out=$out-local
elif [ -n "$local_replace" ]; then
	echo "build-image: --local-replace given, and go.mod at $short replaces no module with a local path" >&2
	exit 1
fi

# The clone, and a build that did not finish, go when the script ends, also on a signal:
# dash runs the EXIT trap on exit, and not when a signal ends it.
tmp=$(mktemp -d)
build=$top/dist/image/.build-$short
trap 'rm -rf "$tmp" "$build"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# A clone whose .git is a directory, as go needs to stamp the commit, holding every
# object the commit needs, with no remote that names this machine's path.
src=$tmp/src
git clone --quiet --shared --no-checkout "$top" "$src"
git -C "$src" -c advice.detachedHead=false checkout --quiet --detach "$sha"
git -C "$src" remote remove origin
git -C "$src" repack --quiet -a -d
rm -f "$src/.git/objects/info/alternates"

if [ -n "$replaced" ]; then
	# A relative path is relative to this repository's go.mod, not to the clone's.
	printf '%s\n' "$replaced" | while read -r m path; do
		case $path in
		/*) ;;
		*) (cd "$src" && GOWORK=off go mod edit -replace "$m=$(cd "$top" && cd "$path" && pwd)") ;;
		esac
	done
	(cd "$src" && GOWORK=off go mod vendor)
fi

mkdir -p "$top/dist/image"
rm -rf "$build"
docker buildx build \
	--platform linux/amd64,linux/arm64 \
	--provenance=false --sbom=false \
	--label "org.opencontainers.image.revision=$sha" \
	--output "type=oci,dest=$build,tar=false" \
	"$src"
rm -rf "$out"
mv "$build" "$out"

# manifests prints "<os>/<architecture> <digest>" for each descriptor of an OCI index,
# "-" for one with no platform. Each descriptor's platform follows its digest.
manifests() {
	tr ',{}' '\n\n\n' <"$1" | awk -F'"' '
		function flush() {
			if (d != "") print (os == "" ? "-" : os "/" arch (v == "" ? "" : "/" v)), d
			d = ""; os = ""; arch = ""; v = ""
		}
		$2 == "digest" { flush(); d = $4 }
		$2 == "os" { os = $4 }
		$2 == "architecture" { arch = $4 }
		$2 == "variant" { v = $4 }
		END { flush() }'
}

index=$(manifests "$out/index.json" | awk 'NR == 1 { print $2 }')
echo
echo "qory image of $sha, in ${out#"$top"/}/"
printf '  %-14s %s\n' index "$index"
manifests "$out/blobs/sha256/${index#sha256:}" | while read -r platform digest; do
	printf '  %-14s %s\n' "$platform" "$digest"
done
if [ -n "$replaced" ]; then
	echo
	echo "This image is for local tests only: go.mod replaces $mods with a path on this machine. Never give it to a runner."
fi
