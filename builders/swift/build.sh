#!/usr/bin/env bash
# Yggdrasil — stage-2 Swift (shen-swift) builder.
#
#   builders/swift/build.sh <shaken-dir> <out-dir>
#
# <shaken-dir> is a (yggdrasil.shake ...) output (kernel.kl + user .kl +
# yggdrasil.manifest). Produces a self-contained artifact at <out-dir>: the
# shaken kernel + user slice plus a `run` launcher that drives the shen-swift
# tree-walking interpreter in `--shaken` mode (boot the minimal slice instead
# of the full 19-file kernel, then run the program to completion).
#
# Unlike the compiled targets (lisp/go/rust/scheme), shen-swift is an
# INTERPRETER, so there is nothing to code-generate: the artifact is the KL
# slice itself, executed by the shen-swift binary — the same relationship the
# lua/julia targets have to their runtimes. The win is boot speed: a ~200-line
# shaken kernel vs the full ~2500-line kernel.
#
# Overridable environment:
#   SHEN_SWIFT       - shen-swift checkout (default: sibling ../shen-swift)
#   SHEN_SWIFT_BIN   - the shen-swift binary (default: built from $SHEN_SWIFT)
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <out-dir>" >&2
    exit 2
fi

RAT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_SWIFT="${SHEN_SWIFT:-$(cd "$RAT_ROOT/../shen-swift" && pwd)}"

DIR="$1"; OUT="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
mkdir -p "$OUT"

# The user .kl filenames from the manifest: a multi-file program has one line per
# file (docs/shake.md), and the interpreter takes one file of forms.
USER_KLS="$(sed -n 's/^user=//p' "$DIR/yggdrasil.manifest.txt")"
[ -n "$USER_KLS" ] || { echo "swift: no user= in manifest" >&2; exit 1; }

# Resolve the shen-swift binary; build a release binary if none is provided and
# none is already built (the interpreter is the runtime, referenced by `run`).
if [ -z "${SHEN_SWIFT_BIN:-}" ]; then
    for cand in \
        "$SHEN_SWIFT/.build/release/shen-swift" \
        "$SHEN_SWIFT"/.build/*/release/shen-swift; do
        [ -x "$cand" ] && SHEN_SWIFT_BIN="$cand" && break
    done
fi
if [ -z "${SHEN_SWIFT_BIN:-}" ]; then
    echo "swift: building shen-swift (release) in $SHEN_SWIFT ..." >&2
    ( cd "$SHEN_SWIFT" && swift build -c release >&2 )
    SHEN_SWIFT_BIN="$SHEN_SWIFT/.build/release/shen-swift"
fi
[ -x "$SHEN_SWIFT_BIN" ] || { echo "swift: shen-swift binary not found ($SHEN_SWIFT_BIN)" >&2; exit 1; }

# The artifact is the shaken slice; copy it next to the launcher.
cp "$DIR/kernel.kl" "$OUT/kernel.kl"
: > "$OUT/user.kl"
printf '%s\n' "$USER_KLS" | while IFS= read -r user_kl; do
    cat "$DIR/$user_kl" >> "$OUT/user.kl"
    printf '\n' >> "$OUT/user.kl"
done

# `run` drives the interpreter on exactly this slice (no full-kernel load, no
# launcher). SHEN_SWIFT_BIN is baked in but stays overridable at run time.
cat > "$OUT/run" <<SH
#!/bin/sh
HERE="\$(cd "\$(dirname "\$0")" && pwd)"
BIN="\${SHEN_SWIFT_BIN:-$SHEN_SWIFT_BIN}"
exec "\$BIN" --shaken "\$HERE/kernel.kl" "\$HERE/user.kl" "\$@"
SH
chmod +x "$OUT/run"

echo "yggdrasil/swift: built $OUT (run: $OUT/run)"
