#!/usr/bin/env bash
# Yggdrasil — stage-2 Scheme (Chez) builder.
#
#   builders/scheme/build.sh <shaken-dir> <out-dir>
#
# <shaken-dir> is a (yggdrasil.shake ...) output (kernel.kl + user .kl +
# yggdrasil.manifest). Produces a self-contained Chez Scheme artifact at
# <out-dir>: the shaken kernel + user program compiled to Scheme with
# shen-scheme's own kl->scheme, plus the shen-scheme runtime (chez-prelude +
# primitives) and a `run` launcher (`chez --script app.scm`).
#
# Overridable environment:
#   SHEN_SCHEME - shen-scheme checkout (default: sibling ../shen-scheme)
#   SHEN_SCHEME_BIN - the shen-scheme launcher (default: $SHEN_SCHEME/_build/bin/shen-scheme)
#   CHEZ        - the Chez Scheme executable for `--script` (default: chez)
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <shaken-dir> <out-dir>" >&2
    exit 2
fi

RAT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHEN_SCHEME="${SHEN_SCHEME:-$(cd "$RAT_ROOT/../shen-scheme" && pwd)}"
SHEN_SCHEME_BIN="${SHEN_SCHEME_BIN:-$SHEN_SCHEME/_build/bin/shen-scheme}"
CHEZ="${CHEZ:-chez}"

DIR="$1"; OUT="$2"
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
mkdir -p "$OUT"

# The artifact carries no KL->Scheme compiler (app.scm never calls
# _scm.initialize-compiler), so an eval-capable slice cannot run: its first
# eval-kl dies with "variable kl:_scm.kl->scheme is not bound".  Refuse it
# up front as a capability SKIP (exit 3), which the CLI and the parity gate
# read as "this target does not support this program", not as a failure.
if grep -qx 'needs-eval=true' "$DIR/yggdrasil.manifest.txt"; then
    echo "scheme: needs-eval=true; this builder emits an eval-stripped artifact with no KL->Scheme compiler (capability SKIP)" >&2
    exit 3
fi

# The user .kl filename(s) from the manifest (one expected for build/run).
# Every user file, in manifest order, as a Shen list of strings.
USERS="$(sed -n 's/^user=\(.*\)$/"\1"/p' "$DIR/yggdrasil.manifest.txt" | tr '\n' ' ')"
[ -n "$USERS" ] || { echo "scheme: no user= in manifest" >&2; exit 1; }

# Stage 1 of the builder: compile shaken kernel.kl + user .kl -> Scheme using
# shen-scheme's own kl->scheme (run from the shen-scheme repo so its relative
# src/ loads resolve). Writes kernel.scm, user.scm, init.scm into OUT.
( cd "$SHEN_SCHEME" && "$SHEN_SCHEME_BIN" eval -q \
    -l src/compiler.shen -l scripts/build.shen \
    -l "$RAT_ROOT/builders/scheme/build.shen" \
    -e "(_scm.initialize-compiler)" \
    -e "(load-overrides)" \
    -e "(scm-rat-build \"$DIR\" [$USERS] \"$OUT\")" >/dev/null )

# Copy the shen-scheme runtime the compiled code links against: the host
# primitives, plus the port-specific OVERRIDES (pr, shen.char-stoutput?, dict
# ops, …) that the shake deliberately excludes (each port supplies its own).
cp "$SHEN_SCHEME/src/chez-prelude.scm"                  "$OUT/chez-prelude.scm"
cp "$SHEN_SCHEME/src/primitives.scm"                    "$OUT/primitives.scm"
cp "$SHEN_SCHEME/compiled/overrides.scm"                "$OUT/overrides.scm"
cp "$SHEN_SCHEME/compiled/shen-scheme-extensions.scm"   "$OUT/shen-scheme-extensions.scm"

