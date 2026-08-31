#!/usr/bin/env bash
# Yggdrasil — stage-2 C (shen-c) builder.
#
#   builders/c/build.sh <shaken-dir> <out-dir>
#
# Emits <out-dir>/app.c plus Makefile and CMakeLists.txt, then `make`
# links bin/libshenc.a into <out-dir>/app. Option 5 rung 1 NativeFunctions
# (not eval_kl_object of the source string).
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <out-dir>" >&2
    exit 2
fi

YGG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_C="${SHEN_C:-$(cd "$YGG_ROOT/../shen-c" && pwd)}"
DIR="$1"
OUT="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac

need_nix=0
if ! command -v pkg-config >/dev/null 2>&1; then
    need_nix=1
elif ! pkg-config --exists bdw-gc; then
    need_nix=1
else
    _gc="$(pkg-config --cflags --libs bdw-gc 2>/dev/null || true)"
    case "$_gc" in
        *Homebrew*|*'/opt/homebrew'*) need_nix=1 ;;
    esac
fi
if [ "$need_nix" = 1 ]; then
    if [ "${YGGDRASIL_C_IN_NIX:-}" != 1 ] && command -v nix >/dev/null 2>&1 && [ -f "$SHEN_C/flake.nix" ]; then
        exec nix develop "$SHEN_C" -c env YGGDRASIL_C_IN_NIX=1 SHEN_C="$SHEN_C" CC="${CC:-clang}" bash "$0" "$@"
    fi
    echo "c: bdw-gc must come from Nix pkg-config (not Homebrew). Run:" >&2
    echo "  nix develop $SHEN_C -c $0 $DIR $OUT" >&2
    exit 1
fi

if [ ! -x "$SHEN_C/bin/yggdrasil-build" ] || [ ! -f "$SHEN_C/bin/libshenc.a" ]; then
    echo "c: building shen-c lib + yggdrasil-build in $SHEN_C" >&2
    make -C "$SHEN_C" CC="${CC:-clang}" bin/libshenc.a bin/yggdrasil-build
fi

export SHEN_C_HOME="$SHEN_C"
exec "$SHEN_C/bin/yggdrasil-build" "$DIR" "$OUT"
