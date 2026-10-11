#!/bin/sh
# Yggdrasil - stage-2 builder for the bounded joy target.
#
#   builders/joy/build.sh <shaken-dir> <out.sji> [shen-joy]
#
# Exit status is the capability contract the CLI reads (docs/targets.md):
#   0  built
#   3  capability SKIP: the program is outside the bounded subset, either
#      refused by the lowerer (lower.py) or by `shen-joy compile` for a
#      reason that is a property of the PROGRAM (an unsupported construct,
#      an unknown name such as a kernel or stdlib call, a resource limit,
#      non-tail recursion the return-stack proof cannot bound, or a
#      construct that needs a larger profile than `core`)
#   1  a real build failure: the lowerer produced input the compiler could
#      not parse, an I/O error, or anything else
set -u

outdir=$1
image=$2
shen_joy=${3:-shen-joy}

python3 "$(dirname "$0")/lower.py" "$outdir" "$outdir/app-joy.sjk"
status=$?
[ "$status" -eq 0 ] || exit "$status"

log="$outdir/app-joy.compile.log"
"$shen_joy" compile --profile core "$outdir/app-joy.sjk" --output "$image" >"$log" 2>&1
status=$?
cat "$log"
if [ "$status" -ne 0 ]; then
    if grep -Eq 'compile error: +(Unsupported_Construct|Unknown_Name|Limit_Exceeded|construct requires)|call arity' "$log"; then
        echo "yggdrasil joy target: outside the bounded subset (capability SKIP)" >&2
        exit 3
    fi
    exit 1
fi
exit 0
