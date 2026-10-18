# Community Shen 43.1 integration

The shared shake universe now uses community ShenOSKernel 43.1, tag
`shen-43.1`, commit `715824a52b55e9c81a9501df8548ff3edd8411f5`.
The release archive SHA-256 is
`900eedef44f440af78f9395ed06e689ce1604546fc309f5bc7c8bc84cc651920`.
All sixteen vendored core files match the release byte for byte. See
[provenance](../KLambda/PROVENANCE.md) for the universe boundary.

Stage 1 requires a matching 43.1 host. Loading the shaker on 42.2 raises
`Yggdrasil requires a community Shen 43.1 stage-1 host` before producing a
slice. The callgraph cache name and both manifest formats record 43.1. CI's
shared Go host pin is `acba88a3f189f055285a9bdb22901ad688db7cd3`.
The reference Lisp host is Shen CL 3.2.0, fork commit `d9494df`.

The kernel now calls public `char-stream?`, `read-unit-string` and
`write-string`. Byte-only ports may report false for character capability;
unsupported character operations must raise catchable errors without reading
from a byte stream. These names replace the former internal hooks in primitive
classification and the guarded optional primitive list.

The Lisp builder stages `dictionaries.lsp` when supplied by Shen CL and loads
it before native functions and the shaken kernel. It removes a stale staged
copy when building an older compatible source layout. The SBCL compiler uses
`:block-compile nil`, matching the port's build. CLISP/ECL loader paths retain
conditional dictionary inclusion; they have not been run in this qualification.

## Local checks, 2026-10-18

- Shared-kernel rules oracle, footprint, initialization-order and typecheck
  gates pass against the new universe and the reference host.
- Lisp/Lua parity passes all ten committed fixtures against their goldens,
  with at least two checked targets per fixture. This includes metaeval,
  Prolog, stdin readers and the new structural dictionary regression.
- The dictionary fixture verifies numeric structural keys, replacement count,
  a stored empty list, removal and count in a standalone Lisp artifact.
- Lisp/Scheme, Lisp/Swift and Lisp/Truffle each pass fib, hello, the
  dictionary fixture and stdin read-data with both targets checked.
- The Go 43.1 launcher also works as a stage-1 host: fib, Prolog, metaeval
  and read-data pass on the Go stage-2 target, including two boots and stdin.
- An old 42.2 host is rejected by the explicit kernel guard.

The hosted Go suite is complete across two runs: **131 top-level tests pass**,
with only the helper subprocess test skipped at top level. A first run
exceeded its 280-second budget after forty top-level tests passed. The
remaining tests completed in 843.898 seconds with a 25-minute budget,
including lowering, trace, SCIP, manifest and reader gates. The KL target's
stdin-sum subtest retains its declared stdin capability skip. The separate
`shen_c` build-tag tests are outside this run; native C qualification is not
inferred from these results.

## Reproduce

Build the CLI with the selected Go toolchain. Point `YGGDRASIL_HOST` at the
matching host, set the target source directories and provide SBCL/LuaJIT:

```sh
go build -o yggdrasil .
export YGGDRASIL_HOST=/path/to/shen-cl/bin/sbcl/shen
export YGGDRASIL_SHEN_CL_DIR=/path/to/shen-cl
export YGGDRASIL_SHEN_LUA_DIR=/path/to/shen-lua
scripts/parity-gate.sh --targets lisp,lua --min-targets 2
```

The gate checks committed expected output and repeated boots; availability
skips do not satisfy `--min-targets`. Exact checked revisions and compressed,
portable logs are retained under `docs/evidence/ports-43.1/`.
