#!/bin/sh
set -eu

outdir=$1
image=$2
shen_joy=${3:-shen-joy}
python3 "$(dirname "$0")/lower.py" "$outdir" "$outdir/app-joy.sjk"
"$shen_joy" compile --profile core "$outdir/app-joy.sjk" --output "$image"
