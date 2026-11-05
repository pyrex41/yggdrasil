#!/bin/sh
# Builds the pinned Shen/JIT release in DIR: DIR/build/shen-jit is the stage-1
# host, and DIR is what the jit target's builder reads.
#
#   sh .github/build-shen-jit.sh DIR
set -eu
here=$(dirname "$0")
version=$(sed -n 's/^version=//p' "$here/shen-jit.ref")
sha256=$(sed -n 's/^sha256=//p' "$here/shen-jit.ref")
name=shen-jit-$version.tar.gz
mkdir -p "$1"
curl -fsSL --retry 3 -o "$1/$name" "https://github.com/tizoc/shen-jit/releases/download/v$version/$name"
echo "$sha256  $1/$name" | { command -v sha256sum > /dev/null && sha256sum -c - || shasum -a 256 -c -; }
tar -C "$1" --strip-components=1 -xzf "$1/$name"
rm "$1/$name"
make -C "$1"
"$1/build/shen-jit" --version
