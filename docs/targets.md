# Targets and hosts

Stage 1 shakes a Shen program into a KLambda slice. Stage 2 hands that slice to
a per-target builder, which compiles it with the target port's own KL compiler.
This page covers the builder contract, the targets, and the Shen hosts stage 1
itself can run on.

## The stage-2 builder contract

A builder is given a shake output directory (`kernel.kl`, one `.kl` per user
file, and the two manifests) and must:

1. Load every defun in `kernel.kl`.
2. Call `(shen.initialise)`.
3. Run each user file's forms in manifest (`user=`) order.

Step 3 is not just defun loading. A user file carries defuns *and* toplevel
expressions, and both must execute in source order.

S42 has no `shen.initialise` of its own: global initialisation is toplevel forms
interleaved through the kernel files. The shake collects those forms and
synthesises `(defun shen.initialise () ...)`, so the contract above is the same
one a builder would write against a kernel that had one.

Builders must ignore manifest keys they do not recognise. New keys are added to
the manifest as the shaker gains checks, and a builder that fails on an unknown
key breaks on an upgrade that did not concern it.

Build and run recipes are data in [`builders.json`](../builders.json), read by
the CLI and by Bifrost's shake-then-run mode. `yggdrasil targets` prints each
target with the port it runs on and the tools it needs on PATH.

## The targets

Paths below are relative to the sibling port checkout unless they start with
`builders/`, which is this repository.

| target | builder | artifact | approximate size and startup |
|---|---|---|---|
| `c` | `builders/c/build.sh <dir> <outdir>` (`SHEN_C`) | `app.c` + Makefile + CMakeLists, then `make` links `libshenc.a` into `<outdir>/app` | |
| `erlang` | `builders/erlang/build.sh <dir> <outdir>` (`SHEN_ERL`) | slice compiled to BEAM plus the small shen-erl runtime and a `run` launcher; needs Erlang/OTP at run time but never boots the full kernel | |
| `go` | `shen-go/cmd/yggdrasil-build <dir> <outdir>`, then `go build` | static binary | ~4.5 MB, under 10 ms; cross-compiles to linux and windows |
| `joy` | `builders/joy/build.sh <dir> <out.sji> [shen-joy]` | deterministic shen-joy image v1, run by the bounded allocation-free device VM | |
| `js` | `node ShenScript/bin/yggdrasil-build.js <dir> <out.js>` | self-contained ES module on Node 20+, Bun or Deno 2 | ~120 KB |
| `julia` | `julia --project=shen-julia shen-julia/bin/yggdrasil-build.jl <dir> <outdir> --sysimage` | artifact project; with `--sysimage` a per-program sysimage, otherwise a lib-mode `.jl`. Kernel and user defuns are baked as module methods, the same AOT technique as shen-julia's own fast boot | sysimage ~266 MB, ~0.15 s warm; lib mode ~4 s |
| `kl` | `go build ./cmd/kl` in the shen-go checkout, then `yggdrasil program-file` concatenates `kernel.kl`, `(shen.initialise)` and the user files into one KL stream | the slice run directly on shen-go's bare KLambda VM, no stage-2 compiler in between | |
| `lisp` | `builders/lisp/build.sh <dir> <exe>` (`LISP_IMPL=sbcl\|clisp\|ecl`, `SHEN_CL`) | saved image, or a linked executable under ECL | SBCL ~36 MB, CLISP ~7.8 MB, ECL ~620 KB plus libecl |
| `lua` | `luajit shen-lua/bin/yggdrasil-build.lua <dir> <out.lua>` | self-contained `.lua` | ~640 KB, ~25 ms |
| `rust` | `cargo run --release -p yggdrasil-build -- <dir> <outdir>`, then `cargo build --release` | static binary | ~9 MB, ~40 ms |
| `scheme` | `builders/scheme/build.sh <dir> <outdir>` (`SHEN_SCHEME`) | Scheme program dir plus a `run` launcher (`chez --script`), compiled with shen-scheme's own `kl->scheme`; overridden kernel functions (`pr`, `shen.char-stoutput?`, dict ops) come from its `overrides.scm`, exactly as its own build does | |
| `swift` | `builders/swift/build.sh <dir> <outdir>` (`SHEN_SWIFT`) | the slice plus a `run` launcher driving the shen-swift tree-walking interpreter in `--shaken` mode | |
| `truffle` | `mvn -DskipTests package` in shen-truffle, then `java -jar target/yggdrasil-builder.jar --format jvm --runtime target/shen-truffle.jar <dir> <out>` | relocatable JVM application at `app-truffle/bin/shen-truffle` | |
| `truffle-native` | the same builder with `--format native` | platform-native executable `app-truffle-native` | |

