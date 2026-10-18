#!/usr/bin/env bash
# Yggdrasil - stage-2 Common Lisp builder.
#
#   builders/lisp/build.sh <shaken-dir> <output-exe>
#
# <shaken-dir> is a directory produced by (yggdrasil.shake ...): kernel.kl,
# user .kl files and yggdrasil.manifest.  Produces a self-contained native
# executable at <output-exe>.
#
# Overridable environment:
#   LISP_IMPL - sbcl (default) | clisp | ecl
#               (ccl unsupported: no native Apple Silicon build)
#   LISP_BIN  - the implementation binary (default: $LISP_IMPL on PATH;
#               sbcl also honors legacy SBCL_BIN)
#   SHEN_BIN  - shen-cl binary           (default: $SHEN_CL/bin/sbcl/shen)
#   SHEN_CL   - shen-cl checkout         (default: sibling ../shen-cl)
#   YGGDRASIL_SBCL_COMPRESSION - sbcl only: set to 1 to save the image with
#               :compression t (needs an SBCL with :sb-core-compression;
#               the build fails by name otherwise)
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <output-exe>" >&2
    exit 2
fi

RAT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_CL="${SHEN_CL:-$(cd "$RAT_ROOT/../shen-cl" && pwd)}"
SHEN_BIN="${SHEN_BIN:-$SHEN_CL/bin/sbcl/shen}"
LISP_IMPL="${LISP_IMPL:-sbcl}"

case "$LISP_IMPL" in
    sbcl)  LISP_BIN="${LISP_BIN:-${SBCL_BIN:-sbcl}}" ;;
    clisp) LISP_BIN="${LISP_BIN:-clisp}" ;;
    ecl)   LISP_BIN="${LISP_BIN:-ecl}" ;;
    *) echo "unsupported LISP_IMPL: $LISP_IMPL (sbcl|clisp|ecl)" >&2; exit 2 ;;
esac

DIR="$1"
EXE="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$EXE" in /*) ;; *) EXE="$PWD/$EXE" ;; esac

# Stage: compile KL -> Lisp, copy the shen-cl runtime + driver into DIR.
cd "$RAT_ROOT"
"$SHEN_BIN" eval -q \
    -l yggdrasil.shen \
    -l builders/lisp/build.shen \
    -e "(set lsp.*shen-cl* \"$SHEN_CL/\")" \
    -e "(lsp.build \"$DIR\" \"$EXE\")"

# Shen CL 3.1+ splits the dictionary backend out of primitives.lsp.
# Older fork runtimes keep it inline; support both layouts.
if [ -f "$SHEN_CL/src/dictionaries.lsp" ]; then
    cp "$SHEN_CL/src/dictionaries.lsp" "$DIR/dictionaries.lsp"
else
    rm -f "$DIR/dictionaries.lsp"
fi

# Build: load the runtime + shaken kernel + user code, initialise, emit the
# executable (saved image on sbcl/clisp; compiled-and-linked binary on ecl).
cd "$DIR"
case "$LISP_IMPL" in
    sbcl)  "$LISP_BIN" --non-interactive --no-userinit --no-sysinit --load driver.lsp ;;
    clisp) "$LISP_BIN" -norc -q driver.lsp ;;
    ecl)   "$LISP_BIN" --norc --load driver.lsp ;;
esac

rm -f "$DIR"/*.fasl "$DIR"/*.fas "$DIR"/*.lib "$DIR"/*.o
echo "yggdrasil/lisp: built $EXE ($LISP_IMPL)"
