# Behavioural parity gate

Yggdrasil's headline guarantee is that all eight stage-1 host ports emit a **byte-identical**
`kernel.kl` + manifest and portable user KL. That proves the *slice* is the same
bytes everywhere. It does **not** prove the slice *computes the same thing* on
each target.

Identical KL can still **execute** differently per host:

- fixed-width vs arbitrary-precision integers,
- symbol interning that resets (or doesn't) between calls,
- hash-map / dictionary iteration order,
- content-addressed memoisation that grows or returns stale entries.

A program that leans on any of these — content hashing, memo tables, big
integers — can pass every byte-identity check and still return wrong,
boot-order-dependent answers, or degrade in-session, on one target. This is
exactly what **shen-cas** hit on the shen-rust target (GitHub issue #8, and
shen-cas#5 / #6): correct and deterministic under ShenScript, silently wrong and
slowing down under shen-rust — from the *same* shaken KL.

The parity gate closes that gap: it runs a shaken slice through each stage-2
target and diffs the rendered output against a reference and against itself.

## Usage

```bash
yggdrasil parity PROG OUTDIR [--target a,b] [--reference R] [--expect FILE] [--time]
```

- `--target a,b` — comma-separated targets to check. Default: every target whose
  toolchain is on `PATH`; the rest are reported `SKIP`, never `FAIL`.
- `--reference R` — the target whose output is the truth source when no `--expect`
  is given (default `lisp` = shen-cl, the reference runtime). It is built and
  added to the checked set automatically.
- `--expect FILE` — a golden stdout file. When given it is the **authoritative**
  truth and every target is diffed against it. Capture it once from a trusted
  run (the reference host / pre-shake output) and commit it.
- `--time` — append per-target wall-clock to the table. Advisory only; timing
  never fails the gate (thresholds are too environment-dependent to gate CI).

Exit status: `0` all checked targets pass, `1` any failure, `3` nothing could be
checked (every selected toolchain missing).

## What it checks

For each built target the artifact is run **twice as two separate processes**
(`bootA`, `bootB`):

| column | meaning | catches |
|---|---|---|
| `vs-truth` | `bootA` == the truth output | cross-target divergence (the shen-cas case) |
| `two-boot` | `bootA` == `bootB` | cross-boot nondeterminism (e.g. live state in a saved image) |
| `two-pass` | the two in-process passes are equal (see below) | boot-order / state-dependent nondeterminism in one process (shen-cas#5) |

On any mismatch the gate prints the first differing line for that check.

## The two-pass fixture convention

The `two-pass` check is target-agnostic — no runtime hooks — because it relies on
a convention in the fixture itself:

> **A parity fixture runs the same work twice in one process and prints two
> identical passes separated by a line that is exactly `===`.**

The gate splits the artifact's stdout on that `===` line and compares the two
halves. If they differ, the program computed different answers the second time
through the *same* process — the shen-cas#5 failure shape. A fixture without a
`===` line simply reports `two-pass = N/A` (it is not failed).

`tests/parity.shen` is the reference fixture: a tiny content-addressed memo
(keyed by `(hash Term ...)`) whose printed values are a pure function of the
input, so a correct implementation is deterministic (the golden in
`tests/parity.expected` is stable) while a host whose hashing/interning
misbehaves returns stale cached values and diverges. It stays non-eval so it
shakes to the small (~100-defun) slice.

## The committed fixtures and the gate script

Every fixture in `tests/` that has a `tests/<name>.expected` beside it is gated.
The goldens were captured once from the reference target (`--target lisp`,
shen-cl on sbcl) and their values are independently checkable by reading the
fixture, which is the point of keeping them small:

| fixture | golden | exercises |
|---|---|---|
| `hello` | `hello from shaken shen` | output + strings, the minimal slice |
| `fib` | `fib 20 = 6765` | recursion + arithmetic |
| `prolog` | `mary likes chocolate: true` | drags in the Prolog engine |
| `metaeval` | three lines of `42` | `needs-eval=true`; keeps the reader (js: see known gaps) |
| `parity` | `tests/parity.expected` | content-addressed memo; the only `===` fixture |

`scripts/parity-gate.sh` runs all of them:

```bash
scripts/parity-gate.sh                          # every fixture, every target on PATH
scripts/parity-gate.sh --targets go,lua,rust,js # a subset
scripts/parity-gate.sh --fixtures fib,prolog --time
```

It builds the CLI from the working tree rather than trusting a `./yggdrasil` on
disk, shakes each fixture, and diffs every selected target against the
committed golden. A fixture with no golden is listed under `no golden` and
skipped — the gate will not mint a truth source from the run it is checking.
Exit status: `0` pass, `1` any fixture failed, `3` nothing was checkable.

Fixtures that carry no golden are listed under `no golden` and skipped. The
`typed-*` fixtures belong to the `check` gate (`check_test.go`) and
`interpreter` / `tc-interp` are the needs-eval typecheck slice used by the C
target, so none of them is a parity fixture; they show up in that line by
design.

### Known gaps

`KNOWN_GAPS` in the script records a fixture/target pair that cannot currently
be gated, with the reason and its issue. Such a target is dropped from that
fixture's run and **probed separately**, so the exclusion is printed on every
run rather than quietly shrinking the gate. If a probed gap starts passing the
gate **fails**, naming the line to delete — an exclusion that outlives its
cause is exactly where the next real failure would hide.

The one entry today is `metaeval:js` (pyrex41/yggdrasil#23): ShenScript
supports `needs-eval=true` only in `--linked` mode and `builders.json` has no
way to select it, so the js target cannot build any eval-capable program.
`metaeval` is gated on go, lua and rust.

**This is not run by CI** (pyrex41/yggdrasil#24). `.github/workflows/go.yml`
builds and unit-tests the Go CLI on three OSes, but a shake needs a Shen
stage-1 host and each target needs its own port checkout plus toolchain — none
of which the hosted matrix has. Run the script locally (or from a self-hosted
runner) before changing `yggdrasil.shen`, `KLambda/`, or a builder.

## Writing your own oracle

To gate your own program, make a fixture that:

1. computes its cases as a pure function of fixed inputs (so the output is
   deterministic on any correct implementation),
2. runs the whole batch **twice** in `main`, printing a `===` line between the
   two passes,
3. exercises the host-execution axis you care about (hashing, big integers,
   dict iteration), so a divergent host actually changes the output.

Then capture the golden once and gate it:

```bash
yggdrasil run    myprog.shen out/ --target lisp > myprog.expected   # capture once, from the reference
yggdrasil parity myprog.shen out/ --expect myprog.expected           # gate every target against it
```

## Limitations

- The gate compares **rendered stdout**. Behaviour a program never prints is not
  checked — make the oracle print what matters.
- `--reference lisp` builds shen-cl, which needs `$SHEN_CL` (or a sibling
  `../shen-cl` checkout) and `sbcl`. Without them, pass `--expect` instead.
- `--time` is reporting only; it surfaces gross in-session degradation (shen-cas#6)
  but does not assert a threshold.