Three targets are references rather than compilations. shen-swift, shen-lua and
shen-julia artifacts reference their port's runtime rather than embedding a
generated program; shen-swift in particular is an interpreter, so there is
nothing to code-generate and the win is boot speed, a roughly 200-line shaken
kernel against the full 2,500-line one.

### Toolchains each target needs on PATH

| target | needs |
|---|---|
| `c` | `cc`, `pkg-config`, `make` |
| `erlang` | `erl`, `erlc`, `make`, `cc`, `curl`, `unzip`, `tar`, `shasum` |
| `go`, `kl` | `go` |
| `joy` | `python3` |
| `js` | `node` |
| `julia` | `julia` |
| `lisp` | `sbcl` |
| `lua` | `luajit` |
| `rust` | `cargo` |
| `scheme` | `chez` |
| `swift` | `swift` |
| `truffle` | `java`, `mvn` |
| `truffle-native` | `java`, `mvn`, `native-image` |

A target whose tools are absent is skipped rather than failed.

### The `js` target and eval

ShenScript's default mode is fully self-contained but refuses an eval-capable
slice, because the artifact would need the compiler inside it. The builder
selects its step on the manifest: `--linked` when `needs-eval=true`, plain
otherwise. `--linked` carries the compiler but imports `lib/` from the
ShenScript checkout, so a linked artifact is not portable off the build machine.

`yggdrasil build ... --target js --web` emits a browser-safe ES module instead
of the Node artifact: no `node:fs`, no streams, no `process`. Import it as
`import $ from './app.js'; $.caller('fn')(...)`. `--web` and `--linked` are
mutually exclusive, so an eval-capable program cannot be built for the browser.

### The `kl` target's output shape

The bare VM reads its program from stdin and prints a numbered prompt, echoing
each toplevel form's value, so the program's output is embedded in a REPL
transcript rather than being the whole of stdout. Goldens are checked against a
`kl` run by containment, and a transcript carrying one of the VM's declared
error markers (`Panic:`, `Recovered in Eval`, `goroutine `) fails even when the
golden is contained in it. Anything on the caller's stdin arrives *after* the
program text, as further toplevel forms, so `kl` is refused by name for a
fixture that supplies stdin.

## The bounded joy target

The `joy` target deliberately does not load the shaken Shen kernel. Its host
lowerer (`builders/joy/lower.py`) reads only the manifest-listed user KLambda,
emits normalised input, and invokes `shen-joy compile --profile core`; the
resulting `.sji` is the deployment artifact.

Accepted: first-order fixnum and boolean code with `cond`, `if`, `let`, `do`,
direct calls and tail calls, the operators `+ - * = < >` and `not`, the list
primitives `cons`, `hd`, `tl` and `cons?`, and exactly one top-level
`(output "~A~%" VALUE)`.

Rejected: `eval` and `eval-kl`, closures (`lambda`, `freeze`), exceptions
(`trap-error`), streams (`open`, `close`, `read-byte`, `write-byte`), mutable
globals (`set`, `value`), `intern`, `absvector`, any `shen.`-prefixed call,
string literals as values, arbitrary formatting, and non-tail recursion.

A rejection exits with status **3**. The CLI reads that one status as a
capability limit rather than a build failure, so parity reports a SKIP instead
of a false failure or a silently approximated semantics.

```bash
yggdrasil run tests/joy-sum.shen out/ --target joy
```

The builder finds the compiler at `$SHEN_JOY_BIN`, then `shen-joy` on PATH, then
`build/shen-joy` or `result/bin/shen-joy` under the sibling shen-joy checkout.

## The lisp builder

`LISP_IMPL` selects the implementation: `sbcl` (default), `clisp` or `ecl`.
`SHEN_BIN` overrides the shen-cl binary, `LISP_BIN` the Lisp itself. CCL is
unsupported, because no native Apple Silicon build exists.

