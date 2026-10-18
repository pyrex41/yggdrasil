# Runtime-load dependency closure, 2026-10-18

The shaken `tc-interp` C artifact now typechecks its runtime-loaded interpreter
and returns **`normal-form = 7`**. The source of a literal runtime `load` is
included in dependency discovery, recursively. Its forms remain runtime loads:
they are not evaluated while shaking or emitted early. This closes the `fix`
omission recorded in the original [RC integration](shen-c-rc-20261018.md).

Discovery over-approximates conditional loads, skips unavailable files, and
uses lexical path identity for `.`/`..` aliases. It keeps different files with
the same basename in different directories. Static and dynamic sources are
bootstrapped once for analysis, without repeating leading-load expansion.
Dynamic roots feed both the rules and worklist engines. This follow-up changes
no shared kernel files or other builders. Computed filenames or sources unavailable
at build time remain outside this static analysis; the runtime still needs
the source files it loads. Filesystem symlink identity is not canonicalized.

## Verification

The production patch is frozen at `34bd9d0`; `183f366` increases only the
bounded native test timeout and `fb753d5` selects the matching CI host pin. Darwin arm64 uses the pinned Shen-Go
`1f587de` stage-1 host and released Shen-C v0.3.0 compiler/runtime. No
full-kernel fallback was used. During qualification, `main` advanced to the
community 43.1 shared kernel at `8705799`. PR CI tests that combined tree; the
native workflow now reads `.github/shen-go.ref` from its actual checkout, so
the host and shared kernel stay matched. Local 42.2 receipts and CI results
for the inherited 43.1 upgrade are identified separately. Evidence from earlier commits remains identified
separately; documentation-only commits do not change the embedded program.

| Check | Result |
|---|---|
| Independent analysis-vs-shake, engine agreement, kernel order, static leading loads | **PASS** across all fixtures at `34bd9d0`; [receipt](evidence/dynamic-load-20261018/oracle-final.json) |
| Linux analysis oracle, including Souffle | **PASS**, [42.2 base](https://github.com/pyrex41/yggdrasil/actions/runs/39160640463) at `34bd9d0` and [inherited 43.1 base](https://github.com/pyrex41/yggdrasil/actions/runs/39161299457) at `fb753d5` |
| Real native manifest/init/arity and runtime-load controls | **PASS** independently at `34bd9d0`, including transitive/optional loads, alias cycles, same basenames and leading parent paths |
| Native Linux/macOS CI on inherited 43.1 base | **PASS**, [run 39161299449](https://github.com/pyrex41/yggdrasil/actions/runs/39161299449) at `fb753d5` |
| Preserved final native app, generated C and dependency audit | **PASS**, `normal-form = 7`; [source/binary/input receipt](evidence/dynamic-load-20261018/native-qualified.json) |
| Full default Go suite in isolated host-free source copy | PASS at earlier `0a4a142`; strict skip gates unchanged |
| Complete host-enabled Go CI and Linux/macOS/Windows host-free matrix | **PASS**, [42.2 base](https://github.com/pyrex41/yggdrasil/actions/runs/39160640395) at `34bd9d0` and [inherited 43.1 base](https://github.com/pyrex41/yggdrasil/actions/runs/39161073991) at `183f366` |

The final native CI checks out the synthetic merge `57ef3bb`, whose parents
are inherited `main` `8705799` and branch `fb753d5`, with Shen-C `27fe901` and
Shen-Go `acba88a`. The [merge identity](evidence/dynamic-load-20261018/pr-merge-identity.json)
and [job checkout receipt](evidence/dynamic-load-20261018/native-ci-inherited-43-checkout.json)
bind those revisions. The passing 43.1 Go matrix is at `183f366`; the only
subsequent source change is the native workflow's host-pin selection. Its
production program, builders and test assertions are identical at `fb753d5`.

The first optional-load implementation kept an unavailable filename in the
later bootstrap list. Independent native validation caught it. The first Linux
native CI also exposed a Clang 18 / LLVM 17 archive-plugin mismatch; CI now
pairs `clang-18` with `llvm-ar-18` on Linux and the host tools on macOS.

Broader CI at `03038c7` then caught a second problem that the targeted native
controls missed: static leading loads were expanded twice, and dynamic roots
were omitted from the worklist seeds. `34bd9d0` bootstraps the already-expanded
static list directly and supplies dynamic roots to both engines. The complete
independent oracle tests now pass, including `tla-election`, `tc-interp`, and
the leading-load regression. The earlier failing CI logs remain published;
earlier native passes alone did not qualify those broader paths. A separate
180-second local full-host attempt also timed out and is retained as UNKNOWN.

The first final Linux native run reached the third C compilation before the
aggregate three-minute test deadline expired. The bounded test budget is now
six minutes, with an eight-minute outer Go deadline. The next PR run exposed
the inherited 43.1 kernel/42.2 host mismatch; reading the shared host pin fixes
that CI pairing without altering assertions or kernel sources.

```sh
export YGGDRASIL_DYNAMIC_HOST=/path/to/pinned/shen-go
export YGGDRASIL_SHEN_C_DIR=/path/to/shen-c-v0.3.0
export YGGDRASIL_HOST="$YGGDRASIL_DYNAMIC_HOST"
CC=clang go test -tags shen_c -count=1 -timeout 8m \
  -run 'TestCBuilder|TestDynamicLoadClosure' -v .
go test -count=1 -timeout 8m \
  -run 'TestAnalysisOracleMatchesShake|TestFootprintEnginesAgree|TestFootprintOrderIsKernelLoadOrder|TestMultiFileLoadsAreHoisted' -v .
go build -o .bin/ygg .
.bin/ygg build tests/tc-interp.shen out-tc-native --target c --host "$YGGDRASIL_DYNAMIC_HOST"
(cd tests && ../out-tc-native/app-c/app)
```

The [publication index](evidence/dynamic-load-20261018/publication.json)
records original/published hashes, source/binary/input identities, final tests
and failed attempts. Raw apps and C sources remain under
`out-shen-c-rc-evidence/gap-closure/`. No performance or new release claim is
made. The original [native CI receipt](evidence/dynamic-load-20261018/native-ci.json)
is historical at `03038c7`, before the broader oracle correction.
