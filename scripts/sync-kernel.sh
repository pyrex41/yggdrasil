#!/bin/sh
# Refreshes KLambda/ from the Shen-Language/shen-sources release named below,
# checked by hash.  KLambda/PROVENANCE.md says what is vendored and why.
set -eu
VERSION=43.6
SHA256=61d1795755a57da252ba8108cdfccc680f95a04bfd395a54f533ab6724647ef5
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
curl -fsSL --retry 3 -o "$WORK/k.tar.gz" \
    "https://github.com/Shen-Language/shen-sources/releases/download/shen-$VERSION/ShenOSKernel-$VERSION.tar.gz"
echo "$SHA256  $WORK/k.tar.gz" | { command -v sha256sum > /dev/null && sha256sum -c - || shasum -a 256 -c -; }
tar -C "$WORK" -xzf "$WORK/k.tar.gz"
for f in "$ROOT"/KLambda/*.kl; do
    cp "$WORK/ShenOSKernel-$VERSION/klambda/$(basename "$f")" "$f"
done
cp "$WORK/ShenOSKernel-$VERSION/LICENSE.txt" "$ROOT/KLambda/LICENSE"
