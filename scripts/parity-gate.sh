#!/usr/bin/env bash
# Yggdrasil - behavioural parity gate over every fixture that has a golden.
#
#   scripts/parity-gate.sh [--targets a,b] [--fixtures f,g] [--min-targets N]
#                          [--time] [--keep]
#
# For each fixture in tests/ that has a committed golden (tests/<f>.expected)
# this shakes the program once and runs `yggdrasil parity` against that golden
# across the selected stage-2 targets.  A target whose toolchain is not on PATH
# reports SKIP and does not fail the gate; see docs/parity.md.
#
# This is NOT run by .github/workflows/go.yml.  A shake needs a Shen stage-1
# host and each target needs its own port checkout + toolchain, none of which
# the CI matrix has.  Run it locally (or from a self-hosted runner) before
# changing yggdrasil.shen, KLambda/, or a builder.
#
# Overridable environment:
#   YGGDRASIL_BIN  prebuilt CLI to use (default: build ./ into a temp dir)
#   plus every YGGDRASIL_SHEN_*_DIR the builders honour (see builders.json)
#
# --min-targets N fails the gate unless every gated fixture actually checked at
# least N targets.  Without it a runner that has quietly lost a toolchain still
# reports PASS, just over a smaller set -- a green run that gates less than the
# last one and says nothing about it.
#
# Exit status: 0 all checked fixtures passed; 1 any fixture failed, a fixture
#              checked fewer than --min-targets, or a KNOWN_GAPS entry has gone
#              stale (it now passes and must be removed); 3 nothing could be
#              checked on any fixture.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGETS=""
FIXTURES=""
TIME_FLAG=""
KEEP=0
MIN_TARGETS=0

while [ $# -gt 0 ]; do
    case "$1" in
        --targets)  TARGETS="$2"; shift 2 ;;
        --targets=*) TARGETS="${1#*=}"; shift ;;
        --fixtures) FIXTURES="$2"; shift 2 ;;
        --fixtures=*) FIXTURES="${1#*=}"; shift ;;
        --min-targets) MIN_TARGETS="$2"; shift 2 ;;
        --min-targets=*) MIN_TARGETS="${1#*=}"; shift ;;
        --time)     TIME_FLAG="--time"; shift ;;
        --keep)     KEEP=1; shift ;;
        -h|--help)  sed -n '2,22p' "$0"; exit 0 ;;
        *) echo "parity-gate: unknown argument $1" >&2; exit 2 ;;
    esac
done

# Known gaps: "<fixture>:<target>  <why>".  A target listed here is dropped from
# that fixture's gate run and probed separately.  Two rules keep the list from
# rotting into a place where failures go to hide:
#
#   * the probe is REPORTED every run, so an exclusion is never silent;
#   * if a probed gap starts PASSING the gate FAILS, demanding the line be
#     deleted.  An exclusion that outlives its cause is the bug this whole
#     script exists to catch.
# Two entries that lived here are gone, and both got out the same way: the
# gate's own stale-gap check started failing and demanded the deletion, which
# is the mechanism working as intended rather than a formality.
#
#   * metaeval:js went when #23 fixed it.
#   * four `kl` entries (fib, hello, parity, prolog) went when the shen-go pin
#     moved to 30ab469 (PR #48).  They had one cause.  `kl` joined the gate
#     when it stopped being a runner special-cased in trace.go and became a
#     builders.json entry (hickey-13): it runs the shaken KL on shen-go's bare
#     KLambda VM, with no stage-2 compiler in between, and up to shen-go
#     da55c5d that VM panicked while evaluating `(shen.initialise)` -- on
#     EVERY boot, of every fixture:
#
#       53 #> shen.initialise
#       54 #> Panic: &{22 implementation error in shen.change-pointer-value}
#       Recovered in Eval: (shen.initialise)
#       Error(goroutine 1 [running]: ...
#
#     then recovered and ran the next toplevel form, so the fixture's output
#     still appeared further down the transcript.  For a while that was
#     reported as a PASS, because `kl`'s stdout is compared by containment (it
#     is a REPL transcript, not the program's output) and containment is
#     satisfied by a transcript whose boot failed.  The entry declares
#     `transcript_error_markers` and both readers check them BEFORE the
#     containment test, so the gate started saying what was always true.
#     metaeval:kl failed a second time over: the eval-capable slice calls
#     `eval` at run time and the VM answered `Panic: &{22 package shen does
#     not exist.}`, so none of its three lines printed.
#
#     At shen-go 30ab469 neither happens.  Measured on this branch,
#     2026-09-23: `yggdrasil run tests/fib.shen OUT --target kl` and the same
#     on tests/metaeval.shen produce transcripts with zero `Panic:` /
#     `Recovered in Eval` / `goroutine ` lines; metaeval prints all three of
#     `eval list: 42`, `eval define: 42`, `eval string: 42`; and
#     `yggdrasil trace-check tests/fib.shen OUT --target kl` reports
#     `OK called=33` with `phase: boot=28 program=11`.  `kl` gates for real on
#     fib, hello, parity and prolog now, and a marker coming back is a
#     regression -- which TestTraceCheckFixtures (trace_test.go) asserts
#     rather than tolerates.
#
# metaeval:kl stays, with a DIFFERENT reason from the one it had.  The old
# reason ("the eval-capable slice prints none of its three lines") is dead:
# at 30ab469 the slice prints all three, in order, with no panic anywhere in
# the transcript.  What it fails on now is containment, and that is a property
# of `kl`'s declared stdout=repl-transcript rather than of the run.  The VM
# echoes each toplevel form's value, so the three answers arrive interleaved
# with prompts and echoes:
#
#   553 #> eval list: 42
#   "eval list: 42
#   "
#   554 #> #vector
#   555 #> eval define: 42
#   "eval define: 42
#   "
#   556 #> eval string: 42
#   "eval string: 42
#   "
#
# while tests/metaeval.expected is the three lines CONTIGUOUS, so
# `canon(golden)` is not a substring of `canon(transcript)` and both the
# vs-truth and two-boot legs report DIFFER.  fib/hello/parity/prolog are
# one-line-ish goldens and are unaffected.  Fixing this is a question about
# what containment should mean for a multi-line golden on a transcript
# target, not a shen-go bug, and nothing here should paper over it.
#
# A new entry belongs here only with the observed failure quoted in it.
KNOWN_GAPS='metaeval:kl  at shen-go 30ab469 the slice prints all three lines and the transcript carries no error marker; it fails containment because the kl VM interleaves its prompts and value echoes between them ("553 #> eval list: 42" / "\"eval list: 42\n\"" / "554 #> #vector" / ...) and tests/metaeval.expected is the three lines contiguous'

