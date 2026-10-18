# Runtime-load dependency closure, 2026-10-18

The shaken `tc-interp` C artifact now typechecks its runtime-loaded interpreter
and returns **`normal-form = 7`**. The source of a literal runtime `load` is
included in dependency discovery, recursively. Its forms remain runtime loads:
they are not evaluated while shaking or emitted early. This closes the `fix`
omission recorded in the original [RC integration](shen-c-rc-20261018.md).

Discovery over-approximates conditional loads, skips unavailable files, and
uses lexical path identity for `.`/`..` aliases. It keeps different files with
the same basename in different directories. Dynamic sources are bootstrapped
once for analysis, without repeating the static leading-load expander. The
shared 42.2 kernel and other builders are unchanged. Computed filenames or
sources unavailable at build time remain outside this static analysis; the
runtime still needs the source files it loads. This is not a proof of closure
for arbitrary runtime-generated code or filesystem symlink aliases.

## Verification

Final production code is in `0a4a142`, followed by a CI-tool pairing change.
Darwin arm64 uses the pinned Shen-Go `1f587de` stage-1 host and released
Shen-C v0.3.0 compiler/runtime. No full-kernel fallback was used.

| Check | Result |
|---|---|
| Real native manifest/init/arity test | PASS |
| Shaken `tc-interp`, exact semantic oracle | PASS, `normal-form = 7` |
| Transitive loads, optional missing load and alias cycle | PASS |
| Distinct same-basename files and leading `../../` paths | PASS; both kernel dependency sets retained |
| Independent final tagged tests | PASS, including both nested subtests |
| Preserved native app, generated C and dependency audit | PASS, source/binary/input hashes recorded |
| Full default Go suite in isolated host-free source copy | PASS |
| Original `23a2f39` full host-enabled CI | PASS, run 39156111660; historical baseline |
| Current complete host-enabled local suite | UNKNOWN: 180-second run timed out after detecting a sibling host |
| Focused Linux/macOS native CI | **PASS**, [run 39159617164](https://github.com/pyrex41/yggdrasil/actions/runs/39159617164) at `03038c7` |

The first optional-load implementation failed independent parent validation:
it kept the missing filename in the later bootstrap list. That failed log is
retained alongside the corrected full subtest run. The first expanded Linux
CI lane also exposed a Clang 18 / LLVM 17 archive-plugin mismatch. CI now pairs
`clang-18` with `llvm-ar-18` on Linux, and the host tools on macOS.

```sh
export YGGDRASIL_DYNAMIC_HOST=/path/to/pinned/shen-go
export YGGDRASIL_SHEN_C_DIR=/path/to/shen-c-v0.3.0
CC=clang go test -tags shen_c -count=1 -timeout 5m \
  -run 'TestCBuilder|TestDynamicLoadClosure' -v .
go build -o .bin/ygg .
.bin/ygg build tests/tc-interp.shen out-tc-native --target c --host "$YGGDRASIL_DYNAMIC_HOST"
(cd tests && ../out-tc-native/app-c/app)
```

[Native artifact receipt](evidence/dynamic-load-20261018/native-receipt.json)
and [publication index](evidence/dynamic-load-20261018/publication.json) retain
original/published hashes, final tests and rejected attempts. Raw app and C
sources remain under `out-shen-c-rc-evidence/gap-closure/`. No performance or
new release claim is made.

The [native CI receipt](evidence/dynamic-load-20261018/native-ci.json) records both
passing jobs. Later changes are documentation only; embedded source and builders
are unchanged from the tested commit.
