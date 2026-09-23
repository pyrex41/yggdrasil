# Yggdrasil

A tree-shaker for [Shen](https://shenlanguage.org). It takes a Shen program and
the S42 kernel, works out which of the kernel's 686 functions the program can
reach, and emits just that slice as portable KLambda. A per-target builder then
compiles the slice with the port's own compiler into a standalone artifact.

`tests/fib.shen` shakes to 54 kernel functions and 13 KB of KLambda. Built for Go
it is a static binary that starts in under 10 ms.

Continuation of Mark Tarver's Yggdrasil 1.0 (3-clause BSD). His original paper is
[`yggdrasil.pdf`](yggdrasil.pdf); the 1.0 distribution is in [`archive/`](archive/).

## Install

```bash
go install github.com/pyrex41/yggdrasil@latest
```

Or download a release binary, or `nix develop` for a shell with every target
toolchain pinned. The CLI embeds the shaker and the kernel, so it runs without a
checkout.

Stage 1 needs a Shen host. By default it looks for a sibling `../shen-cl`
checkout; set `YGGDRASIL_HOST` to any Shen launcher to override. Stage 2 needs
the target port as a sibling checkout (`../shen-go`, `../shen-lua`, and so on)
or a `YGGDRASIL_SHEN_<PORT>_DIR` variable pointing at one.

## Use

```bash
yggdrasil shake  prog.shen out/                 # emit the KLambda slice
yggdrasil build  prog.shen out/ --target go     # shake, then build an artifact
yggdrasil run    prog.shen out/ --target go     # build, then run it
yggdrasil why    prog.shen                      # what each part of the program costs
yggdrasil targets                               # list targets and their toolchains
```

`out/` receives `kernel.kl`, `<prog>.kl` and a manifest. The manifest records
what the slice can and cannot reach (`cannot-reach=eval` is a static guarantee
that the program never evaluates code at run time), whether initialisation
order was checked, and the flags the shake ran with.

## Targets

| target | port | artifact |
|---|---|---|
| `go` | shen-go | static binary |
| `lua` | shen-lua | self-contained LuaJIT script |
| `js` | ShenScript | ES module for Node, or a browser module with `--web` |
| `rust` | shen-rust | static binary |
| `lisp` | shen-cl | saved image, SBCL, CLISP or ECL |
| `erlang` | shen-erl | BEAM modules plus a small runtime |
| `julia` | shen-julia | artifact project, optionally a sysimage |
| `scheme` | shen-scheme | Chez program |
| `swift` | shen-swift | slice run by the shen-swift interpreter |
| `truffle`, `truffle-native` | shen-truffle | JVM application or native image |
| `c` | shen-c | C project linked against libshenc |
| `joy` | shen-joy | bounded image for the allocation-free device VM |
| `kl` | shen-go `cmd/kl` | the slice run directly on the bare KLambda VM |

Build and run recipes are data in [`builders.json`](builders.json).
[`docs/targets.md`](docs/targets.md) has the per-target details, sizes and
caveats.

## Verifying a shake

The shake makes one claim: nothing outside the emitted slice runs. Several
independent checks hold it to that.

```bash
yggdrasil parity      prog.shen out/               # same output on every target
yggdrasil trace-check prog.shen out/ --target go   # a traced run enters only slice functions
yggdrasil scip-check  prog.shen out/ --target go   # the compiled artifact kept every edge
yggdrasil facts       prog.shen facts/             # dump the facts the Datalog rules run on
yggdrasil contract    --target go                  # what the port declares, and what checks it
```

The footprint is defined by a Datalog rule set in
[`analysis/analysis.dl`](analysis/analysis.dl), evaluated by Soufflé in CI and
by a Python reference evaluator locally, and both must agree with what the shake
wrote. [`docs/verification-guide.md`](docs/verification-guide.md) walks through
all of it and ends with a table of every claim, marked checked, evidence or
assumed.

## Docs

| | |
|---|---|
| [`docs/verification-guide.md`](docs/verification-guide.md) | the tour: what is checked, how, and what is assumed |
| [`docs/targets.md`](docs/targets.md) | per-target builders, artifact sizes, running the shaker on each port |
| [`docs/shake.md`](docs/shake.md) | how the shake works: reachability, eval-stripping, initialisation, pruning |
| [`docs/analysis-rules.md`](docs/analysis-rules.md) | the Datalog rules, the runtime trace, the SCIP check |
| [`docs/port-contract.md`](docs/port-contract.md) | the facts a port declares and how each is verified |
| [`docs/parity.md`](docs/parity.md) | the cross-target behavioural gate |
| [`docs/why.md`](docs/why.md) | footprint attribution |
| [`docs/lowering.md`](docs/lowering.md) | port-specific lowering, and what running it found |
| [`docs/optimisation.md`](docs/optimisation.md) | the invariant any KL-to-KL pass must keep |
| [`docs/eval-free-cli.md`](docs/eval-free-cli.md) | writing a program that stays eval-free |
| [`docs/reachability.md`](docs/reachability.md) | why a worklist replaced Warshall's closure |
| [`DEMO.md`](DEMO.md) | an executable demo across five targets |

## Tests

```bash
go test ./...
```

With a Shen host and sibling ports available, the suite runs every fixture
through the shake, the rules oracle, the trace check, and the stage-2 builders.
Without them, host-gated tests skip by name and CI asserts the exact skip count.

## Lineage

The kernel is Tarver's S42, vendored under [`KLambda/`](KLambda/). Yggdrasil 1.0
targeted the community 41.2 kernel; this repository retargeted it, was briefly
published under another Norse name, and now carries the original name again.