gap_reason() {  # gap_reason <fixture> <target> -> prints reason, or empty
    printf '%s\n' "$KNOWN_GAPS" | while IFS= read -r line; do
        [ -n "$line" ] || continue
        key="${line%%  *}"
        [ "$key" = "$1:$2" ] || continue
        printf '%s' "${line#*  }"
    done
}

gap_targets_for() {  # gap_targets_for <fixture> -> prints targets, one per line
    printf '%s\n' "$KNOWN_GAPS" | while IFS= read -r line; do
        [ -n "$line" ] || continue
        key="${line%%  *}"
        case "$key" in "$1:"*) printf '%s\n' "${key#*:}" ;; esac
    done
}

case "$MIN_TARGETS" in
    ''|*[!0-9]*) echo "parity-gate: --min-targets wants a non-negative integer, got '$MIN_TARGETS'" >&2
                 exit 2 ;;
esac

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ygg-parity.XXXXXX")"
cleanup() { [ "$KEEP" -eq 1 ] || rm -rf "$WORK"; }
trap cleanup EXIT

# The CLI.  Building it here (rather than trusting a stale ./yggdrasil on disk)
# is deliberate: a gate that silently exercises yesterday's binary proves
# nothing about today's tree.
BIN="${YGGDRASIL_BIN:-}"
if [ -z "$BIN" ]; then
    BIN="$WORK/yggdrasil"
    echo "parity-gate: building the CLI from $ROOT"
    go build -o "$BIN" . || { echo "parity-gate: go build failed" >&2; exit 1; }
fi

# Fixtures: an explicit list, else every tests/*.shen that has a golden beside
# it.  A fixture without a golden is reported and skipped -- the gate needs a
# committed truth source, and inventing one from the run under test would make
# the check vacuous.
if [ -n "$FIXTURES" ]; then
    IFS=',' read -r -a NAMES <<< "$FIXTURES"
