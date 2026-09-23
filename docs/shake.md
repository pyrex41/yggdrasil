# How the shake works

## Stage 1 in one paragraph

A shake runs on a Shen host. The host's `bootstrap` compiler turns each user
file into KLambda, which is why the host must emit fully portable KL and why
`docs/targets.md` lists the eight ports verified to do so. The kernel's own
KLambda is read alongside it. The seed set is the symbols of the kernel's
toplevel initialisation forms plus the symbols of the user KL: anything named in
either is something the program might call. From those seeds a worklist pass
walks the kernel call graph to fixpoint, and the reachable defuns are the
footprint; everything else is discarded. The graph counts a symbol as an edge
wherever it occurs in a body, not only in call position, because a bare symbol
in argument position gets applied by the callee: in `(map f xs)`, `f` is never
in call position and is called anyway. That over-approximates, which is the safe
direction. Why a worklist and not Warshall's transitive closure is
[`docs/reachability.md`](reachability.md); the rules as a Datalog program, and
the oracles that check the shake against them, are
[`docs/analysis-rules.md`](analysis-rules.md).

## The call-graph cache

Building the direct call graph is the one expensive pass: it walks every symbol
leaf of roughly 280 KB of KLambda. It is done once and cached as plain text in
`callgraph-cache.shen`, one line per defun, and reloaded on later shakes. The
current graph is 686 defuns and 2,583 edges, about four per node, which is very
sparse.

Two properties of the cache are load-bearing.

It is a **derived** artifact and deliberately does not live under `KLambda/`. It
used to, and `main.go`'s `go:embed` then named the bare `KLambda` directory,
which sweeps up whatever is in the working tree including gitignored generated
files. The cache rode into the binary and into the hash that names the extracted
root, so the shaker's behaviour depended on untracked files present at
`go build` time. `main.go` now names `KLambda/*.kl` explicitly, and at the repo
root the cache matches no embed pattern at all.

Loading the cache also restores the `defp` marks. `called-fns` runs per shake
over the kernel's toplevel init forms to collect seeds, and consults `defp` to
decide whether a symbol is a kernel defun. Without the marks a cache-hit shake
seeds nothing from the init forms and silently under-shakes.

The cache is keyed by filename, and the loader validates only that the parse was
non-empty, so a cache that does not match the kernel loads fine. Freshness has
to be structural rather than checked: folding over the kernel as a bytelist in
Shen measured around 5 s per pass, against the 0.24 s the cache saves. The CLI's
extracted root is named for a hash of the embedded tree, so a cache written
inside one is by construction the cache for that kernel.

## Tables masquerading as code

Several kernel constructs are syntactically calls but semantically data. Left
alone they drag nearly every public symbol into every footprint, so the graph
builder drops their edges:

| construct | why it is data |
|---|---|
| the arity table literal | pure name-and-number pairs |
| the package external-symbols registration | a name list handed to `put` |
| `(set shen.*special* ...)` and `shen.*extraspecial*` | name lists |
| a toplevel `(declare F Type)` | `F` is a table key and `Type` is data; declaring a function does not call it |
| `shen.assoc->` with a literal symbol key | the key is a key, not a callee |
| the lambda-form eta-entries | a name-to-wrapper table |

The literals themselves survive; they are filtered to the footprint at write
time, so the emitted arity table and lambda table carry only names the slice
kept.

In an eval-capable program this subtraction is not sound, because the kernel can
turn a name in a data literal into a call at run time. The rule set says so
directly: a data-literal symbol is an edge exactly when the program is
eval-capable.

## Eval-stripping

`*eval-entry-points*` is the list of names that let a program evaluate code at
run time:

```
eval  eval-kl  load  tc  spy  track  step  it
read  read-from-string  lineread  input  input+  bootstrap
```

When the user KL mentions none of them, the shake takes the eval-free path. It
drops the `*macros*` registration and replaces `shen.f-error`'s interactive
track prompt with a plain `simple-error`. With those two gone the macro
expander, the typechecker, the reader and `eval` itself are no longer reachable
and fall away.