Three implementation details cost real debugging and are still live:

- shen-cl's native `pr` override is `#+(or ccl sbcl)`, so other implementations
  need the optional stream primitives (`shen.write-string` and friends). The
  driver installs portable fallbacks when they are missing.
- Streams captured in a saved image are dead on restart under CLISP, so the
  image toplevel rebinds `*stoutput*` and `*stinput*` at startup.
- ECL cannot dump images at all. The driver compiles each module to an object
  file and links a real executable with `c:build-program`, replaying boot at
  program startup.

When the manifest says `needs-eval=true` the builder additionally stages
shen-cl's precompiled `compiled/compiler.lsp` into the artifact, so `eval-kl`
works at run time.

## The C target

Shake first, then build. The shaker is Shen, not C.

```bash
yggdrasil build tests/fib.shen out/ --target c
# or, by hand, after a shake into out/:
builders/c/build.sh out out/app-c && out/app-c/app
```

Each shaken `defun` becomes a C `NativeFunction` on `shen_context` over Boehm
GC. It is not `eval_kl_object` of the source string, and not Chicken-style
compilation onto the C stack. The builder emits `app.c`, a Makefile and a
CMakeLists, then `make` links `libshenc.a` into `<outdir>/app`. It builds
shen-c's `bin/libshenc.a` and `bin/yggdrasil-build` first if they are missing.

Boehm GC must come from Nix's `pkg-config`, not Homebrew. When a Homebrew
`bdw-gc` is what `pkg-config` finds, `builders/c/build.sh` re-enters
`nix develop` in the shen-c flake; if `nix` is unavailable it prints the command
to run and exits 1.

`tests/hello.shen` and `tests/fib.shen` build and print their expected output.
`tests/tc-interp.shen` is the needs-eval typecheck slice (`(tc +)` plus
`load interpreter.shen`), whose `kernel.kl` keeps `t-star`, `types`, the reader
and `load`, around 568 defuns. Run that app from a directory containing
`tests/interpreter.shen`. Kernel `declare` tables run as NativeFunctions, but
`load interpreter.shen` currently traps in `macroexpand`/`walk`.

shen-c is not a stage-1 host. Its launcher has `-l` and `-e` and its `*hush*` is
stdout-only, but `yggdrasil.shake` on shen-c is unverified; the CLI's host
fallback deliberately skips it.

## Running stage 1 on each host

Stage 1 is pure Shen, but it compiles your program to KLambda with the host's
`bootstrap` compiler, so the host must emit fully portable KL. Eight ports do:
they produce a byte-identical `kernel.kl` and manifest against the shen-cl
reference, with user KL identical modulo gensym numbering.

The expression is always the same; only the launcher syntax differs. Write
`SHAKE` for `'(yggdrasil.shake ["prog.shen"] "out")'`.

| host | command | note |
|---|---|---|
| shen-cl | `shen eval -q -l yggdrasil.shen -e SHAKE` | reference host, and the fastest |
| shen-go | `shen eval -q -l yggdrasil.shen -e SHAKE` | the CLI's fallback host when shen-cl is missing |
| shen-lua | `bin/shen yggdrasil.shen -e SHAKE` | omit `-q` (see below) |
| shen-rust | `shen-rust eval -l yggdrasil.shen -e SHAKE` | omit `-q`. Runs the deep call-graph walk on a 1 GB-stack thread |
| shen-erl | `shen-erl eval -q -l yggdrasil.shen -e SHAKE` | |
| ShenScript | `node bin/shen.js eval -l yggdrasil.shen -e SHAKE` | slowest host, roughly 25 s |
| shen-julia | `shen-julia/bin/shen eval -l yggdrasil.shen -e SHAKE` | pre-create the output directory; the shake does not `mkdir` |
| shen-swift | `shen-swift/.build/release/shen-swift eval -q -l yggdrasil.shen -e SHAKE` | tree-walking KLambda interpreter, iOS-capable |

The **`*hush*` caveat**: `-q` sets `*hush*`, and on **shen-lua** and
**shen-rust** that silences the `pr` writes to the output files, producing
zero-byte artifacts. Omit `-q` on those two. shen-cl, shen-go, shen-erl,
ShenScript, shen-julia and shen-swift route `pr` to file streams regardless of
`*hush*`, so `-q` is harmless there. Dropping `-q` everywhere is the safe
default: it only adds a load-echo line to stdout, never to the artifacts.