# Global parameters + register-globals + a home-path stub (the C foreign proc
# is only in the linked shen-scheme binary; the eval-stripped artifact never
# needs the real one). Mirrors shen-scheme.scm.
cat > "$OUT/globals.scm" <<'SCM'
(define kl:global/shen.*infs* (make-parameter #f))
(define kl:global/shen.*call* (make-parameter #f))
(define kl:global/shen.*occurs* (make-parameter #f))
(define kl:global/shen.*special* (make-parameter #f))
(define kl:global/shen.*extraspecial* (make-parameter #f))
(define kl:global/shen.*demodulation-function* (make-parameter #f))
(define kl:global/shen.*gensym* (make-parameter #f))
(define kl:global/*stinput* (make-parameter #f))
(define kl:global/*stoutput* (make-parameter #f))
(define kl:global/*sterror* (make-parameter #f))
(define kl:global/*property-vector* (make-parameter #f))
(define kl:global/*macros* (make-parameter #f))
(define kl:global/_scm.*kl-prefix* (make-parameter #f))
(define kl:global/_scm.*global-prefix* (make-parameter #f))
(define kl:global/_scm.*yields-boolean1* (make-parameter #f))
(define kl:global/_scm.*yields-boolean2* (make-parameter #f))
(define (register-globals)
  (shen-global-parameter-set! 'shen.*infs* kl:global/shen.*infs*)
  (shen-global-parameter-set! 'shen.*call* kl:global/shen.*call*)
  (shen-global-parameter-set! 'shen.*occurs* kl:global/shen.*occurs*)
  (shen-global-parameter-set! 'shen.*special* kl:global/shen.*special*)
  (shen-global-parameter-set! 'shen.*extraspecial* kl:global/shen.*extraspecial*)
  (shen-global-parameter-set! 'shen.*demodulation-function* kl:global/shen.*demodulation-function*)
  (shen-global-parameter-set! 'shen.*gensym* kl:global/shen.*gensym*)
  (shen-global-parameter-set! '*stinput* kl:global/*stinput*)
  (shen-global-parameter-set! '*stoutput* kl:global/*stoutput*)
  (shen-global-parameter-set! '*sterror* kl:global/*sterror*)
  (shen-global-parameter-set! '*property-vector* kl:global/*property-vector*)
  (shen-global-parameter-set! '*macros* kl:global/*macros*)
  (shen-global-parameter-set! '_scm.*kl-prefix* kl:global/_scm.*kl-prefix*)
  (shen-global-parameter-set! '_scm.*global-prefix* kl:global/_scm.*global-prefix*)
  (shen-global-parameter-set! '_scm.*yields-boolean1* kl:global/_scm.*yields-boolean1*)
  (shen-global-parameter-set! '_scm.*yields-boolean2* kl:global/_scm.*yields-boolean2*))
(define-top-level-value 'get-shen-scheme-home-path (lambda () "."))
SCM

# The self-contained program. Order: runtime + baked defuns, then set the
# language vars + standard ports, register the globals, run (shen.initialise)
# and the user program (init.scm). No _scm.initialize-compiler: eval-stripped.
cat > "$OUT/app.scm" <<'SCM'
(import (chezscheme))
(include "globals.scm")
(include "chez-prelude.scm")
(include "primitives.scm")
(include "overrides.scm")
(include "shen-scheme-extensions.scm")
(include "kernel.scm")
(include "user.scm")
(kl:set '*language* "Scheme")
(kl:set '*implementation* "chez-scheme")
(kl:set '*release* (call-with-values scheme-version-number (lambda (a b c) (format "~s.~s.~s" a b c))))
(kl:set '*porters* "Bruno Deferrari")
(kl:set '*port* "0.50")
(register-globals)
(kl:global/*sterror* (standard-error-port))
(kl:global/*stinput* (standard-input-port))
(kl:global/*stoutput* (standard-output-port))
(include "init.scm")
(flush-output-port (standard-output-port))
(exit 0)
SCM

cat > "$OUT/run" <<SCM
#!/bin/sh
HERE="\$(cd "\$(dirname "\$0")" && pwd)"
cd "\$HERE"
exec "$CHEZ" --script app.scm "\$@"
SCM
chmod +x "$OUT/run"

echo "yggdrasil/scheme: built $OUT (run: $OUT/run)"