else
    NAMES=()
    for f in tests/*.shen; do
        name="$(basename "$f" .shen)"
        # Fixtures the shaker is meant to REFUSE never produce an artifact,
        # so they are not parity material.  (They have no golden either,
        # which would skip them anyway; this says why.)
        case "$name" in init-order-bad) continue ;; esac
        # The two computed-name fixtures exist to VIOLATE the shake's
        # hypothesis that every name the artifact can call occurs
        # syntactically in it -- they are `yggdrasil trace-check` material,
        # not parity material, and they ship a golden because trace-check
        # compares the run's stdout against one.
        #
        #   computed-call  its SLICE cannot run at all, by construction: it
        #                  resolves shen.printF through (intern "shen.printF")
        #                  and the shake does not keep it.  Only the --full
        #                  artifact runs, which is the whole point.
        #   computed-read  its slice does run (checked on go), but its parity
        #                  across the other twelve targets has not been
        #                  measured, and a gate list is not the place to find
        #                  out.  `--fixtures computed-read` runs it on demand.
        case "$name" in computed-call|computed-read) continue ;; esac
        NAMES+=("$name")
    done
fi

declare -a PASSED=() FAILED=() NOGOLD=() UNCHECKED=() RESOLVED=() THIN=()

# Every known target, for subtracting known gaps when --targets was not given.
ALL_TARGETS="$("$BIN" targets | awk '{print $1}')"

for name in "${NAMES[@]}"; do
    prog="tests/$name.shen"
    gold="tests/$name.expected"
    if [ ! -f "$prog" ]; then
        echo "parity-gate: no such fixture: $prog" >&2
        exit 2
    fi
    if [ ! -f "$gold" ]; then
        NOGOLD+=("$name")
        continue
    fi

    echo
    echo "=== $name ==="

    # Selected targets for this fixture, minus any known gap.
    if [ -n "$TARGETS" ]; then
        sel="$(printf '%s\n' "$TARGETS" | tr ',' '\n')"
    else
        sel="$ALL_TARGETS"
    fi
    gaps="$(gap_targets_for "$name")"
    keep=""
    probe=""
    while IFS= read -r t; do
        [ -n "$t" ] || continue
        if printf '%s\n' "$gaps" | grep -qx -- "$t"; then
            probe="$probe $t"
        else
            keep="$keep,$t"
        fi
    done <<< "$sel"
    keep="${keep#,}"

    if [ -n "$keep" ]; then
        args=(parity "$prog" "$WORK/$name" --expect "$gold" --target "$keep")
        # A fixture may ship its own stdin.  This is what makes an eval-free
        # CLI gateable: its driver reads bytes, not S-expressions, so it stays
        # needs-eval=false -- see tests/stdin-sum.shen.
        [ -f "tests/$name.stdin" ] && args+=(--stdin "tests/$name.stdin")
        [ -n "$TIME_FLAG" ] && args+=("$TIME_FLAG")
        # tee, not a plain redirect: the run must stay visible in the log AND
        # be readable back for the --min-targets assertion.
        "$BIN" "${args[@]}" 2>&1 | tee "$WORK/$name.out"
        rc=${PIPESTATUS[0]}
        case $rc in
            0) PASSED+=("$name") ;;
            3) UNCHECKED+=("$name") ;;
            *) FAILED+=("$name") ;;
        esac
        if [ "$MIN_TARGETS" -gt 0 ] && [ "$rc" -eq 0 ]; then
            n=$(sed -n 's/^parity: PASS (\([0-9]*\) target(s) checked)$/\1/p' \
                    "$WORK/$name.out" | tail -1)
            if [ -z "$n" ]; then
                echo "    parity-gate: could not read the checked-target count for $name"
                THIN+=("$name:unreadable")
            elif [ "$n" -lt "$MIN_TARGETS" ]; then
                echo "    parity-gate: $name checked $n target(s), --min-targets is $MIN_TARGETS"
                THIN+=("$name:$n")
            fi
        fi
    else
        UNCHECKED+=("$name")
    fi

    # Probe each excluded target on its own.  Reported every run; a gap that
    # has started passing fails the gate so the exclusion gets deleted.
    for t in $probe; do
        echo
        echo "--- known gap probe: $name on $t ---"
        echo "    reason: $(gap_reason "$name" "$t")"
        "$BIN" parity "$prog" "$WORK/$name-probe-$t" --expect "$gold" --target "$t"
        case $? in
            0) echo "    KNOWN GAP RESOLVED: $name now passes on $t."
               echo "    Delete the '$name:$t' line from KNOWN_GAPS in $0 and re-run."
               RESOLVED+=("$name:$t") ;;
            3) echo "    not checkable ($t toolchain not on PATH); gap neither confirmed nor cleared" ;;
            *) echo "    gap still present (expected)" ;;
        esac
    done
done

echo
echo "=== parity gate summary ==="
printf 'passed:    %s\n' "${PASSED[*]:-(none)}"
printf 'failed:    %s\n' "${FAILED[*]:-(none)}"
printf 'unchecked: %s\n' "${UNCHECKED[*]:-(none)}"
printf 'no golden: %s\n' "${NOGOLD[*]:-(none)}"

if [ ${#THIN[@]} -gt 0 ]; then
    echo
    echo "parity-gate: FAIL - fewer than $MIN_TARGETS target(s) checked: ${THIN[*]}"
    echo "  A toolchain is missing or a target SKIPped.  This is a failure on purpose:"
    echo "  otherwise the run stays green while gating less than it used to."
    exit 1
fi

if [ ${#RESOLVED[@]} -gt 0 ]; then
    echo
    echo "parity-gate: FAIL - stale KNOWN_GAPS entries: ${RESOLVED[*]}"
    echo "  These now pass.  Remove them from KNOWN_GAPS so the target is gated"
    echo "  for real; an exclusion nobody removes is where the next failure hides."
    exit 1
fi

if [ ${#FAILED[@]} -gt 0 ]; then
    echo "parity-gate: FAIL"
    exit 1
fi
if [ ${#PASSED[@]} -eq 0 ]; then
    # Distinguish the two ways to check nothing.  Reporting "no toolchain" when
    # the real cause was "no golden" would send someone installing compilers to
    # fix a missing file.
    if [ ${#UNCHECKED[@]} -gt 0 ]; then
        echo "parity-gate: nothing checked (no selected target's toolchain is on PATH)"
    else
        echo "parity-gate: nothing checked (no selected fixture has a committed golden)"
    fi
    exit 3
fi
echo "parity-gate: PASS (${#PASSED[@]} fixture(s))"
