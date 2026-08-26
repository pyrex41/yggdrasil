# Yggdrasil

> **Kernel lineage — S41.2 refresh (merged).** The shaker targets Mark
> Tarver's **refreshed S41.2** kernel (shenlanguage.org, re-uploaded
> 2026-07-11; canonical mirror `pyrex41/shen-upstream`, tag
> `s41.2-pristine-20260711`), a lineage switch from the community
> ShenOSKernel-41.2 packaging. The refreshed kernel has no
> `shen.initialise` (init is toplevel forms, wrapped into a synthetic
> initialiser at shake time), no dict layer (property vector instead),
> and a leaner surface: 683 boot defuns vs 1,152. The lua, rust, go and
> js targets are green on their migrated ports (all four fixtures;
> eval-free `fib` shakes to **54 defuns / 13.4 KB**, metaeval to 548;
> four-target parity gate PASS). The reference stage-1 host is shen-cl
> built from its refreshed master (same lineage); a community-41.2
> shen-cl binary is a verified-working alternative — both produce
> byte-identical `kernel.kl` + manifest on every fixture. Prose below
> that predates the refresh is being updated as sections are touched.

A tree-shaker for [Shen](https://shenlanguage.org) programs, targeting
Tarver's refreshed **S41.2** kernel. Descended from Mark Tarver's **Yggdrasil 1.0**
(3-clause BSD), it restores the name Tarver gave the project. In Norse
mythology Yggdrasil is the world tree; here its roots are KLambda and its
branches are the target runtimes reached by each minimal kernel slice.

Dr. Tarver's original vision and description, *Using Yggdrasil to Generate
Stand-alone Programs from Shen* (Shen Group, 2023), is preserved here as
[`yggdrasil.pdf`](yggdrasil.pdf). The Yggdrasil 1.0 distribution this
repository started from is archived in [`archive/`](archive/) along with the
[Wayback Machine capture](https://web.archive.org/web/20240430183437/https://www.shenlanguage.org/Download/Yggdrasil.zip)
it was retrieved from.

Yggdrasil turns a Shen program into a minimal, standalone artifact in a
target language: it computes which of the kernel's 683 functions the
program can actually reach, emits just that slice as KLambda, and hands the
result to a per-target builder that compiles it with the target port's own
KL compiler.

> **The shaker runs on any of the eight ports.** Stage 1 is pure Shen, but
> it compiles your program to KLambda with the host's `bootstrap`
> compiler, so the host must emit fully portable KL. All eight ports —
> shen-cl, shen-lua, shen-go, shen-erl, shen-rust, ShenScript, shen-julia
> and shen-swift — are now verified to produce a byte-identical `kernel.kl` +
> manifest and portable user KL (see the Gotchas section for the per-host
> launcher invocation and the `*hush*` caveat). shen-cl remains the
> reference and the fastest host; shen-julia and shen-swift matched it
> byte-for-byte out of the box (shen-swift via a host-side `pr` override so
> `*hush*` gates only stdout, never file streams, so shakes run under `-q`).

**See it run:** [`DEMO.md`](DEMO.md) is an executable demo (built with
[showboat]) that shakes one program and produces a running artifact on all
five targets; `showboat verify DEMO.md` re-executes every step.

## CLI (`yggdrasil`)

A single static **Go binary** wraps both stages so you don't hand-write the
launcher invocation. It embeds the shaker source + the kernel KLambda slice and
materialises them to a cache dir on first use, so it runs with no checkout.
Install it three ways:

```bash
go install github.com/pyrex41/yggdrasil@latest        # Go toolchain
# or download a prebuilt release binary for your OS/arch (GitHub Releases)
uvx --from git+https://github.com/pyrex41/yggdrasil yggdrasil targets  # uvx (builds Go locally)
```

Then:

```bash
yggdrasil shake prog.shen out/                 # stage 1: emit the KLambda slice
yggdrasil build prog.shen out/ --target go     # stage 1 + build a Go artifact
yggdrasil build prog.shen out/ --target js --web  # a BROWSER-safe ES module
yggdrasil run   prog.shen out/ --target js     # build, then run it (prints stdout)
yggdrasil parity prog.shen out/                # behavioural parity gate across targets
yggdrasil targets                              # list stage-2 targets
```

| subcommand | does |
|---|---|
| `shake PROG OUTDIR` | stage 1 — emit `kernel.kl` + `<prog>.kl` + manifest |
| `build PROG OUTDIR --target T` | stage 1 + the stage-2 builder for target `T` |
| `build … --target js --web` | emit a browser-safe ES module (`import $ from './app.js'; $.caller('fn')(…)`) instead of the Node artifact — no `node:fs`/streams/`process`; passes `--web` to ShenScript's builder |
| `run PROG OUTDIR --target T` | build, then execute the artifact |
| `parity PROG OUTDIR` | run the shaken slice on every target and diff outputs against a reference — see [Behavioural parity gate](#behavioural-parity-gate) |
| `targets` | list available targets (`lisp`/`lua`/`go`/`erlang`/`rust`/`js`/`julia`/`scheme`/`swift`/`truffle`/`truffle-native`) |

The stage-1 **host** defaults to the sibling `../shen-cl/bin/sbcl/shen`
binary, used as-is. The reference is shen-cl built from its S41.2-refresh
master (same lineage as the vendored kernel); an older community-41.2
binary at that path also works — the two are verified to produce
byte-identical `kernel.kl` + manifest on every fixture (user KL differs
only in gensym numbering), so a stale sibling build is a correctness
no-op. Rebuild shen-cl from master to refresh the host. Override with
`--host "<launcher>"` (e.g. `--host "node /path/shen.js" --eval-style
sub`, or `--eval-style positional` for shen-lua), or set `$YGGDRASIL_HOST`
or `$BIFROST_SHEN_CL`. Stage-2
builders live in the sibling port repos (`../shen-lua`, `../shen-go`, …),
overridable per target via `$YGGDRASIL_SHEN_*_DIR`; the build/run recipes are
data in [`builders.json`](builders.json), which [Bifrost](../bifrost)'s
`--shake` mode reads too.

**Cross-platform.** The CLI is a single static Go binary and runs on Linux, macOS
and Windows. Launcher resolution matches `shen.exe` (PATHEXT) on Windows, and a
`.bat`/`.cmd` host or a `.sh` builder (the lisp stage-2 `build.sh`) is
auto-wrapped (`cmd /c` / `sh` — the latter needs git-bash/WSL/MSYS `sh` on
PATH). The `go` CI job builds and tests the binary — including these helpers and
the embedded `builders.json` — on `ubuntu`/`macos`/`windows-latest`. As ever,
whether a given target's *toolchain* (sbcl/luajit/go/Erlang/cargo/node/julia/chez/swift)
is available is your environment's call.

## Architecture

**Stage 1 — shake** (this repo; run on any of the eight ports — see the
host-portability gotcha for per-host launcher syntax):

```
shen eval -q -l yggdrasil.shen -e '(yggdrasil.shake ["prog.shen"] "out")'
```

writes to `out/`:

| file | contents |
|---|---|
| `kernel.kl` | shaken kernel defuns, load order preserved |
| `<prog>.kl` | the user program compiled to KLambda |
| `yggdrasil.manifest.txt` | line-oriented contract (`key=value`) |
| `yggdrasil.manifest` | same, as s-expressions |

The manifest also reports the artifact's effectful **capabilities** —
`reaches=` / `cannot-reach=` over `{eval, read, write, file, clock}` —
derived from the emitted primitive set. `cannot-reach=eval` is a static,
certifiable "this program can never evaluate code at runtime". See
`docs/reachability.md`.

**Stage 2 — build** (one builder per target port, living in that port's
repo):

| target | builder | output (eval-stripped fib) |
|---|---|---|
| Common Lisp | `builders/lisp/build.sh <dir> <exe>` (this repo; `LISP_IMPL=sbcl\|clisp\|ecl`) | saved image (SBCL ~36 MB, CLISP ~7.8 MB) or compiled binary (ECL ~620 KB + libecl) |
| LuaJIT | `shen-lua/bin/yggdrasil-build.lua <dir> <out.lua>` | self-contained .lua (~640 KB, ~25 ms startup) |
| Go | `shen-go/cmd/yggdrasil-build <dir> <outdir>` then `go build` | static binary (~4.5 MB, ≤10 ms startup, cross-compiles linux/windows) |
| Erlang | `builders/erlang/build.sh <dir> <outdir>` (this repo; `SHEN_ERL=<checkout>`) | shaken KLambda compiled to BEAM plus the small shen-erl runtime and a `run` launcher; requires Erlang/OTP at runtime, but never boots the full kernel. |
| Rust | `shen-rust/crates/yggdrasil-build <dir> <outdir>` then `cargo build --release` | static binary (~9 MB, ~40 ms startup) |
| JavaScript | `node ShenScript/bin/yggdrasil-build.js <dir> <out.js>` (`--linked` for needs-eval; `--web` for a browser module) | self-contained ES module (~120 KB, runs on Node 20+ / Bun / Deno 2; `--web` → browser, `import`s the booted env) |
| Julia | `julia --project=shen-julia shen-julia/bin/yggdrasil-build.jl <dir> <outdir> [--sysimage]` | artifact project; with `--sysimage` a per-program sysimage (~266 MB, ~0.15 s warm startup), else a lib-mode `.jl` (~4 s, no sysimage). The shaken kernel+user defuns are baked as module methods (same AOT technique as shen-julia's own fast boot). |
| Chez Scheme | `builders/scheme/build.sh <dir> <outdir>` (this repo; `SHEN_SCHEME=<checkout>`) | self-contained Scheme program dir + `run` launcher (`chez --script`). The shaken kernel+user are compiled with shen-scheme's own `kl->scheme`; overridden kernel fns (`pr`, `shen.char-stoutput?`, dict ops, …) come from shen-scheme's `overrides.scm`, exactly as its own build does. |
| Swift | `builders/swift/build.sh <dir> <outdir>` (this repo; `SHEN_SWIFT=<checkout>`) | slice + `run` launcher driving the shen-swift tree-walking interpreter in `--shaken` mode. shen-swift is an *interpreter*, so there is nothing to code-generate (like LuaJIT/Julia it references its runtime); the artifact is the KL slice and the win is boot speed — a ~200-line shaken kernel vs the full ~2500-line kernel. |
| Truffle (JVM) | `java -jar shen-truffle/target/yggdrasil-builder.jar --format jvm <dir> <out>` | relocatable JVM application (`app-truffle/bin/shen-truffle`) launched with Java; requires the Shen 41.2 Truffle runtime. |
| Truffle (Native Image) | `java -jar shen-truffle/target/yggdrasil-builder.jar --format native <dir> <out>` | platform-native executable (`app-truffle-native`) produced by the Truffle builder. |

**Builder contract**: load `kernel.kl`'s defuns, call `(shen.initialise)`
(41.2 consolidates all global initialisation there), then run each user
file's forms in manifest order — user files contain defuns *and* toplevel
expressions that must execute in source order.

## How the shake works

The kernel call graph (683 defuns, 2568 edges on the S41.2 refresh) is
built once by walking every defun body for call-position symbols and
cached as plain text (`KLambda/callgraph-s41r-20260711.shen`). Per
shake, a pure worklist reachability pass runs from the seed set
`symbols(kernel toplevel init forms) ∪ symbols(user KL)`. See
`docs/reachability.md` for why this replaced Yggdrasil 1.0's O(N³)
Warshall transitive closure, and why fancier algorithms lose on this graph.

Several kernel "tables masquerading as code" (the arity table, the package
external-symbols registry, `*special*`, type-signature keys, lambda-form
eta-entries) are treated as data, not calls; lambda-form entries are
additionally filtered to the footprint at write time.

**Eval-stripping**: when the user KL never mentions an eval-capable entry
point (`eval`, `eval-kl`, `load`, `tc`, `read`, `input+`, …), the shake
additionally drops the `*macros*` registration and replaces
`shen.f-error`'s interactive track-prompt with a plain `simple-error`,
letting the macro expander, typechecker, reader and `eval` fall away.
Stripped programs shake to ~100 kernel defuns (~66 KB of KL) and the
manifest reports `needs-eval=false`; eval-capable programs keep the full
machinery (~561 defuns). Detection over-approximates safely — a stray
symbol named `eval` keeps the machinery.

**Type declarations**: `(declare F Type)` is build-time-only, like
`(datatype ...)`, and eval-free programs drop it. A signature is read by
the typechecker and by nothing else; stage 1 never runs the typechecker
(`bootstrap` is `read-file` + `shen->kl-h`, purely syntactic), so a
signature in the emitted KL is only of use to a typechecker running
*inside* the artifact — which needs the compiler. Retained it is worse
than dead weight: the kernel's `declare` calls `eval-kl` directly, so one
signature drags the typechecker, the prolog engine and `eval` into the
footprint and flips `needs-eval` to true, which disqualifies `--web`
outright (`--web` and `--linked` are mutually exclusive). Eval-capable
programs keep their signatures, since they can load and typecheck code at
runtime; and `(tc +)` is itself an eval entry point, so a program that
actually turns the typechecker on is never eval-free and never stripped.
Nothing else is affected: a program with declares now shakes to the same
KL as the same program without them.

The eval-capable path is exercised end-to-end by `tests/metaeval.shen`
(builds expressions as data — a list, a runtime `define`, a string — and
evaluates them) on all five targets. Each port embeds or links its own
KL compiler for runtime `eval-kl`: the Lisp builder stages shen-cl's
precompiled `compiled/compiler.lsp` when the manifest says
`needs-eval=true`, and ShenScript requires `--linked` (self-contained
mode refuses eval-capable manifests).

## Gotchas (hard-won)

- Shen's `read-file` is **not a data reader**: it applies the currying
  transform to paren applications and turns `[a b c]` into cons ASTs.
  `.kl` files survive because the symbol walk doesn't care about tree
  shape; anything else (like the call-graph cache) must be written and
  parsed as plain text.
- 41.2's stlib is lazily materialised: `mapc`, `filter`,
  `remove-duplicates`, `copy-file` don't exist in port runtimes.
  `yggdrasil.shen` carries its own `ygg.*` versions.
- Compiled KL carries explicit property-table arguments — e.g. the
  external-symbols registration is a 5-element `put` node, not 4.
- **Stage 1 runs on all eight ports** (verified 2026-06-12 for `fib` and
  `prolog` on the first five; shen-julia and shen-swift verified 2026-06-19,
  and shen-erl verified 2026-08-26:
  byte-identical `kernel.kl` + both manifests against the shen-cl reference,
  user KL identical modulo gensym numbering). Getting there took one fix per
  non-shen-cl host, since the user program's KL comes from the host's
  `bootstrap` (shen→KL) compiler and each had a way of emitting non-portable KL
  (shen-julia and shen-swift were the exceptions — both matched byte-for-byte
  with no portability fix):
  - **shen-cl** — reference host, fastest (~0.06 s): `shen eval -q -l yggdrasil.shen -e '(yggdrasil.shake ["prog.shen"] "out")'`
  - **shen-lua** — `bin/shen yggdrasil.shen -e '(yggdrasil.shake ...)'`. Its native engine compiled `prolog?` to port-local `shen.lua-run-query*` hooks; that expansion is now gated to skip the dynamic extent of `bootstrap`, so compiled `.kl` carries the kernel's portable CPS expansion.
  - **shen-go** — `shen eval -q -l yggdrasil.shen -e '(yggdrasil.shake ...)'`. Gained the standard launcher CLI (`extension-launcher.kl`); the stock binary previously had no `-l`/`-e` and fell straight into the REPL.
  - **shen-erl** — `shen-erl eval -q -l yggdrasil.shen -e '(yggdrasil.shake ...)'`. Verified 2026-08-26: emitted byte-identical `kernel.kl` and manifests against shen-cl; its native `pr` override keeps file writes active under `-q`.
  - **shen-rust** — `shen-rust eval -l yggdrasil.shen -e '(yggdrasil.shake ...)'`. Gained the same launcher CLI (on a 1 GB-stack thread for the deep call-graph walk); also fixed `open/2` to honour the `in`/`out` direction symbol so the KL writers truncate-for-write.
  - **ShenScript** — `node bin/shen.js eval -l yggdrasil.shen -e '(yggdrasil.shake ...)'`. The async `read-byte`/file streams left EOF as an unsettled promise, so `read-file-as-bytelist` looped forever (the 50-min hang); file streams are now synchronous and the shake finishes in ~25 s.
  - **shen-julia** — `shen-julia/bin/shen eval -l yggdrasil.shen -e '(yggdrasil.shake ...)'` (omit `-q`: like shen-lua/shen-rust, `*hush*` would otherwise silence the `pr` writes; a host-side `pr` override makes `*hush*` gate only stdout). Pre-create the output dir (the shake doesn't `mkdir`). Produced byte-identical `kernel.kl` + manifests on the first try — no portability fix needed.
  - **shen-swift** — `shen-swift/.build/release/shen-swift eval -q -l yggdrasil.shen -e '(yggdrasil.shake ...)'`. Tree-walking KLambda interpreter (iOS-capable), drives the standard `extension-launcher.kl` CLI. A host-side `pr` override gates `*hush*` to stdout only (file streams always write), so `-q` is safe. Produced byte-identical `kernel.kl` + manifests against the shen-cl reference on the first try — no portability fix needed.
  - **`*hush*` caveat**: `-q` sets `*hush*`, and on **shen-lua and
    shen-rust** that silences the `pr` writes to the output files,
    producing zero-byte artifacts — **omit `-q` on those two**. shen-cl
    (native `pr` override), shen-go, shen-erl, ShenScript, shen-julia and shen-swift
    route `pr` to file streams regardless of `*hush*`, so `-q` is harmless
    there. Dropping `-q` everywhere is the safe default; it only adds a
    load-echo line to stdout, not to the artifacts.

## Tests

`tests/{hello,fib,prolog,metaeval}.shen` are the four fixtures; expected
outputs `hello from shaken shen`, `fib 20 = 6765`,
`mary likes chocolate: true`, and three lines of `eval ...: 42`
(metaeval is the eval-capable fixture: `needs-eval=true`, ~568 kernel
defuns). `tests/parity.shen` (+ `tests/parity.expected`) is the
behavioural-parity fixture — see below.
Every stage-1 change should be verified through at least one stage-2
builder (the Lua one is fastest).

### Behavioural parity gate

Byte-identical KL across hosts is necessary but **not sufficient**: the same KL
can still *execute* differently per target (integer width, symbol interning,
hash iteration order, memoisation), so a slice can pass every byte-identity check
and still return wrong, boot-order-dependent answers on one target — the failure
shen-cas hit on shen-rust ([issue #8](https://github.com/pyrex41/yggdrasil/issues/8)).

`yggdrasil parity PROG OUTDIR` closes that gap: it shakes once, then runs the
slice through each stage-2 target and diffs the rendered output against a
reference (`--reference`, default `lisp`) or a committed golden (`--expect FILE`).
Each artifact is run twice as separate processes, and a fixture that prints two
identical passes separated by a line that is exactly `===` is additionally
checked for in-process determinism (pass 1 == pass 2) — catching boot-order
nondeterminism within a single run. See [`docs/parity.md`](docs/parity.md).

```bash
yggdrasil parity tests/parity.shen out/ --expect tests/parity.expected
# parity gate: parity.shen  (truth = expect:parity.expected)
# target   build  vs-truth  two-boot  two-pass
# lua      ok     ok        ok        ok
# js       ok     ok        ok        ok
# parity: PASS (2 target(s) checked)
```

The Lisp builder is verified on SBCL, GNU CLISP and ECL (`LISP_IMPL=`).
CCL is unsupported: no native Apple Silicon build exists. Implementation
notes that cost real debugging: shen-cl's native `pr` override is
`#+(or ccl sbcl)`, so other implementations need the optional stream
primitives (`shen.write-string` etc. — the driver installs portable
fallbacks when missing); and streams captured in a saved image are dead
on restart under CLISP, so the image toplevel rebinds
`*stoutput*`/`*stinput*` at startup. ECL cannot dump images at all — the
driver compiles each module to an object file and links a real
executable via `c:build-program`, with boot replayed at program startup.

## Name and lineage

This is the continuation of Dr. Tarver's Yggdrasil 1.0, retargeted and
rebuilt for the 41.2 kernel. It was briefly published under another Norse
name; the original **Yggdrasil** name and `yggdrasil` interface are now
canonical again.

[shen-cl]: https://github.com/Shen-Language/shen-cl
[showboat]: https://github.com/simonw/showboat
