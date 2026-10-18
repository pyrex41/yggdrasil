#!/usr/bin/env bash
# Compile a shaken KL manifest with Shen-C v0.3.0's native RC compiler.
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <out-dir>" >&2
    exit 2
fi

YGG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_C="${SHEN_C:-$YGG_ROOT/../shen-c}"
DIR="$1"
OUT="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac

if [ ! -f "$SHEN_C/Makefile.rc" ] || [ ! -f "$SHEN_C/src/rc/rc.h" ]; then
    echo "c: SHEN_C must name a Shen-C v0.3.0 or newer native RC source checkout" >&2
    exit 1
fi
SHEN_C="$(cd "$SHEN_C" && pwd)"
# Always ask make to refresh the compiler; an existing binary may be stale.
make -C "$SHEN_C" -f Makefile.rc CC="${CC:-cc}" bin/rc-build
export SHEN_C_HOME="$SHEN_C"
exec python3 "$YGG_ROOT/builders/c/compile.py" "$SHEN_C/bin/rc-build" "$DIR" "$OUT"
