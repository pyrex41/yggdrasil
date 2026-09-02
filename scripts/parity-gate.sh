#!/usr/bin/env bash
# Yggdrasil - behavioural parity gate over every fixture that has a golden.
#
#   scripts/parity-gate.sh [--targets a,b] [--fixtures f,g] [--time] [--keep]
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
# Exit status: 0 all checked fixtures passed, 1 any fixture failed OR a
#              KNOWN_GAPS entry has gone stale (it now passes and must be
#              removed), 3 nothing could be checked on any fixture.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGETS=""
FIXTURES=""
TIME_FLAG=""
KEEP=0

while [ $# -gt 0 ]; do
    case "$1" in
        --targets)  TARGETS="$2"; shift 2 ;;
        --targets=*) TARGETS="${1#*=}"; shift ;;
        --fixtures) FIXTURES="$2"; shift 2 ;;
        --fixtures=*) FIXTURES="${1#*=}"; shift ;;
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
KNOWN_GAPS='metaeval:js  builders.json cannot select ShenScript --linked, the only mode that supports needs-eval=true (pyrex41/yggdrasil#23)'

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
        NAMES+=("$(basename "$f" .shen)")
    done
fi

declare -a PASSED=() FAILED=() NOGOLD=() UNCHECKED=() RESOLVED=()

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
        [ -n "$TIME_FLAG" ] && args+=("$TIME_FLAG")
        "$BIN" "${args[@]}"
        case $? in
            0) PASSED+=("$name") ;;
            3) UNCHECKED+=("$name") ;;
            *) FAILED+=("$name") ;;
        esac
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
