# Yggdrasil: one Shen program, five targets

*2026-06-11T22:53:26Z by Showboat 0.6.1*
<!-- showboat-id: f13bb21e-fd4c-4bea-b91b-3c2b524ba3d8 -->

[Yggdrasil](README.md) tree-shakes a [Shen](https://shenlanguage.org) program against Mark Tarver’s refreshed S41.2 kernel (canonical mirror `pyrex41/shen-upstream`) and emits the minimal KLambda slice plus a manifest; per-target builders in the sibling port repos then compile that slice with each port's own KL compiler. This demo shakes one program and produces a running artifact on **Common Lisp (SBCL), LuaJIT, Rust, Go, and JavaScript (Node)**.

Assumptions: sibling checkouts `../shen-cl` (with a built `bin/sbcl/shen`), `../shen-lua`, `../shen-rust`, `../shen-go`, `../ShenScript`, and `sbcl`, `luajit`, `cargo`, `go`, `node` on PATH. Run from the Yggdrasil repo root.

The program: `tests/fib.shen` —

```bash
cat tests/fib.shen
```

```output
\\ Fixture: function definition, recursion, arithmetic.
(define fib
  0 -> 0
  1 -> 1
  N -> (+ (fib (- N 1)) (fib (- N 2))))

(output "fib 20 = ~A~%" (fib 20))
```

## Stage 1 — shake

`yggdrasil.shake` computes the program's reachable slice of the kernel's 683 functions. fib never evaluates Shen at runtime, so eval-stripping kicks in: the macro expander, typechecker, reader and `eval` all fall away, leaving ~54 kernel functions.

```bash
rm -rf out-demo && mkdir -p out-demo && ../shen-cl/bin/sbcl/shen eval -q -l yggdrasil.shen -e "(yggdrasil.shake [\"tests/fib.shen\"] \"out-demo\")" | tail -1 && ls out-demo
```

```output
done
fib.kl
kernel.kl
yggdrasil.manifest
yggdrasil.manifest.txt
```

```bash
grep -c "(defun" out-demo/kernel.kl && grep -E "manifest-version|kernel-version|user=|fn=|needs-eval" out-demo/yggdrasil.manifest.txt
```

```output
54
manifest-version=3
kernel-version=42-s42.20260825
user=fib.kl
fn=fib 1
needs-eval=false
```

54 kernel defuns (of 683, including the synthesised `shen.initialise`) plus the user code, with the contract a builder needs: load `kernel.kl`, call `(shen.initialise)`, run the user forms in order.

## Stage 2 — Common Lisp (SBCL)

The Lisp builder compiles the slice with shen-cl's `kl->lisp` and saves a native executable (`LISP_IMPL=clisp|ecl` also work; see README).

```bash
builders/lisp/build.sh out-demo out-demo/fib-lisp >/dev/null 2>&1 && ./out-demo/fib-lisp
```

```output
fib 20 = 6765
```

## Stage 2 — LuaJIT

The shen-lua builder compiles the slice to Lua source and emits a single self-contained file (~640 KB) that needs only a `luajit` binary.

```bash
luajit ../shen-lua/bin/yggdrasil-build.lua out-demo out-demo/fib.lua >/dev/null 2>&1 && luajit out-demo/fib.lua
```

```output
fib 20 = 6765
```

## Stage 2 — Rust

The shen-rust builder AOT-compiles every shaken defun to Rust via `klcompile` and scaffolds a standalone Cargo project (path-dependency on the `shen-rust` crate). `cargo build --release` links a ~9 MB native binary.

```bash
(cd ../shen-rust && cargo run -q --release -p yggdrasil-build -- ../yggdrasil/out-demo ../yggdrasil/out-demo/fib-rust) >/dev/null 2>&1 && (cd out-demo/fib-rust && cargo build --release -q >/dev/null 2>&1) && ./out-demo/fib-rust/target/release/fib-rust
```

```output
fib 20 = 6765
```

## Stage 2 — Go

The shen-go builder translates the slice through shen-go's bytecode-IR→Go codegen into a plain Go module — no plugins — which `go build` turns into a ~4.5 MB static binary.

```bash
(cd ../shen-go && go build -o /tmp/yggdrasil-build-demo ./cmd/yggdrasil-build) && /tmp/yggdrasil-build-demo -shen-go ../shen-go out-demo out-demo/fib-go >/dev/null 2>&1 && (cd out-demo/fib-go && go build -o ../fib-go-bin .) && ./out-demo/fib-go-bin
```

```output
fib 20 = 6765
```

Because the Go output is an ordinary module, cross-compilation is free:

```bash
(cd out-demo/fib-go && GOOS=linux GOARCH=amd64 go build -o ../fib-go-linux .) && file out-demo/fib-go-linux | grep -o "ELF 64-bit LSB executable, x86-64" && file out-demo/fib-go-linux | grep -o "statically linked"
```

```output
ELF 64-bit LSB executable, x86-64
statically linked
```

## Stage 2 — JavaScript (Node / Bun / Deno)

The ShenScript builder AOT-compiles the slice with its own KL→JS compiler and emits one self-contained ES module (~120 KB, no dependencies) that runs on Node 20+, Bun, and Deno 2.

```bash
node ../ShenScript/bin/yggdrasil-build.js out-demo out-demo/fib.js >/dev/null 2>&1 && node out-demo/fib.js
```

```output
fib 20 = 6765
```

## The tally

One 7-line Shen program, one shake, five independently-runnable artifacts:

| target | artifact | runs via |
|---|---|---|
| Common Lisp | `out-demo/fib-lisp` | native executable (SBCL image; `LISP_IMPL=clisp\|ecl` also supported) |
| LuaJIT | `out-demo/fib.lua` | `luajit fib.lua` |
| Rust | `out-demo/fib-rust/target/release/fib-rust` | native executable |
| Go | `out-demo/fib-go-bin` (+ a linux/amd64 cross-build) | static native executable |
| JavaScript | `out-demo/fib.js` | `node fib.js` (also `bun` / `deno run`) |

All five printed `fib 20 = 6765` from a kernel slice of 54 functions — the other 629 were shaken away. Re-execute this document with `showboat verify DEMO.md`.

```bash
ls out-demo/fib-lisp out-demo/fib.lua out-demo/fib-rust/target/release/fib-rust out-demo/fib-go-bin out-demo/fib-go-linux out-demo/fib.js
```

```output
out-demo/fib-go-bin
out-demo/fib-go-linux
out-demo/fib-lisp
out-demo/fib-rust/target/release/fib-rust
out-demo/fib.js
out-demo/fib.lua
```
