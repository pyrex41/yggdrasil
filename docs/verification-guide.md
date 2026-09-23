# Verifying the shake: a guided tour

This guide walks through everything Yggdrasil does to justify one claim:
that the small program it emits behaves like the big program you wrote.
It is written for someone who knows Shen and wants to understand, and
ideally check, the argument. Each technology is introduced where it first
matters, with a link to learn more. The detailed design notes are linked
from each section; this document is the map.

Contents:

1. [The problem: what a shake changes](#1-the-problem-what-a-shake-changes)
2. [What "equivalent" has to mean](#2-what-equivalent-has-to-mean)
3. [Reachability, and why the graph is small](#3-reachability-and-why-the-graph-is-small)
4. [Footprint attribution: `yggdrasil why`](#4-footprint-attribution-yggdrasil-why)
5. [The analysis as a rule set: Datalog](#5-the-analysis-as-a-rule-set-datalog)
6. [Two transcriptions and one independent engine: Soufflé, Python, Shen](#6-two-transcriptions-and-one-independent-engine-soufflé-python-shen)
7. [Two checks the rules made possible](#7-two-checks-the-rules-made-possible)
8. [Runtime tracing: aspect weaving at the KL level](#8-runtime-tracing-aspect-weaving-at-the-kl-level)
9. [Static inclusion: SCIP and the graph extractor](#9-static-inclusion-scip-and-the-graph-extractor)
10. [Behavioural parity](#10-behavioural-parity)
11. [The port contract](#11-the-port-contract)
12. [What is proved, what is evidence, what is assumed](#12-what-is-proved-what-is-evidence-what-is-assumed)
13. [Reproducing everything](#13-reproducing-everything)

---

## 1. The problem: what a shake changes

Yggdrasil is a **tree shaker** for Shen programs. Tree shaking is the
general name for removing code a program cannot reach; the term comes from
the JavaScript bundler world
([Rollup's explanation](https://rollupjs.org/introduction/#tree-shaking)
is a good short one), and Mark Tarver's
[original Yggdrasil paper](../yggdrasil.pdf) proposed it for Shen in 2023.

The S42 kernel is 686 KL functions. A "hello world" needs about fifty.
The shake computes which fifty, emits just those as `kernel.kl`, and
hands the result to a per-target builder that compiles KL to Go, Lua,
JavaScript, Rust, and so on. See the [README](../README.md) for the
pipeline and targets.

The shaken program is not just a subset. Along the way the shake also:

- wraps the kernel's toplevel initialisation forms into a synthesised
  `shen.initialise`;
- when the program can never evaluate code at runtime ("eval-free"),
  replaces the interactive partial-function handler `shen.f-error` with a
  plain error, blanks the macro registration, and trims three kernel
  tables to the functions that survive;
- drops type declarations, which only the typechecker reads.

So the question "is the shaken program the same program?" is a real one,
and the rest of this guide is the answer in stages.

## 2. What "equivalent" has to mean

Two programs are not syntactically the same here, by construction. The
right notion is **closed trace equivalence**: on every input, running the
whole program produces the same observable trace, meaning the bytes
written, the files opened and closed, termination, and the exit status.
Time and memory are excluded on purpose.

Even that is too strong as stated, because one rewrite is visible: on a
partial-function failure, the full program opens Shen's interactive
tracker and the shaken program raises `simple-error` with the function's
name. So the honest theorem is *equivalence on every trace that does not
hit a partial-function failure, plus a stated refinement on those that do*.

The argument then splits in two:

**At the KL level**, a
[bisimulation](https://en.wikipedia.org/wiki/Bisimulation) between the
full and shaken programs, with four obligations:

1. *Reachability soundness*: any function the program ever calls is in the
   computed footprint. (Section 3, checked in sections 5 and 8.)
2. *Footprint closure*: every function in the footprint is emitted.
3. *Initialiser equivalence*: after the synthesised initialiser runs, every
   global the slice reads has the value the full boot would give it.
   (Section 7.)
4. *Failure refinement*: the `f-error` rewrite changes only the trace after
   a failure.

**At the target-language level**, a transfer step through the backend. If
the backend compiles each function independently, KL equivalence transfers
without proving the backend correct, because a miscompiled function is
miscompiled identically in both programs. That is what section 9 checks,
and section 11 turns into a contract per port.

Obligation 1 has a hypothesis: no function name is computed at runtime.
`((intern (cn "rev" "erse")) X)` is legal KL, mentions no `reverse`, and
would be shaken out. The shake now reports this case (section 7).

Design note: [analysis-rules.md](analysis-rules.md), "The claim".

## 3. Reachability, and why the graph is small

The footprint is **single-source reachability** over a call graph: start
from the functions the user program and the kernel's init forms mention,
follow calls, keep everything you touch. The graph is built once from the
kernel's KL by walking every symbol in every function body, and cached.

Two facts about this graph decide much of what follows.

It is small and sparse: 686 nodes and about 2,600 edges. A worklist
traversal takes milliseconds. Tarver's 1.0 design computed the full
transitive closure with
[Warshall's algorithm](https://en.wikipedia.org/wiki/Floyd%E2%80%93Warshall_algorithm),
which answers reachability for every pair of functions; the shake needs
only the rows for its seeds, so Yggdrasil uses a worklist and keeps a
finished Warshall as a differential oracle. Design note:
[reachability.md](reachability.md).

And a more precise graph would not make the footprint much smaller. The
edge rule counts every symbol mentioned in a body, whether in call
position or as an argument. The obvious refinement, counting only call
position, is unsound in KL because functions are passed by name: in
`(map f xs)` the symbol `f` is an argument, not a call, yet `map` applies
it at runtime. Drop that edge and `f` is shaken out of a program that
calls it. Measured on S42 without eval stripping, even that unsound
refinement shrinks the graph by 7% and the floor by eleven functions out
of 661. What actually decides the footprint is a step: with no eval entry
point reachable the floor is 48 functions; with one, it is 548, because
the macro expander, typechecker and reader come in together. That
difference of five hundred functions is a symbol test, not a graph
problem. So the machinery in the rest of this guide is not about making
the footprint smaller; it is about establishing that the footprint is
*correct*, and about where the remaining bytes actually are (section 7,
section 12).

## 4. Footprint attribution: `yggdrasil why`

The first tool built for this work answers Tarver's request on
[the Shen group](https://groups.google.com/g/qilang/c/duE01tE5oIU) for "some kind of device that scans the
code and highlights parts of your program that drag in kernel":

```
yggdrasil why prog.shen --trace read
```

prints the floor every program pays, the total, and for each user defun,
the file's toplevel forms, and each kernel function the program mentions
directly, two numbers: what it *adds* over the floor alone, and what would
leave `kernel.kl` if it went away (*exclusive*). `--trace F` prints the
shortest call chain to any kernel function.

Two things it showed immediately. The S42 kernel's own `bootstrap` already
rewrites a partial function's `(shen.f-error f)` into a plain error, so the
example in the thread costs nothing in a bootstrapped program. And when
creep happens it is that step function again: the eval-capable trace for
`read` runs `eval -> shen->kl -> ... -> scan-body -> shen.f-error ->
y-or-n? -> read`.

Design note: [why.md](why.md). Fixtures: `tests/partial.shen` (eval-free),
`tests/partial-eval.shen` (one stray `eval`).

## 5. The analysis as a rule set: Datalog

**Datalog** is a logic programming language: a program is a set of facts
(`edge(f, g)`) and rules (`reach(G) :- reach(F), edge(F, G)`), and running
it computes the least set of facts the rules can derive. Unlike Prolog,
Datalog has no function symbols and no control flow, which makes every
program terminate and every answer independent of rule order. It is the
standard language for expressing static analyses declaratively; the
[Doop](https://bitbucket.org/yanniss/doop/src/master/) framework for Java
is the best-known example. Introductions:
[Wikipedia](https://en.wikipedia.org/wiki/Datalog), and the first chapters
of [*Foundations of Databases*](http://webdam.inria.fr/Alice/) (free
online) for the theory.

Two Datalog concepts recur below:

- **Semi-naive evaluation**: rather than re-deriving everything each
  round, join only against the facts that were new in the previous round.
  Same answer, far less work.
- **Stratified negation**: a rule may say `!reach(F)` only if `reach` is
  fully computed in an earlier stratum. This keeps "not" well-defined.

Why restate the shake this way? Not for speed: the worklist already takes
0.1 s. For **legibility and auditability**. Before, the shake's decisions
lived in a dozen pattern-match clauses spread through `yggdrasil.shen`:
four special cases for kernel tables that look like code, an eval
entry-point list, an `f-error` row strip, a lambda-table filter. As rules
they fit on a page, and "the analysis is sound" becomes a single sentence
about one object: *the dynamic call relation is contained in the least
fixpoint of these rules*.

The rule set is [`analysis/analysis.dl`](../analysis/analysis.dl). Writing
it from the code rather than the design found four places the design was
wrong about the shake that exists; they are recorded in the file as
deviations D1 to D10, and the design note keeps them.

Design note: [analysis-rules.md](analysis-rules.md), "Facts" and "Rules".

## 6. Two transcriptions and one independent engine: Soufflé, Python, Shen

Three programs evaluate these rules, and it is worth being precise about
what their agreement is worth, because "three independent engines"
oversells it.

`analysis/analysis.dl` was written **from the code**, not from the design
— deviations D1 to D10 in its header are the record of the design bending
to the shake that exists. The Shen engine beside it in `yggdrasil.shen` is
the shake itself, running a clause-for-clause copy of that file. And
`analysis/refeval.py` implements `reach` as the same worklist in another
language. Two of the three readings are therefore **transcriptions** of
the thing they check, and a transcription agreeing with its original is a
weaker statement than an independent evaluation: what it can catch is a
line copied wrongly, not a rule that is wrong about the shake.

Soufflé is the one genuinely independent evaluator: an off-the-shelf
engine that reads `analysis.dl` and knows nothing about Yggdrasil.

The agreement check is worth having, and it is why D1 to D10 exist at all.
But the honest description is *two transcriptions and one independent
engine*, not three independent engines, and the claim it supports is
"these three readings of one rule set agree", not "three parties
independently confirmed the analysis".

**[Soufflé](https://souffle-lang.github.io/)** is the reference Datalog
engine for program analysis: it compiles Datalog to parallel C++ and is
what Doop runs on. `yggdrasil facts PROG DIR` dumps the shake's fact
relations as TSV; `souffle -F DIR -D out analysis/analysis.dl` computes
`reach`, and a test asserts it equals the functions in `kernel.kl`, on
every fixture, in both modes. CI runs this
(`.github/workflows/analysis-oracle.yml`). Soufflé never runs in a user's
shake; it is an oracle.

**[`analysis/refeval.py`](../analysis/refeval.py)** is a 348-line
semi-naive evaluator in standard-library Python, for developers without
Soufflé. The test runs whichever is present and, when both are, diffs them
against each other.

**The Shen engine** is the one the shake actually uses. Shen's own
[Prolog](https://shenlanguage.org/learn-shen/prolog.html) cannot run these
rules: it is top-down SLD resolution with no
[tabling](https://en.wikipedia.org/wiki/Tabling), so `reach(G) :- reach(F),
edge(F,G)` loops on a cyclic call graph, and it has no built-in negation.
So `yggdrasil.shen` carries a bottom-up Datalog engine of about 150 lines:
tuples as lists, rules as data, semi-naive, indexed by predicate and first
argument, with stratification *checked* rather than assumed. The rules live
beside a verbatim copy of `analysis.dl` so a reader can compare them line
by line. Fixpoint time is 0.12 to 0.15 s eval-free and about 0.25 s
eval-capable on the shen-go host; the naive prototype it replaced took
6.5 s.

The shake's output stayed **byte-identical** on every fixture across this
change, with one later, deliberate exception described below, and
`(yggdrasil.footprints ["prog"])` computes the footprint by rules, by
worklist and by Warshall and asserts agreement.

One divergence needs explaining, recorded as D8. Datalog derives a *set*
of reachable functions; it has no notion of order. But two things the
shake writes depend on an order. `kernel.kl` lists the kept functions in
kernel load order, which is fine, since that order comes from the kernel
files, not the footprint. The lambda table does not: for an eval-free
program the shake replaces the kernel's boot-time table construction with
a literal `(set shen.*lambdatable* (cons (cons f (lambda ...)) ...))`, one
entry per footprint function, and that literal is emitted by walking the
footprint *as a list*. Whatever order the list has becomes the order of
the entries and therefore the bytes of `kernel.kl`, so the order has to be
a defined, deterministic function of the program.

It is defined, not walked. `ygg.rule-footprint` renders the derived set as
kernel load order — `(map (fn row-head) Graph)`, the order `graph-rows`
read the kernel in — restricted to `reach`, followed by the seeds the
rules do not reach, deduplicated. That remainder is exactly the non-kernel
seeds: primitives, user function names and data symbols, which D6 keeps
out of `reach` and which `keep-set` and the arity trimming still read.
Membership is decided by the rules; the order is a value.

It did not start that way, and the correction is the one place the shake's
bytes have moved. Stage 3 first kept the old order by re-running the
pre-rules depth-first walk over the same raw graph rows, emitting only
nodes the rules had put in `reach`, with a check that errored if the rules
derived anything the list lacked — a second reachability implementation,
over a second graph, from a second seed computation, kept alive only so
that `kernel.kl`'s bytes would not move. It is now deleted;
`ygg.dl-walk-order` and `ygg.dl-covered?` no longer exist. Deleting it
moved exactly one line of `kernel.kl` on each eval-free fixture — the
synthesised `shen.initialise`, whose lambda-table literal is a permutation
of the same entries — while the eval-capable fixtures, which still build
that table at boot, stayed byte-identical. The defun set, both manifests
and every `tests/*.expected` are unchanged before and after, and `fn`
reads the lambda table with `assoc`, so the order never carried meaning
beyond those bytes. Byte identity *across the eight ports* is the headline
guarantee and is untouched; byte identity across versions of Yggdrasil was
a self-imposed constraint, and it was given up there, once, deliberately,
rather than keep paying for it with a duplicate traversal.
`TestFootprintOrderIsKernelLoadOrder` now checks the definition on every
fixture that shakes — the footprint restricted to kernel defuns is a
subsequence of kernel load order — and the old walk's order fails that
test, so it is a regression test and not a golden.

Design note: [analysis-rules.md](analysis-rules.md), "Engine" and Stage 3.

## 7. Two checks the rules made possible

Once reads and writes of globals are facts, two checks are one rule each.

**Initialisation order.** Tarver's example on
[the thread](https://groups.google.com/g/qilang/c/duE01tE5oIU):

```
(set a 1)
(set b (+ (value a) 1))
(set c (+ (value b) 1))
```

Of the six orderings only one is right. The rule `readBeforeWrite(N, V)`
fires when a toplevel form reads a global that no form up to and including
itself wrote — directly, through a function it calls, or inside a `freeze`
it thaws — and no port supplies. The shake refuses such a program with
`yggdrasil-shake: FAIL init-order form=N reads=V` and writes no
`kernel.kl`; what is left refused is exactly the program a reordering can
fix. A clean run records `init-order=checked` in the manifest, or
`init-order=checked-weak` when some read was discharged only by one of
those three over-approximations (the sibling rule `weakRead`). Fixtures:
`tests/init-order-bad.shen` (refused), `tests/init-order-setter.shen` and
`tests/init-order-sameform.shen` (both `checked-weak`).

**Dead initialisation.** `liveGlobal(V)` is a global some kept function or
toplevel form reads, or the port's runtime reads natively; `deadInit(N,V)`
is a write nothing reads. Behind `--prune-init` the shake drops dead
literal sets from the initialiser: 28 of 35 forms on a typical eval-free
program, about 6% of `kernel.kl`. It is off by default because "the port
reads it natively" is a declared fact per target (`port_reads` in
`builders.json`), verified today only for shen-go by reading its runtime
source.

The **computed-name** rule from section 2 also lives here:
`computedName(F)` when a user function applies an `intern`ed name or
reads a computed global. The shake warns and records
`computed-names=` in the manifest. It decides nothing yet; it makes the
hypothesis visible. On the current fixtures only the one written to
trigger it does.

Design note: [analysis-rules.md](analysis-rules.md), Stages 2 and 4.

## 8. Runtime tracing: aspect weaving at the KL level

Everything so far is static. The reachability lemma says what *could* run;
it is worth checking against what *does*.

**Aspect-oriented programming**, of which
[AspectJ](https://eclipse.dev/aspectj/) is the canonical implementation,
separates a cross-cutting concern such as logging from the code it cuts
across: a *pointcut* names the places (every method entry), *advice* says
what to do there (record the name), and a *weaver* inserts the advice
without touching the source. Yggdrasil does exactly this, but weaves at
the KL level rather than in any target language, so it works on every
port without port code:

- pointcut: every emitted defun's entry, and every `(value V)`;
- advice: append `f<TAB>name<TAB>phase` or `v<TAB>name<TAB>phase` to a
  trace file, where the phase byte is `b` while `shen.initialise` runs and
  `p` afterwards;
- weaver: `--trace` on `shake`, `build` and `run`, plus one extra toplevel
  form after the last form of the last user file, which writes the
  end-of-run record `e<TAB>end` and closes the stream.

The advice uses only primitives, deliberately not `pr`, which is a kernel
function that reads `*hush*` and need not be in the footprint. The trace
stream is opened as the initialiser's first form. The helpers are added
after weaving so they are not themselves woven.

`yggdrasil trace-check PROG OUTDIR --target go` shakes traced, builds,
runs with the fixture's stdin, turns the trace into `called` and
`readGlobal` facts, and evaluates one query with the Shen engine:

```
uncoveredCall(F) :- called(F), kernel(F), !reach(F).
```

It must be empty. Taken on Yggdrasil `3c499a1`, shen-go `da55c5d`, host
shen-cl, 2026-09-22, on four fixtures and two targets out of the same
shen-go checkout — the Go artifact and shen-go's bare KL VM (`cmd/kl`).
Both are `builders.json` entries: `kl` used to be a runner special-cased
inside `trace.go`, and what was peculiar about it is declared on its entry
now (`program_file`, `stdin: appended-to-program`, `stdout:
repl-transcript` — section 11):

| fixture | reach | called (go) | called (kl) | globals read |
|---|---|---|---|---|
| fib | 53 | 34 | 32 | 3 |
| partial | 53 | 34 | 32 | 3 |
| stdin-sum | 54 | 38 | skipped | 4 |
| prolog | 66 | 41 | 43 | 5 |

The two do **not** agree name for name, and the disagreement is
informative rather than alarming. `go` records `<-vector` and `vector->`
on `fib` and `partial` that `kl` does not; `kl` records `shen.pvar?` and
`thaw` on `prolog` that `go` does not. All four are entries in shen-go's
`native_overrides`, and the trace is a property of the *runtime*, not of
the KL: a name whose binding is native when the program reaches it records
no entry. shen-go's generated `main` installs its natives *after*
`shen.initialise`, so `go` records the boot's KL bodies and misses any
override first entered afterwards; `cmd/kl` never calls
`InstallKernelFast` at all and lowers a different handful in its own
compiler. `analysis-rules.md` has the per-name table. Containment holds on
both readings, which is the check doing its job across a real runtime
difference. `stdin-sum` on `kl` is a named skip, not a pass: shen-go's
`cmd/kl` reads its program from stdin and cannot also be fed a fixture's
input. The skip is driven by the declared fact rather than by the target's
name — `stdin: appended-to-program` on the entry, `SKIP
stdin-appended-to-program` on the sentinel line — so a second runtime with
the same property is refused the day its entry says so.

The gap between reach and called is the static over-approximation made
visible: on the go artifact, sixteen to twenty-five kept functions per
program are never entered on that path. Injected violations, an
out-of-footprint call and an unwritten global, are both caught, and the
Shen engine, `refeval.py` and Soufflé agree on the verdict — section 6 on
what that agreement is and is not.

This is evidence, not proof: one run exercises one path. It is narrower
than that, even. On a port whose artifact *is* the slice, `uncoveredCall`
is empty **by construction** — a call to a kernel defun outside `reach` is
a name the artifact does not contain, so it is an undefined-function crash
and there is no finished run to read facts from. The query has teeth on a
`dispatch: full-kernel` build, on a host-side facts dump, and against a
`reach` computed from a different program than the one that ran, which is
the injected-violation case above. What the instrument measures on every
target is *coverage*, and `trace-check` says so on its own report line.

What it can now also do is fail for the reasons it claims to check. The
trace carries an end-of-run record, so a run that did not finish is
refused rather than read as a shorter one; the run's stdout is compared
with `tests/<name>.expected` in the same invocation, on the same artifact,
so a trace of a run that produced the wrong answer is not counted as
evidence; and the fact writer declares its row counts in
`FactsDir/trace.meta`, so a `called.facts` that is a strict prefix of the
run is caught by the host half alone. A case where no evidence can be
obtained at all — a fixture with stdin on a target that declares `stdin:
appended-to-program`, which is `kl`, since shen-go's `cmd/kl` cannot be fed
its program and the fixture's input on one descriptor — is a named skip,
not a pass.

It is also the floor of the port contract in section 11, because it is
identical in form on every target.

Design note: [analysis-rules.md](analysis-rules.md), "Runtime trace".

## 9. Static inclusion: SCIP and the graph extractor

Section 2 said KL equivalence transfers to the target language if the
backend compiles each function independently. That is checkable: build the
full program A and the shaken program A* with the same builder, and
compare the compiled artifacts function by function.

**[SCIP](https://github.com/sourcegraph/scip)** (SCIP Code Intelligence
Protocol) is Sourcegraph's index format for code navigation: for a
codebase it records every symbol, every definition, and every reference
with its range, as a [protobuf](https://protobuf.dev/) file. Indexers
exist for [Go](https://github.com/sourcegraph/scip-go),
[TypeScript](https://github.com/sourcegraph/scip-typescript), Rust (via
rust-analyzer), Java, Python and others. An index is exactly a static
reference graph, so indexing both artifacts and comparing the part
reachable from `main` is the inclusion check with off-the-shelf tooling.

`yggdrasil scip-check PROG OUTDIR --target go` does this: it builds A with
`--no-shake` and A* normally, with the same builder, and compares the two.

The first version ran `scip-go` on each module and decoded the index
in-repo (the `scip` CLI would not install here, so the wire format was read
directly). What that found was about the backend, not the shake:
**shen-go is not compositional at the Go level.** Its builder emits each KL
function as an anonymous closure bound at run time, and every call is a
symbol lookup. The generated `fib` module has 136 closures and four named
Go functions, all driver code, so the Go-level comparison was 4 against 4
and said nothing — and, being the trivial direction of an inclusion between
two sets of driver functions, it could not fail. That machinery is gone.

What the check does instead is recover the *KL-level* graph from the
generated Go with `go/ast`, with the run-time bindings as nodes and the
symbol occurrences in their bodies as edges, and hold the shake's own
footprint — the defun names in the shaken `kernel.kl` — against it. There
the statement has content, and it is asserted **by name**: any name in the
footprint the emitted graph cannot reach, and any name the graph reaches
that the shake did not keep, is printed as `kl-delta-missing` /
`kl-delta-extra` and fails.

Two things about that graph are easy to get wrong and were both wrong
once:

*Occurrences, not calls.* The generated code compiles a call to
`PrimFunc(symF)` and a quoted symbol to `PrimCons(symF, …)`, and only the
first looks like an edge. But the shake's edge relation counts **every**
kernel-defined symbol leaf of a body, in any position — `analysis.dl` D1,
"argpos yields an edge unconditionally, exactly like callpos" — because
the kernel constructs KL it may later evaluate (`(cons shen.f-error (cons
V761 ()))` in `shen.scan-body`). A call-position-only edge relation on the
artifact side therefore reports every such name as unreachable and
manufactures a residue out of the two sides disagreeing about what an edge
is. On `metaeval`, `partial-eval` and `tc-interp` that was fifteen names
apiece, none of them a finding.

*The body is behind a temporary.* `yggdrasil-build` emits
`tmpN := MakeNative(func(__e *ControlFlow){ … }, 2)` and then
`tmpM := Call(__e, ns2_1set, symdo, tmpN)`, so the binding's fourth
argument is an identifier, not the closure. An extractor that treats that
argument as the body finds no edges at all and every lookup lands outside
every body, seeding itself — which makes the reach set the whole node set
and the comparison vacuous.

The artifact's reachable set is not identical to the footprint, and every
difference has to be accounted for by a *declared* cause; anything else
fails. There are three declared causes, and no fourth.

`shen.initialise` is in the artifact's graph but not in `reach`, because
the shake synthesises it *after* reachability runs, from the kernel's
toplevel forms, and the generated `main` calls it. The user's own defuns
are likewise in the graph but not in the kernel footprint, because they
live outside `kernel.kl`; the shake already computes that set. The third
is the port's **special forms**: names that are kernel defuns in S42 but
that shen-go lowers inline with no symbol lookup, so they are never an
edge in the recovered graph. `do` is one — S42 defines it as a kernel
function while `src/compiler.shen` handles `(do X Y)` as a parse head —
and it is not the only one: the intersection of shen-go's `shenPrimitive`
table with `kernel.kl`'s defun names adds `not`, `symbol?`, `variable?`,
`integer?`, `read-file-as-bytelist` and `read-file-as-string`, each
emitted as a bare `PrimX` call because the kernel chunks are compiled
sealed.

That is the trap this stage is built to avoid: the first two fixtures
(`fib`, `hello`) leave only `do` and `not` in residue, and a declaration
fitted to *them* turns `scip-check` red on `metaeval` and `tc-interp` for
reasons that are not findings. A declared subtraction has to be derived
from the port's lowering rule, not from a fixture. So the port contract
(section 11) makes it a declared fact (`special_forms`, with a
`special_forms_source` naming the line of the port for each name) that the
check subtracts rather than a person — and the check audits the
declaration in turn, printing which names it `applied`, which went
`unused` in this slice, and failing on any declared name that is not a
kernel defun at all.

One node is deliberately not read the same way: the synthesised
`shen.initialise`. `trim-top` rewrites the arity table, the
external-symbols list and the lambda table inside it **against the
footprint**, so every kept name is quoted there by construction; following
those quotations saturates the reach set and makes the check vacuous
again. That is the same circularity the rules avoid by dropping those four
sites from `called-fns` (`analysis.dl` D2/D7), so the check counts what
the initialiser *calls* and not what it *quotes*.

The declared subtraction is also transitive, and has to be. shen-go emits
`PrimIsSymbol` at every call site of `symbol?`, so the kept defun
`symbol?` is never looked up — and neither is `shen.analyse-symbol?`,
which nothing else calls. Subtracting the declared name without its
subtree would report the subtree as a finding. The subtree is derived from
the same declaration over the graph the backend emitted, never declared
separately, and the verdict prints it apart from the declaration as
`called-only-from-them=`.

Measured on the repo fixtures (shen-go `da55c5d`): `fib`, `hello`,
`prolog`, `metaeval`, `partial-eval` and `tc-interp` all pass with an
empty residue on both sides. `tests/interpreter.shen` fails, with
`kl-missing-in-full fix` and `kl-missing-in-full shen.fix-help` — an open
finding, not a defect in the slice. The cause is the other half of
`trim-top`: the *shaken* initialiser materialises the lambda table as
`(cons fix (lambda X1 (lambda X2 (fix X1 X2))))`, a real call site, while
the full build's initialiser calls `shen.build-lambda-table` and fills the
table at run time. So A\* reaches `fix` statically and A does not, even
though A reaches it when it runs. Either the A-side extractor learns to
follow `shen.build-lambda-table`, or check (2) stops counting the
initialiser's own edges; both are changes to the graph, not subtractions
from the residue, and the choice is a person's.

The lesson generalises: SCIP is one way to get a graph out of an artifact.
The contract in section 11 fixes the *graph* (three relations: `node`,
`edge`, `body`) and the *query*, and lets each port supply an extractor:
SCIP for direct-call languages with an indexer, source patterns or the
language's own parser otherwise, the identity for interpreters whose
artifact is the KL itself.

What it cannot see: runtime lookups and computed names. That is the same
blind spot as the shake, so it is an independent oracle for the same
over-approximation, not a soundness proof.

Design note: [analysis-rules.md](analysis-rules.md), Stage 5.

## 10. Behavioural parity

The oldest check, and still the top of the ladder: `yggdrasil parity`
runs the same shaken slice on every target with a toolchain present,
diffs stdout against a golden and against itself across two boots and two
in-process passes. It is the only check that observes the *runtime* rather
than the artifact's text, and the only one that can catch integer width,
hash iteration order, and memoisation drift, which is how it caught
identical KL computing different answers on shen-rust.

Design note: [parity.md](parity.md).

## 11. The port contract

The checks above were built against one port. What an arbitrary port must
declare and satisfy for them to apply is a five-level ladder:

| level | what | port obligation |
|---|---|---|
| 0 | builder contract | load `kernel.kl`, call `shen.initialise`, run user files in order |
| 1 | self-description in `builders.json` | truthful `port_reads`, `special_forms` and `native_overrides` (the three keys that exist), each with a `<key>_source` saying where it was read off and a `<key>_checked_by` naming the test that fails when it drifts, or `none`; plus `port_writes`, `native_deps`, `call_style` and `dispatch`, which are **proposed** — the report prints a row for each, and today every target says `unknown` |
| 2 | runtime trace | flush open streams at exit, or declare that you cannot |
| 3 | static inclusion | an extractor producing `node`/`edge`/`body` facts from a built artifact |
| 4 | behavioural parity | already met by every target with a golden |

Two obligations at level 1 deserve a mention. A **native override**
(shen-go's `InstallKernelFast`, shen-scheme's `overrides.scm`) replaces a
kernel function with native code whose callees the rules cannot see; the
port must enumerate them or be reported unsound at level 1 — and must say
*when* the swap happens, because on shen-go the generated `main` calls
`shen.initialise` before `InstallKernelFast`, so every override's KL body
runs during the boot. And **dispatch**: a port that links the whole kernel
behind the slice is not running the shaken program, and every higher check
is vacuous for it. `dispatch` is a proposed key: no target declares it and
nothing reads it, so the report prints `unknown` for every target.

The output is `yggdrasil contract --target P`: one line per level-1 fact,
marked **verified** (a `_checked_by` names a test; read the string for what
that test catches and in which direction), **declared** (stated, with or
without a source, and nothing checks it), or **unknown** (the key is
absent, which is not the same claim as "this port has none"). Levels 2 to 4
are commands rather than declarations, and the report names them rather
than pretending to have run them. The rows marked *declared* rather than
*verified* are, exactly, what that port adds to the core of trust.

There is no `_verified` boolean. There were two, spelling one word over two
unrelated predicates -- "each entry was read off a named source line" and
"the list equals the symbols `InstallKernelFast` rebinds" -- and one of the
two was false at the time it was read. Provenance and the name of the check
are now separate strings, because they are separate facts.

Design note: [port-contract.md](port-contract.md), including a staging
plan whose last step is taking shen-lua through the ladder.

## 12. What is proved, what is evidence, what is assumed

Sections 3 to 11 each establish something different, and it is easy to
lose track of which kind of thing. This table is the whole guide in one
place.

The `kind` column used to carry two questions at once — what kind of
statement a row is, and how often anything re-establishes it — so they are
now separate columns.

`kind` is:

- **checked**: a deterministic computation that fails loudly, and the
  `re-established by` column names the test or command in this repository
  that fails when the claim is false. No named test, no `checked`.
- **evidence**: a check that can only observe the runs or fixtures it was
  given.
- **assumed**: a premise written down and reported, but not established by
  anything here.

`re-established by` is one of `CI on every commit`, `go test with a host`,
`nightly`, or `once, by hand, at <commit>`. A row whose answer is the last
of those is a measurement, not a guard: nothing re-takes it, and it will
drift.

Taken on Yggdrasil `3c499a1`, shen-go `da55c5d`, host shen-cl, 2026-09-22.

| claim | kind | established by | re-established by | section |
|---|---|---|---|---|
| The footprint is the least fixpoint of the published rules | checked | `TestAnalysisOracleMatchesShake` (`analysis_test.go`): Soufflé and/or `analysis/refeval.py` and the Shen engine agree on `reach` for every fixture in both modes, and are diffed against each other when both are present. Soufflé is the only one of the three that is not a transcription of the shake (section 6) | `go test with a host` for the Python leg; the Soufflé leg is `CI on every commit` via `.github/workflows/analysis-oracle.yml`, on `push` to `main` and to this branch, `nightly` at 05:41 UTC, and on a path-filtered `pull_request`, against the shen-go host pinned in `.github/shen-go.ref` | 5, 6 |
| The rules describe the shake that ships | checked | the same test: `reach` equals the defun set of the `kernel.kl` that same shake wrote, on every fixture. Deviations D1 to D10 in `analysis/analysis.dl` record where the design had to bend to the code — which is also why the rules are a transcription and not an independent statement | `go test with a host`, and `CI on every commit` / `nightly` in `analysis-oracle.yml` | 5, 6 |
| The shake's output is byte-identical across the eight ports for one shake; it is *not* byte-identical across versions of Yggdrasil, and the D8 ordering change is where that was given up | checked for the port half, `once, by hand` for the version half | The port half is what the tool guarantees and what `yggdrasil parity` exercises. The version half was a self-imposed constraint, and stages 1 to 3 each verified it by hand against the previous binary — until D8 (`9084812`), which deleted the duplicate depth-first traversal that existed only to keep those bytes still, and moved exactly one line of `kernel.kl` (the synthesised `shen.initialise`, whose lambda-table literal is a permutation of the same entries) on the eval-free fixtures. Nothing re-runs a cross-version byte comparison today. What *is* guarded is the property that replaced it: `TestFootprintOrderIsKernelLoadOrder` (`footprint_test.go`) fails if the footprint's order is not kernel load order, on every fixture that shakes. The manifests were never byte-identical either — stage 2 added `init-order=`, stage 3 `computed-names=`, stage 4 `pruned-init=`, and the manifest version has since been bumped to 4; the honest claim is that each stage changed them by exactly the lines it declared | `go test with a host` for the order property; `once, by hand, at 9084812` for the byte comparison | 6, 7, 8, 9 |
| The footprint is minimal for the rule set | checked, and qualified below | `TestFootprintEnginesAgree` (`footprint_test.go`) on every fixture that shakes: the worklist, the Warshall closure and the rules agree, so nothing reachable is dropped and nothing unreachable is kept, *relative to the rules' notion of an edge*. The Warshall leg is skipped above 150 nodes and says so | `go test with a host` | 3, 6 |
| No toplevel form reads a global before it is written | checked, with an over-approximated write side | the init-order rules, on the final form sequence, refuse the shake on violation; `initorder_test.go` (`TestInitOrderBadIsRefused`, `TestInitOrderLaterWriteStillRefused`, `TestInitOrderOKShakesAndRecords` and the `checked-weak` fixtures) fails when they stop doing so, and `TestAnalysisOracleMatchesShake` diffs `readBeforeWrite` and `weakRead` across the engines like any other relation. The weakness is on the **write** side and is deliberate: a form that *calls* a function that *might* write `V` counts as writing `V`, whether or not that call runs. A read discharged only that way — or by a same-form write, or one inside a `freeze` the form thaws — records `init-order=checked-weak` rather than `checked`, which is how the manifest says which kind of answer this was | `go test with a host`, and `CI on every commit` for the rule-level diff | 7 |
| Every function actually entered on a run was in the footprint | evidence | the woven trace and the containment query, on four fixtures and two targets (`TestTraceCheckFixtures`, `trace_test.go`). Coverage, not soundness: on a port whose artifact *is* the slice the query is empty by construction, and `trace-check` prints that caveat on its own report line. What the run does establish is that it finished (an end-of-run record), that it produced the fixture's committed answer, and that the facts were not truncated | `go test with a host` | 8 |
| The backend compiled the same kept functions the same way in the full and shaken builds | evidence, and only for compositional backends | the KL-level graph recovered from the generated Go with `go/ast` (`TestScipCheckFixtures`, `scip_test.go`), asserted **by name**: any residue at all fails. On `tests/fib.shen --target go` the accounted subtractions are the port's declared `special_forms` (`do`, `not` applied here, each cited to a line of shen-go), the user's own defuns (`fib`), and the synthesised `shen.initialise` — `footprint=53 reachable=53 accounted=2`, `full defuns=688 reachable=545`. `tests/interpreter.shen` still fails with `kl-missing-in-full fix` / `shen.fix-help`, an open finding printed by name rather than subtracted. There is no Go-level SCIP comparison any more; that path compared four generated driver functions and was deleted | `go test with a host` | 9 |
| The shaken artifact computes what the full program computes | evidence | the parity gate against goldens, across targets, boots and passes (`.github/workflows/parity-gate.yml`) | `CI on every commit` for the targets the gate builds; `go test with a host` for the fixtures' goldens | 10 |
| No function name is computed at runtime | assumed, reported | `computed-names=` in the manifest; the shake warns when it is not `none`. Nothing refuses a program on it — the manifest makes the hypothesis visible, and that is all. `TestComputedNameWarnsAndRecords` / `TestComputedNameNoneIsRecorded` check that the reporting works, not that the hypothesis holds | `go test with a host` for the reporting; the hypothesis itself is re-established by nobody | 7 |
| The port's self-description is truthful | assumed, reported; `verified` only where a named test exists | each `builders.json` fact carries a `_source` and a `_checked_by`, and `yggdrasil contract --target P` prints both. Exactly two rows read `verified` anywhere today, both on `go`: `port_reads` (`TestPruneInitGoArtifactStillRuns`) and `native_overrides` (`TestNativeOverridesMatchKernelFast`, which parses `InstallKernelFast` in the sibling checkout and compares both ways). `go`'s `special_forms` reads `declared` because its `special_forms_checked_by` is `none`, which is the literal truth about the declaration — though `TestGoBuilderDeclaresSpecialForms` (`scip_test.go`) does pin the seven names and require the source to cite a line of shen-go for each, and `TestSpecialFormsAudit` covers the residue accounting. Naming one of them in `builders.json` would make the row `verified`; nobody has. `port_writes`, `native_deps`, `call_style` and `dispatch` are `unknown` on every target, which is the report saying nobody declared them, not that the port has none. Thirteen targets declare no `port_reads` of their own and inherit `_default`, whose value is now the literal string `unknown` rather than a 35-name conservative guess -- so those rows read `unknown` too, and `--prune-init` refuses them by name (`port-contract.md`, "`unknown` is a value"). Every target also declares `stdin` and `stdout` by inheriting `_default`'s ordinary run contract; `kl` is the only one to state the other value of either | `go test with a host` for the two verified rows; the rest is re-established by nobody | 11 |
| The backend compiles KL correctly | assumed | the port's own kernel test suite; the same assumption for the full and the shaken program | not re-established here at all | 2, 9 |

The last row is the one Bruno Deferrari raised on
[the Shen group](https://groups.google.com/g/qilang/c/duE01tE5oIU), and
James Fetzer's
[*Program Verification: The Very Idea*](https://dl.acm.org/doi/10.1145/48529.48530)
(1988) is the classic statement of it: every verification rests on a core
of trust certified by engineer's induction. The contribution here is not
to remove that core but to make its contents a list: the rows marked
*assumed*.

Four gaps in the table above are known and deliberately left open rather
than quietly fixed: nothing empirically checks a **pruned** artifact
beyond one `fib` stdout comparison on `go`, because `trace-check` shakes
with pruning off (`analysis-rules.md`, "Against stage 4"); `native_deps`
and `dispatch` are proposed keys with no data and no consumer
(`port-contract.md`); thirteen of the fourteen targets' `port_reads` is
`unknown` -- the honest value, and one nobody has replaced with a
measurement (`port-contract.md`, "`unknown` is a value"); and shen-go's
natives are installed *after* `shen.initialise`, which is the port's to
change, not Yggdrasil's (`lowering.md`, "The measurement").

One gap that was on this list is closed: `kl` was a special-cased runner
counted in places as a runtime, and is a `builders.json` target now
(`analysis-rules.md`, "Targets, and a port caveat").

### Is this the smallest possible artifact?

No, and it is worth being precise about the three ways it is not, because
they are different in kind.

**Unentered functions.** The trace in section 8 shows sixteen to
twenty-five kept functions per program that a given run never enters. Some
of these are genuinely reachable on other inputs; some only through the
rules' over-approximation (a symbol mentioned as data, never applied). The
rules could be sharpened, at the cost of a soundness argument for each
refinement, and section 3 measured the ceiling on that: about eleven
functions on the eval-free floor. The shake is minimal *for its rules*; the
rules are deliberately coarse because the coarseness is what makes them
easy to prove sound.

**Dead initialisation.** Section 7's pruning removes about 6% of
`kernel.kl` by dropping literal sets nothing reads. It is off by default
only because its correctness depends on a declared fact per port.

**Everything else is optimisation, and it is a different problem.** The
shake removes; it never rewrites. A port that inlines a small kernel
function into its caller, fuses `(f (g x))` into one compiled function,
specialises `shen.app` for a statically known type, or unboxes a number,
will produce a smaller and faster artifact than anything the shake alone
can. Two things follow.

First, such a port is still covered by the KL-level argument in section 2,
because the shake happens before the backend and the backend sees the
same KL for the full and shaken programs. What changes is which
*evidence* is available. A backend that fuses functions is not
compositional: the code it emits for `f` depends on whether `g` is present
and on what else calls `g`, so the body comparison in section 9 is
meaningless for it, and the port declares as much (`call_style` in the
contract). For such a port the transfer step rests on parity (section 10)
and on the backend's own correctness, exactly as it does for SBCL or Chez
today. That is not a weakness introduced by optimisation; it is the same
core of trust, with one more component in it.

Second, if the optimisation is done at the KL level *by Yggdrasil* rather
than by a port, it becomes a KL-to-KL transformation, and every such
transformation carries its own equivalence obligation. This is precisely
the gap Tarver identified for the kernel's own factorisation and
triple-stack rewrites: elegant, working, and without a proof of
extensional equivalence. Yggdrasil's checks certify the *removal* of code;
they say nothing about a rewrite. A KL-level optimiser would need its own
rule set, its own oracle, and its own trace check, and the machinery in
this guide is the template for building them, not a substitute.

## 13. Reproducing everything

Toolchain used for the numbers in this guide:

- a Shen host (the reference is shen-cl; shen-go also works). Set
  `YGGDRASIL_HOST`.
- [Soufflé](https://souffle-lang.github.io/install) 2.4.1 (apt, Homebrew,
  or Nix all carry it).
- the sibling shen-go checkout, for the go target. Set
  `YGGDRASIL_SHEN_GO_DIR` if it is not at `../shen-go`.

Then:

```
go build -o yggdrasil_bin .
PATH=/path/to/souffle:$PATH \
YGGDRASIL_HOST=/path/to/shen \
YGGDRASIL_SHEN_GO_DIR=/path/to/shen-go \
go test -count=1 ./...
```

runs every check described above. Measured at Yggdrasil `3c499a1` on
shen-cl with a sibling shen-go at `da55c5d`: `go test ./... -count=1`
takes 216 s and needs `-timeout 40m` on a slower machine. Soufflé is
optional locally — without it `analysis_test.go` uses `refeval.py` alone,
and the two are diffed against each other only in CI.

CI pins its host: `.github/shen-go.ref` carries one SHA that
`analysis-oracle.yml`, `parity-gate.yml` and `go.yml`'s host job all read,
so a red run is attributable. The numbers in this guide were taken against
a local shen-go checkout at `da55c5d`, which is not that pin.

Individual commands:

```
yggdrasil why tests/fib.shen --trace pr
yggdrasil facts tests/fib.shen out/facts && souffle -F out/facts -D out analysis/analysis.dl
yggdrasil shake tests/init-order-bad.shen out/          # refused
yggdrasil shake tests/fib.shen out/ --prune-init --target go
yggdrasil trace-check tests/fib.shen out/ --target go
yggdrasil scip-check tests/fib.shen out/ --target go
yggdrasil parity tests/fib.shen out/ --expect tests/fib.expected
```

Tests that need a tool that is absent skip with a message naming it; they
never pass vacuously — and since `04d213d` the pipeline enforces that at
the job level rather than per test. `.github/workflows/go.yml` asserts an
**exact** host-less skip count (`NO_HOST_EXPECTED_SKIPS`, 33 as of
`3c499a1`, with the enumeration written out name by name in the file) and
runs a second `host-test` job with a real Shen host and a sibling shen-go
in which at most one test (`HOST_MAX_SKIPS: 1`, the parity helper
subprocess) may skip. Before that job existed every run of the workflow
was green without a host, which is to say the whole of the machinery in
this guide skipped. Neither number has been observed on a real Actions
run from this branch.
