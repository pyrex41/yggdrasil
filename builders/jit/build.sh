#!/usr/bin/env bash
# Yggdrasil - stage-2 Shen/JIT builder.
#
#   builders/jit/build.sh <shaken-dir> <out-exe>
#
# Lowers the shaken kernel, the user files and the part of the Shen/JIT port
# they reach with Shen/JIT's own lowerer (builders/jit/build.shen), writes an
# image from them with `runtime --stage1`, and appends it to the runtime. The
# result is one native executable that holds the slice, not the whole kernel.
#
# Overridable environment:
#   SHEN_JIT      Shen/JIT checkout or source tarball (default: sibling ../shen-jit)
#   SHEN_JIT_BIN  executable that runs the lowerer (default: $SHEN_JIT/build/shen-jit)
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <out-exe>" >&2
    exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_JIT="${SHEN_JIT:-$ROOT/../shen-jit}"
# Exit 3 is a capability SKIP: without the port this target cannot be checked.
if [ ! -f "$SHEN_JIT/src/port/shen-jit.kl" ]; then
    echo "jit: no Shen/JIT sources at $SHEN_JIT; set YGGDRASIL_SHEN_JIT_DIR to a source checkout or a release source tarball" >&2
    exit 3
fi
SHEN_JIT="$(cd "$SHEN_JIT" && pwd)"
SHEN_JIT_BIN="${SHEN_JIT_BIN:-$SHEN_JIT/build/shen-jit}"
RUNTIME="$SHEN_JIT/build/runtime"
LOWER_KL="$SHEN_JIT/build/port/lower.kl"

DIR="$1"; OUT="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac

# A checkout that has not been built is built here: a release's source tarball
# with a C compiler alone, a Git checkout after make deps fetches its host.
if [ ! -x "$SHEN_JIT_BIN" ] || [ ! -x "$RUNTIME" ] || [ ! -e "$LOWER_KL" ]; then
    echo "jit: building Shen/JIT in $SHEN_JIT ..." >&2
    if [ ! -d "$SHEN_JIT/generated" ]; then
        make -C "$SHEN_JIT" deps >&2
    fi
    make -C "$SHEN_JIT" >&2
fi

MANIFEST="$DIR/yggdrasil.manifest.txt"
USERS="$(sed -n 's/^user=\(.*\)$/"\1"/p' "$MANIFEST" | tr '\n' ' ')"
[ -n "$USERS" ] || { echo "jit: no user= in manifest" >&2; exit 1; }
EVAL=false
if grep -qx 'needs-eval=true' "$MANIFEST"; then EVAL=true; fi

WORK="$OUT.build"
rm -rf "$WORK"
mkdir -p "$WORK/stage0" "$WORK/tools"

# The tools under the names the Makefile gives them, as the host has its own
# lowerer sealed under the real names (src/lowerer/README.md).
sed -e 's/shen-jit\.lower\./stage0.lower./g' -e 's/(shen-jit\.sealed? /(stage0.sealed? /g' \
    -e 's/(shen-jit\.inline-body /(stage0.inline-body /g' \
    "$SHEN_JIT/src/lowerer/lower.shen" > "$WORK/tools/lower.shen"
sed -e '/^(load "src\/lowerer\/host.shen")$/d' -e '/^(load "build\/tools\/lower.shen")$/d' \
    -e '/^(shen-jit\.stage0\.main (value \*argv\*))$/d' \
    "$SHEN_JIT/src/lowerer/stage0.shen" > "$WORK/tools/stage0.shen"

UNIVERSE=""
for f in toplevel core sys dict sequent yacc reader prolog track load writer macros declarations types t-star init; do
    UNIVERSE="$UNIVERSE \"$ROOT/KLambda/$f.kl\""
done

"$SHEN_JIT_BIN" eval \
    -l "$SHEN_JIT/src/lowerer/host.shen" \
    -l "$WORK/tools/lower.shen" \
    -l "$WORK/tools/stage0.shen" \
    -l "$ROOT/builders/jit/build.shen" \
    -e "(yggdrasil.jit.build-checked \"$DIR\" [$USERS] $EVAL [\"$SHEN_JIT/src/port/shen-jit.kl\" \"$LOWER_KL\"] [$UNIVERSE] \"$WORK/stage0\")" \
    > "$WORK/lower.log" 2>&1 || { cat "$WORK/lower.log" >&2; exit 1; }
# Exit 3: this target cannot express the program.
if [ -f "$WORK/stage0/jit-refused.txt" ]; then
    cat "$WORK/stage0/jit-refused.txt" >&2
    exit 3
fi
[ -s "$WORK/stage0/toplevel.sbc" ] || { cat "$WORK/lower.log" >&2; echo "jit: the lowerer wrote no code" >&2; exit 1; }

"$RUNTIME" --stage1 "$WORK/stage0" "$WORK/app.img"
cat "$RUNTIME" "$WORK/app.img" > "$OUT.tmp"
chmod +x "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
grep '^jit:' "$WORK/lower.log" >&2 | sort -u || true