Detection is a syntactic membership test, so it over-approximates, and it
over-approximates in the safe direction: a stray symbol named `eval` that is
never applied keeps the whole machinery. A program that is wrongly called
eval-capable is merely larger; the converse cannot happen.

The size difference is the point. An eval-free program shakes to roughly 50 to
100 kernel defuns, an eval-capable one to around 550. `tests/fib.shen` is 54
defuns and 13 KB of KL; `tests/metaeval.shen` is 551, both counts including the
synthesised `shen.initialise`.

Each port supplies its own runtime `eval-kl` for the eval-capable case: the lisp
builder stages shen-cl's precompiled `compiled/compiler.lsp` when the manifest
says `needs-eval=true`, and ShenScript requires `--linked`, its self-contained
mode refusing an eval-capable manifest. [`docs/eval-free-cli.md`](eval-free-cli.md)
covers the other half of this: how a program that reads stdin can stay eval-free
by reading bytes rather than S-expressions, since `read` is an eval entry point,
and the two traps that catches people.

## Type declarations

`(declare F Type)` is build-time-only, like `(datatype ...)`, and an eval-free
program drops it.

A signature is read by the typechecker and by nothing else. Stage 1 never runs
the typechecker (`bootstrap` is `read-file` plus `shen->kl-h`, purely
syntactic), so a signature in the emitted KL could only serve a typechecker
running *inside* the artifact, which needs the compiler. Keeping it is worse
than dead weight: the kernel's `declare` calls `eval-kl` directly, so a single
retained signature drags in the typechecker, the prolog engine and `eval`, flips
`needs-eval` to true, and disqualifies `--web` outright.

Eval-capable programs keep their signatures, since they can load and typecheck
code at run time. `(tc +)` is itself an eval entry point, so a program that
actually turns the typechecker on is never eval-free and never stripped. Nothing
else changes: a program with declares shakes to the same KL as the same program
without them.

## The manifest keys

Both manifests (`yggdrasil.manifest.txt` as `key=value`, `yggdrasil.manifest` as
s-expressions) carry the same facts. Builders must ignore keys they do not
recognise, which is what makes every key below contract-safe to add.