The CLI writes this invocation for you. `--host "<launcher>"` overrides the
launcher, and `--eval-style` says how the host takes the expression: `sub` (the
default) or `positional` for shen-lua.

## Environment variables

| variable | effect |
|---|---|
| `YGGDRASIL_HOST` | stage-1 host launcher argv, checked first. Whitespace-separated, for example `node /path/shen.js` |
| `BIFROST_SHEN_CL` | the same, checked second |
| `YGGDRASIL_SHEN_<PORT>_DIR` | the sibling port checkout for a target, overriding `../<port>` |
| `YGGDRASIL_KEEP_TMP` | set to anything to keep scratch directories (host driver files, stage-2 build temp trees) instead of removing them after each run |

With neither host variable set, the CLI looks for `../shen-cl/bin/sbcl/shen`,
then `../shen-go/bin/shen`, then `../shen-go/shen`.

The port-directory variable names are per target in `builders.json`
(`dir_env`), and `js` is the one that does not follow the pattern:

| target | variable |
|---|---|
| `lisp` | `YGGDRASIL_SHEN_CL_DIR` |
| `lua` | `YGGDRASIL_SHEN_LUA_DIR` |
| `go`, `kl` | `YGGDRASIL_SHEN_GO_DIR` |
| `joy` | `YGGDRASIL_SHEN_JOY_DIR` (or `SHEN_JOY_BIN` for the compiler itself) |
| `rust` | `YGGDRASIL_SHEN_RUST_DIR` |
| `js` | `YGGDRASIL_SHENSCRIPT_DIR` |
| `julia` | `YGGDRASIL_SHEN_JULIA_DIR` |
| `scheme` | `YGGDRASIL_SHEN_SCHEME_DIR` |
| `swift` | `YGGDRASIL_SHEN_SWIFT_DIR` |
| `erlang` | `YGGDRASIL_SHEN_ERL_DIR` |
| `truffle`, `truffle-native` | `YGGDRASIL_SHEN_TRUFFLE_DIR` |
| `c` | `YGGDRASIL_SHEN_C_DIR` |

Several builders also read a direct path variable of their own (`SHEN_C`,
`SHEN_ERL`, `SHEN_SCHEME`, `SHEN_SWIFT`, `SHEN_CL`); the CLI sets these from the
resolved sibling directory, so they matter only when you invoke a `build.sh` by
hand.

## Cross-platform notes

The CLI is a single static Go binary and runs on Linux, macOS and Windows. The
`go` CI job builds and tests it, including the helpers below and the embedded
`builders.json`, on `ubuntu-latest`, `macos-latest` and `windows-latest`.

Launcher resolution on Windows retries a path with each `PATHEXT` suffix
(defaulting to `.COM;.EXE;.BAT;.CMD`), so a host named `shen` finds `shen.exe`.
A resolved `.bat` or `.cmd` is wrapped in `cmd /c`, and a `.sh` (the lisp,
scheme, swift, erlang, c and joy stage-2 scripts) is wrapped in `sh`, which
needs a git-bash, WSL or MSYS `sh` on PATH.

Whether a given target's toolchain is installed is your environment's call, on
every platform.

## The Nix flake

[`flake.nix`](../flake.nix) supplies the Go CLI and the host-language
dependencies for every supported stage-2 target: Common Lisp (SBCL, CLISP, ECL),
LuaJIT, Go, Rust, Node, Julia, Chez Scheme, Swift, Erlang, Truffle/GraalVM, and
the C toolchain with Boehm GC. Systems are `aarch64-darwin`, `aarch64-linux` and
`x86_64-linux`.

```bash
nix develop                                # shell with the pinned toolchain
nix develop --command go test ./... -count=1
nix develop --command go run . targets
nix build                                  # just the CLI
```

The Shen implementations themselves remain sibling source checkouts, or paths
chosen with the `YGGDRASIL_SHEN_*_DIR` variables; Nix supplies their compilers
and runtime dependencies. Each port also has its own locked flake, so it can be
developed independently with `nix develop`. Unfinished Forth, HVM/inets, OCaml
and Odin ports have development flakes without being supported Yggdrasil
targets.
