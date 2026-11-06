# Shen/JIT and kernel 43.2

Yggdrasil's kernel is community ShenOSKernel 43.2
([provenance](../KLambda/PROVENANCE.md)), and [Shen/JIT](https://github.com/tizoc/shen-jit)
is its reference host and a stage-2 target. No change to Shen/JIT is needed.

```bash
make -C ../shen-jit deps all
yggdrasil run tests/fib.shen out/ --target jit
```

## As a host

Shen/JIT runs the shaker from `../shen-jit/build/shen-jit`, or wherever
`YGGDRASIL_HOST` says. A shake of `tests/fib.shen` keeps the same 81 kernel functions
and is byte-identical between runs. Its reader turns `8.0` into the integer 8, and
reads an integer past the fixnum range as a real, in what `bootstrap` emits. That real
is not always the nearest: 2^63 is `9.223372036854778e18` on v0.2.

## As a target

`builders/jit/build.sh` makes one executable from a stock checkout or a release's
source tarball:

1. The slice's defuns, with those the port overrides replaced by the port's.
2. The port defuns those name, and so on. A kernel function that only kept port code
   names comes from `KLambda/` and is listed in `jit-port-support.txt`, as it runs
   outside the manifest. Data does not keep a function: KLambda spells quoted data as
   `cons` trees, and a name called through data is a computed name, which the shake
   reports.
3. For an eval-capable slice, the port's `shen-jit.eval-kl` and the lowerer, which step
   2 then follows. In an eval-free slice `shen-jit.eval-kl` raises an error instead,
   and no compiler is kept.
4. These are lowered with Shen/JIT's lowerer and its stage 0 tools, the slice and port
   sealed and user code open, as on a whole Shen/JIT. `runtime --stage1` writes the
   image, and the image is appended to the runtime.

Stage 1 reads 24 fixed file names and calls four initialisers. The builder writes the
unused files empty and supplies the initialisers. `shen-jit.initialise` sets
`shen-jit.*entry*`, which makes the image a program whose arguments are all its own.

When it starts, the artifact does what most builders' boot does: the kernel's toplevel
forms, `shen.initialise`, then the user's toplevel forms. User functions are already in
the image by then. Stage 1's own call to
`shen.initialise` is guarded to do nothing, so a traced run records the boot. The cost
is startup: about 4 ms for `fib`, against about 6.5 ms for a whole `shen-jit` running
the same script (best of 30, macOS arm64, 2026-11-05).

What the build leaves in `<exe>.build/stage0/`:

| file | contents |
|---|---|
| `jit-port-kept.txt` | port functions in the image |
| `jit-port-support.txt` | kernel functions kept for the port, outside the manifest |

### Limits

- Every user function is defined before the first toplevel form runs. A program that
  defines a function twice would see only the last definition, so the builder refuses
  it with exit 3. A call made before its definition succeeds here, where a whole
  Shen raises that the function is undefined.
- The target needs Shen/JIT's source tree, from `../shen-jit` or
  `YGGDRASIL_SHEN_JIT_DIR`. A release's single executable can host a shake but cannot
  build this target.
- The port's functions and any in `jit-port-support.txt` are not woven by `--trace`,
  so `trace-check` does not see them, as it does not see another port's natives.
- `scip-check` reports SKIP: nothing has established that this target compiles calls
  compositionally. `call_style`, `dispatch`, `special_forms`, `port_writes` and
  `native_deps` are undeclared, and `yggdrasil contract --target jit` shows them unknown.
- `stlib` and the extensions are outside the universe, as on every target.
- The builder is a bash script. It has run on macOS arm64 only; Linux runs in CI
  once this is merged, and Windows has not been tried.

## The other ports

The shaker refuses a host on another kernel. The maintained ports have moved to 43.2 on
their default branches: shen-cl v3.2.1, shen-go v1.6.0, shen-lua v0.12.0, ShenScript
v1.1.0-rc.2, shen-scheme v0.52-pyrex41.6, shen-julia v0.3.0, shen-c v0.5.0, and shen-rust,
shen-swift, shen-erl and shen-truffle at merged commits. shen-joy has not moved. It is a
bounded subset and not a host.

Bifrost's `yggdrasil-shake-parity` shook `tests/fib.shen` on 11 of those hosts, and
`kernel.kl` and the manifest were byte-identical. shen-julia hosts too, but is too slow to
take part without a sysimage. A shake on it matched Shen/JIT's in a separate run.

## What was checked

On 2026-11-05, macOS arm64, Shen/JIT v0.2 and its `main` at `715bf60`, with
Shen/JIT as the host:

| check | result |
|---|---|
| `scripts/parity-gate.sh --targets jit,go,kl,js,lua --min-targets 4` | 10 of 10 fixtures, 5 targets each |
| `trace-check --target jit` on `fib`, `prolog`, `metaeval` | OK, boot phase recorded |
| `parity --prune-init --target jit` on `fib` | PASS |
| `lower-check --target jit --reference jit` on `fib` | OK |
| build and run `prolog` from the v0.2 source tarball | matches the golden |

The other targets, from those ports' 43.2 commits, on macOS arm64:

| target | fixtures |
|---|---|
| `rust`, `erlang`, `swift`, `truffle`, `lisp`, `c`, `julia` | 10 of 10, each run on its own |
| `scheme` | 8 of 10, with `metaeval` and `read-data-eval` a capability SKIP |
| `joy` | not checked on 43.2 |

Those were not one run. A single gate over every target needs every toolchain in one
environment, and no such environment exists yet, so CI gates `jit`, `go`, `js` and `lua`.
`docs/evidence/` holds the earlier kernels' records.
