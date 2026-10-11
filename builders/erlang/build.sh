#!/usr/bin/env bash
# Yggdrasil stage-2 builder for shen-erl.
#
#   builders/erlang/build.sh <shaken-dir> <out-dir>
#
# Compiles kernel.kl and the manifest's user modules (every user= line, in
# manifest order) to BEAM, then packages the
# small shen-erl runtime and launcher beside them.  The resulting artifact
# requires only an Erlang runtime; it does not load shen-erl's full kernel.
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <out-dir>" >&2
    exit 2
fi

YGG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_ERL="${SHEN_ERL:-$(cd "$YGG_ROOT/../shen-erl" && pwd)}"

DIR="$1"
OUT="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac

# Every user file, in manifest order: a multi-file program (hoisted leading
# loads, docs/shake.md) has one user= line per file, and they run in order.
USER_KLS=()
while IFS= read -r f; do USER_KLS+=("$f"); done < <(sed -n 's/^user=//p' "$DIR/yggdrasil.manifest.txt")
[ "${#USER_KLS[@]}" -gt 0 ] || { echo "erlang: no user= in manifest" >&2; exit 1; }
USER_PATHS=()
USER_MODULES=""
for f in "${USER_KLS[@]}"; do
    USER_PATHS+=("$DIR/$f")
    USER_MODULES="$USER_MODULES ${f%.kl}"
done

echo "yggdrasil/erlang: building shen-erl runtime ..." >&2
make -C "$SHEN_ERL" >&2

mkdir -p "$OUT/bin" "$OUT/ebin"
cp "$SHEN_ERL/bin/shen-erl" "$OUT/bin/shen-erl"
cp "$SHEN_ERL"/ebin/shen_erl_*.beam "$OUT/ebin/"

"$SHEN_ERL/bin/shen-erl" --kl \
    "$DIR/kernel.kl" "${USER_PATHS[@]}" --output-dir "$OUT/ebin" >&2

printf '%s\n' \
    '#!/bin/sh' \
    'HERE="$(cd "$(dirname "$0")" && pwd)"' \
    'exec "$HERE/bin/shen-erl" --shaken kernel'"$USER_MODULES"' "$@"' \
    > "$OUT/run"
chmod +x "$OUT/run"

echo "yggdrasil/erlang: built $OUT (run: $OUT/run)" >&2