| key | meaning |
|---|---|
| `reaches=` / `cannot-reach=` | the artifact's effectful capabilities over `{eval, read, write, file, clock}`, derived from the emitted primitive set. `cannot-reach=eval` is a static, certifiable "this program can never evaluate code at run time" |
| `needs-eval=` | whether `eval-kl` survived into the slice. The eval-strip and the capability report stay in lock-step because both read the same primitive set |
| `init-order=` | `checked` or `checked-weak`, see below |
| `computed-names=` | `none`, or a comma-separated list of the user defuns (or `top` for a file's toplevel forms) that contain an `intern`, or a `(value X)` / `(set X _)` whose `X` is not a literal symbol |
| `pruned-init=` | how many toplevel forms the synthesised initialiser dropped as dead |
| `shaken=false` | written only by `--no-shake`; its absence means the ordinary shaken slice |
| `traced=true`, `trace-file=` | written only by `--trace` |

`init-order` reports a check over the emitted boot sequence: the kernel's init
forms, then the user files' toplevel forms in manifest order. Every form must
read a global only after some form up to and including itself set it, or because
the port supplies it (`*stinput*`, `*stoutput*`).

- **`checked`** means every such read had a literal, strictly earlier
  `(set V _)` to point at.
- **`checked-weak`** means at least one read was discharged only by an
  over-approximation: the form calls a function whose body sets the global, or
  the form sets it itself, or the `(set V _)` sits inside a `freeze` or
  `lambda`. None of the three establishes that the write *does* happen before
  the read, only that it could, so the check is weaker there and the manifest
  says so.

A program that violates the rule outright is refused with no artifacts written.
The check runs before anything is written, which is what makes the Go driver's
"missing or empty `kernel.kl` means failure" contract sound.

`computed-names` only reports. The soundness argument for the whole shake is
that a name the artifact can call occurs syntactically in the code, and `intern`
plus a non-literal global name are the two things that break it. Neither is an
eval entry point (a program that interns a name it never applies is perfectly
safe), so the shake warns and records rather than refusing.

## `--prune-init` and `port_reads`

`pruned-init=` is `0` unless you pass `--prune-init` to `shake`, `build` or
`run`. It is off by default, because pruning changes the bytes of `kernel.kl`,
and pruning against a `port_reads` list nobody has checked can drop a
`(set V Lit)` that the port's runtime reads natively.

A global counts as live when a reachable kernel defun reads it, when a kept
toplevel form reads it, when its name occurs anywhere in your program, or when
**the port's own runtime** reads it natively. shen-go's `fn` reads
`shen.*lambdatable*` from Go, its `arity` reads `*property-vector*`, and its
`open` resolves paths through `*home-directory*`. That last category is
per-backend data, so it lives in [`builders.json`](../builders.json) next to the
backend as a `"port_reads"` array, with a `"port_reads_source"` naming the
runtime lines it was read off and a `"port_reads_checked_by"` naming the test
that fails when it drifts.

Today only `go` declares one. Every other target inherits the `_default` block,
whose value is the literal string **`"unknown"`**. That block once held a
35-name conservative guess, which every unmeasured target inherited, so "nobody
looked" and "somebody measured this" reached every consumer in the same shape.
`yggdrasil contract --target T` now prints `port_reads  unknown` for such a
target, with the source saying why and a line saying what it costs.

There are two refusals, and both name what the union is over and what it says
nothing about:

- **`--prune-init --target T`** where `T` resolves to unknown, or whose declared
  list has no `port_reads_checked_by`, is refused by name.
- **`--prune-init` with no `--target`** is refused while any target is unknown.
  The union it would prune against is over the targets that *have* declared a
  list (today `go` alone), so it is sound for those and for no one else, and a
  no-target slice is by definition one that may be built for a port nobody
  checked.

`--target go` uses go's own list, the only measured one, and is the only
invocation the flag accepts unasked. `--prune-init-unverified` prunes against
the union anyway and takes a warning.

### Only `(set V Lit)` is ever dropped

The pruner drops a toplevel form only when it is exactly `(set V Lit)` for a
literal symbol `V` and an atomic `Lit` (including the empty list), and the rules
found that global dead. A form whose value is a call, such as
`(set *property-vector* (vector 20000))`, is an effect in its own right and
stays however dead its global is.

## Gotchas

- Shen's `read-file` is **not a data reader**. It applies the currying transform
  to paren applications and turns `[a b c]` into cons ASTs. `.kl` files survive
  it because the symbol walk does not care about tree shape, but anything else,
  the call-graph cache included, has to be written and parsed as plain text.
- The stlib is **lazily materialised**, so `mapc`, `filter`,
  `remove-duplicates` and `copy-file` do not exist in port runtimes.
  `yggdrasil.shen` carries its own `ygg.*` versions and uses those throughout.
- Compiled KL carries **explicit property-table arguments**. The
  external-symbols registration, for instance, is a 5-element `put` node, not a
  4-element one. Pattern matches on KL shapes have to expect the extra
  argument.

## Where to go deeper

| | |
|---|---|
| [`docs/analysis-rules.md`](analysis-rules.md) | the facts and rules as a Datalog program, the runtime trace, the SCIP-contract inclusion check |
| [`docs/reachability.md`](reachability.md) | why a worklist replaced Warshall's closure, and why fancier algorithms lose on this graph |
| [`docs/eval-free-cli.md`](eval-free-cli.md) | keeping a program that reads stdin eval-free |
| [`docs/verification-guide.md`](verification-guide.md) | the whole justification, claim by claim, marked checked, evidence or assumed |
