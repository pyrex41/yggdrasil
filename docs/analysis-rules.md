# Design note: the shake as a rule set

**Status**: stages 1–5 shipped, with the runtime trace beside them
(Yggdrasil, September 2026)
**Code**: `analysis/analysis.dl`, `analysis/refeval.py`; `yggdrasil.shen` —
`ygg.dl`, `*shake-rules*`, `ygg.*init-rules*`, `*trace-rules*`,
`yggdrasil.facts`, `yggdrasil.footprints`, `yggdrasil.shake-full`,
`yggdrasil.shake-traced`, `yggdrasil.trace-check`; `main.go` — `cmdFacts`;
`prune.go`; `scip.go` — `scip-check`; `trace.go` — `cmdTraceCheck`;
`builders.json` — `port_reads`; `analysis_test.go`, `footprint_test.go`,
`prune_test.go`, `scip_test.go`, `trace_test.go`

**Motivation**: Mark Tarver, *The Future of Shen* (Shen group): Yggdrasil
enters the core of trust next to the kernel and the backend, so it should be
small and legible. Today the shake is correct but its decisions are spread
over pattern-match clauses (`called-fns`'s four table exceptions,
`*eval-entry-points*`, `strip-f-error-row`, `trim-top`, the lambda-table
filter). This note restates them as a Datalog rule set, adds the two checks
the thread asked for that do not exist yet (initialisation order, dead
initialisation), and says how to evaluate the rules without adding a new
toolchain to the trust core.

## What this is not

It is not a precision project. Measured on S42 without eval stripping, the
most aggressive syntactic edge rule (call position only, which is unsound)
shrinks the graph from 2587 to 2414 edges and the floor from 661 to 650
defuns. The footprint is decided by the eval step (548 to 48), which is a
symbol test. See `docs/why.md`. Precision-grade engines (Doop-style CFA on
Soufflé) would optimise the wrong term on a 686-node graph the worklist
already reaches in milliseconds.

It is also not a new dependency. Stage 1 stays pure Shen, running on all
eight ports with byte-identical output. Soufflé appears only as an offline
oracle in CI, the role the Warshall closure plays today.

## Facts (extracted from syntax, one pass over kernel KL and user KL)

```
defun(F)                     F is a kernel or user defun
kernel(F)                    F is a kernel defun
top(N, F)                    the N-th kept toplevel form, F its name or "top"
callpos(F, G)                G appears in call position inside F's body
argpos(F, G, C)              G is a bare symbol argument to call C inside F
datasym(F, G)                G appears inside a data literal in F (cons chains,
                             arity table, external-symbols, declare, *special*)
usersym(S)                   S is any symbol in the user KL
entry(S)                     S in *eval-entry-points*
reads(N, V)                  toplevel form N evaluates (value V)
writes(N, V)                 toplevel form N evaluates (set V _)
readsIn(F, V)                defun F evaluates (value V)
portGlobal(V)                V is supplied by the port (*stinput*, *stoutput*)
portReads(V)                 the port's runtime reads V natively (*hush* ...);
                             one fact list per builder in builders.json
prim(P)                      P in *primitives*
cap(C, P)                    capability C is gated by primitive P
userintern(F)                user defun F (or "top") mentions `intern`
userglobal(F)                F has a (value X)/(set X _) with X not a
                             literal symbol
```

The relations above are the sketch. The last two are stage 3's computed-name hypothesis.
What `yggdrasil facts` actually dumps
is that list with `form-mentions` split into `formmentions` (raw) and
`formmentionsef` (after `prepare-tops`), `usersym` joined by `rawsym` (the
user KL before `strip-user-declares`), `mentions` narrowed to
`mentionsprim`, and `initprim` added; `reads`/`writes`/`readsIn`/
`portReads` were stage 2 and 4 and are all dumped now (stage 4), with
`defunwrite` and the runtime trace's `called`/`readGlobal` beside them —
the last two empty until `yggdrasil trace-check` fills them. That is
twenty-four relations. See the declarations at the top of
`analysis/analysis.dl`.

## Rules

Mode.

```
evalcapable :- usersym(S), entry(S).
```

Edges. The last clause is what the four hand-written exceptions in
`called-fns` say today: a symbol in a data literal is an edge only when the
program can evaluate code, because only then can the kernel turn a name
into a call at runtime.

```
edge(F, G) :- callpos(F, G), kernel(G).
edge(F, G) :- argpos(F, G, C), kernel(G), C != cons.
edge(F, G) :- datasym(F, G), kernel(G), evalcapable.
edge(shen.f-error, G) :- callpos(shen.f-error, G), evalcapable.
```

Reach.

```
seed(G)      :- top(_, _), form-mentions(N, G), kernel(G).
seed(G)      :- usersym(G), kernel(G).
floorseed(G) :- top(N, _), form-mentions(N, G), kernel(G).
reach(G)     :- seed(G).
reach(G)     :- reach(F), edge(F, G).
floor(G)     :- floorseed(G).
floor(G)     :- floor(F), edge(F, G).
```

Attribution (what `yggdrasil why` prints; per-row reach is the same rule
with a different seed set, so it is a query, not a new rule).

Primitives and capabilities.

```
usedprim(P)  :- prim(P), (reach(F), mentions(F, P) ; usersym(P)).
reaches(C)   :- cap(C, P), usedprim(P).
needsEval    :- usedprim(eval-kl).
```

Initialisation order. This is the check the thread asked for, and the shake
runs exactly these rules (`ygg.*init-order-rules*` in `yggdrasil.shen`). A
form may read a global only if some form up to and including itself wrote it
-- with its own `(set V _)`, or by applying a function whose body sets it --
or the port supplies it. `succ` is the index order of the final form
sequence: kept toplevel forms first, then the user files' forms after the
synthesised initialiser, in source order.

```
wantedGlobal(V)           :- reads(_, V).
writesVia(F, V)           :- defwrite(F, V).
writesVia(F, V)           :- fcall(F, G), writesVia(G, V).
topWrites(N, V)           :- formwrite(N, V).
topWrites(N, V)           :- formcalls(N, G), writesVia(G, V).
writtenBefore(N, V)       :- topWrites(M, V), succ(M, N), wantedGlobal(V).
writtenBefore(N, V)       :- writtenBefore(M, V), succ(M, N).
directWrittenBefore(N, V) :- writes(M, V), succ(M, N), wantedGlobal(V).
directWrittenBefore(N, V) :- directWrittenBefore(M, V), succ(M, N).
covered(N, V)             :- writtenBefore(N, V).
covered(N, V)             :- topWrites(N, V), wantedGlobal(V).
readBeforeWrite(N, V)     :- reads(N, V), !covered(N, V), !portGlobal(V).
weakRead(N, V)            :- reads(N, V), covered(N, V),
                             !directWrittenBefore(N, V), !portGlobal(V).
```

The order is an EDB relation, not the side condition `M < N`, because the
shake's own engine has no arithmetic; materialising it is what lets all
three engines run the same rules rather than three dialects of them (D11).
It is `succ`, the adjacent pairs walked transitively, rather than a tuple per
`M < N` pair, and `wantedGlobal` restricts both closures to the globals some
form actually reads -- a magic-set restriction written out by hand. Both are
there for the shake's engine, not for Soufflé: unrestricted, the closures are
~14,000 tuples on `tests/metaeval.shen`, derived to answer a question about
that program's single toplevel read.

Three things widen the write side, and each one exists because a program
that runs correctly was being refused with a hint -- *reorder the program* --
that could not be acted on:

```
(define setup -> (set *cfg* 41))     (do (set *x* 1) (print (value *x*)))
(setup)                              (thaw (freeze (set *f* 1)))
(print (value *cfg*))                (print (value *f*))
```

the call graph (`writesVia`, D13), a form's own writes covering its own reads
(`covered`'s second clause, D15), and a write walk that descends into `freeze`
and `lambda` (`formwrite`, D15 -- `writes` deliberately does not, because
stage 4 must not treat an unthawed freeze as an initialisation).

Any `readBeforeWrite` fact fails the shake with the form and the variable
named; what is left refused is a read with no write anywhere up to its own
form, which is exactly the program a reordering can fix. `weakRead` decides
nothing; it reports how the reads were discharged. None of the three
widenings is precise -- a call *could* write `V` rather than does, nothing
knows in which order a `do` runs or whether a freeze is thawed -- so a read
only they cover is covered more weakly than one a literal earlier write
covers. `weakRead` is exactly that set: `directWrittenBefore`, the relation
that decides `checked`, stays strictly-earlier and freeze-blind, and the
manifest says `init-order=checked` when `weakRead` is empty and
`init-order=checked-weak` when it is not. Measured today on
`tests/fib.shen`: the slice reads 6 globals, the initialiser writes 35, and
the only read without a prior write is `*stoutput*`, which is a port global,
so both output relations are empty and fib records `checked`. The rules'
value is that they keep deciding this when the kernel's boot order or the
user's toplevel forms change.

Dead initialisation.

```
liveGlobal(V) :- readsIn(F, V), reach(F).
liveGlobal(V) :- reads(_, V).
liveGlobal(V) :- portReads(V).
deadInit(N, V) :- writes(N, V), !liveGlobal(V).
```

A `deadInit` form can be dropped from the synthesised initialiser. Measured
on fib against the `go` runtime's `portReads`: 29 of the 35 globals the
initialiser sets are dead, and 28 of the forms that set them are prunable.
This is the one place the rule set makes the artifact smaller, and it is
gated on a per-builder fact list that has to be written by reading each
port's runtime, so it ships after the checks, not with them.

Two clauses of this rule set are not in the note's sketch, and both are
recorded as deviations (D9, D10) in `analysis.dl`. The first is the
over-approximation the rest of the shake already makes: a global whose name
occurs *anywhere* in the user KL, in any position, is live, because the
shake's soundness argument is symbol occurrence and nothing narrower.
(`rawsym`, not `usersym`: whether the name is in the program the user wrote
is a mode-independent fact, and `strip-user-declares` must not be able to
make a global look dead.) The second is that the rules decide only which
*indices* are dead — what is *prunable* is narrower, and is not a rule.

## Engine

Shen Prolog cannot evaluate `reach` directly: no tabling, and the call
graph is cyclic. The rules above are Datalog, so a bottom-up semi-naive
evaluator over tuples is enough. That evaluator is `ygg.dl` in
`yggdrasil.shen` (stage 3, shipped): about 150 lines, no new primitives,
indexed on the property store and stratified. The rules are a Shen data
structure, `(value *shake-rules*)`, in the same file, which keeps them
readable to the same audience that reads the kernel and leaves them
available to later Shen2logic-style reasoning.

Soufflé evaluates the same rules from a `.dl` file in CI, fed by a fact
dump the shake writes when asked. Its output must match the Shen
evaluator's footprint, set for set, on every fixture. Disagreement is a
bug in one of them. This is the differential oracle role; Soufflé never
runs in a user's shake.

## Runtime trace (AspectJ-style weaving at the KL level)

The rules above derive `reach`: the kernel defuns a program **can** call. The
soundness obligation under the whole shake is that nothing outside `reach`
ever runs. That is argued on paper — a name the artifact can call occurs
syntactically in the artifact — and stage 3's `computedName` records the two
ways the argument can be broken. This is the other half: **evidence**, from a
real run, per target.

`yggdrasil shake|build|run --trace` weaves advice into the KL the shake is
about to write, and `yggdrasil trace-check PROG OUTDIR --target T` shakes,
builds, runs, and evaluates the containment query against the trace.

### Pointcut and advice

It is aspect weaving in the AspectJ sense, done at the **KLambda IR** rather
than in any backend. That is the whole design decision: the woven artifact is
ordinary KL, so one weaver serves all eight stage-2 targets and the thing
being measured is the thing the shake actually emits.

| | |
|---|---|
| pointcut | every `defun` entry, in `kernel.kl` and in every user file |
| pointcut | every `(value V)` whose `V` is a literal symbol |
| advice | append one record to a trace stream |
| join model | around for the read (record, then perform it), before for the entry |

```
(defun F Args Body)   ->  (defun F Args (do (ygg.traced F) Body'))
(value V)             ->  (ygg.traced-value V)
```

`ygg.traced-value` records `V` and then returns `(value V)`, so the advice
observes the read without replacing it. A `(value X)` whose `X` is a KL
variable is left alone: it names a global only at run time, and that case is
exactly what `computedName` already reports.

Weaving runs **after** the footprint, `rewrite-f-error`, `trim-top` and the
init-order check, and immediately before anything is written, so it cannot
perturb a decision the shake made. A traced `kernel.kl` has exactly the
defuns of the untraced one plus five helpers, and the manifests gain
`traced=true` and `trace-file=yggdrasil.trace`. With `--trace` off the weaver
is the identity and the output is byte-identical.

### The advice's own dependencies

The five helpers are emitted into `kernel.kl` and must satisfy two
constraints that the ordinary shake never has to think about.

They **must not be woven themselves** — `ygg.traced` calling `ygg.traced` is
unbounded recursion — so they are appended after the weave, not before it.

They **must not depend on the footprint**, because the footprint was decided
before they existed and re-deciding it would make `--trace` change the very
answer it is checking. So they use only KL primitives: `open`, `write-byte`,
`string->n`, `pos`, `tlstr`, `str`, `value`, `set`, plus `if`/`let`/`do`. In
particular **not `pr`**, which looks like a primitive and is not: it is a
kernel defun in `writer.kl` that reads `*hush*`, and a program whose
footprint excludes it would weave a call to a function that is not there.

The stream is opened by the synthesised initialiser as its **very first
form**, ahead of the initialiser's own entry advice, so no traced entry can
run before the stream exists — including the entries inside `shen.initialise`
itself, which is where the great majority of them happen.

The record format is a tag byte, a tab, the name, a newline: `f<TAB>NAME` for
an entry, `v<TAB>NAME` for a read. Tag, separator and terminator are written
as bytes, so `kernel.kl` carries no string literal with a control character
in it.

Weaving adds primitives, so `primitive=` grows (`open`, `write-byte`,
`string->n`, `pos`, `tlstr`). On every fixture measured, `reaches=` and
`cannot-reach=` are unchanged — those primitives are already in the slice of
anything that prints — and `needs-eval=` cannot move, since the weaver
introduces no eval entry point. `trace_test.go` asserts the `needs-eval=`
half, which is the one that would silently disqualify `--web`.

### The check

Two relations are added to `analysis/analysis.dl`, mirrored in
`(value *trace-rules*)` in `yggdrasil.shen` and in `analysis/refeval.py`:

```
uncoveredCall(F) :- called(F), kernel(F), !reach(F).
uncoveredRead(V) :- readGlobal(V), !initwrite(V), !defunwrite(V),
                    !portGlobal(V).
```

`called` and `readGlobal` are `.input` relations, empty in an ordinary
`yggdrasil facts` dump and filled by `trace-check` from a run. `kernel(F)` is
what keeps user defuns, the weaver's helpers and the synthesised
`shen.initialise` out of the first rule: none of them is a row of the kernel
call graph, so `reach` could not have derived them and a run entering them
says nothing about the footprint. `initwrite` is the globals a kept
**toplevel** form writes (stage 4's `writes`, computed by the same
`ygg.io-writes` the init-order check's `directWrittenBefore` half uses);
`defunwrite` is the globals a
kept **defun body** writes, which is what stops `uncoveredRead` from flagging
every counter the kernel maintains at run time (`shen.*call*`, `shen.*infs*`,
`shen.*gensym*`).

`uncoveredCall` is necessarily empty if the shake is sound, so a non-empty
one is a counterexample to the shake, not to the trace.

The rules live in a program of their own rather than in `(value
*shake-rules*)`, for one reason: their EDB is derived from `trim-top`'s
output, which the shake only has *after* the shake rules have run, and a
stratum that derives nothing has no business costing every shake a round.
They are written to be read side by side with the block at the foot of
`analysis.dl`.

### What it checks, and what it cannot

It checks that on the inputs tried, on that target, the artifact called
nothing outside its footprint and read no global nothing writes. That is
evidence for soundness obligation 1, **not a proof**: a run exercises one
path, and a different input can enter a function this one did not.

The converse containment does not hold and is not asserted. `reach ⊋ called`
on every fixture, which is ordinary imprecision (`called-fns` is a
position-insensitive cons walk, D1) plus the fact that one run takes one
path. Shrinking that gap is not what this is for; `docs/why.md` explains why
precision is the wrong thing to optimise on a 686-node graph.

### Measured

`yggdrasil trace-check FIXTURE OUT --target go`, stage-1 host shen-go,
stage-2 shen-go's `yggdrasil-build`. All four `OK`; Soufflé and
`analysis/refeval.py` agree with the Shen engine on `uncoveredCall` and
`uncoveredRead` over the same fact dirs, both on these traces and on traces
with an out-of-footprint call injected.

| fixture | mode | `reach` | `called` | `readGlobal` | records |
|---|---|---|---|---|---|
| `fib` | eval-free | 53 | 34 | 3 | 49,078 |
| `partial` | eval-free | 53 | 34 | 3 | 27,176 |
| `stdin-sum` | eval-free | 54 | 38 | 4 | 27,545 |
| `prolog` | eval-free | 66 | 43 | 5 | 27,646 |

So a run enters 64–70% of the footprint. The 30–36% that never runs is the
kernel's boot machinery on paths this input does not take, plus the
over-approximation the edge rule is built on; it is what the shake keeps
because it cannot prove otherwise, which is the correct trade.

The `kl` runner agrees with `go` on all four, name for name — worth knowing,
because it says the compiled backend introduced no call the interpreter did
not. It is not guaranteed to: the trace is a property of the **runtime**, not
only of the KL, and a runtime that binds a kernel function natively records
no entry for it. Run against shen-go master's VM, which binds `<-vector` and
`vector->` natively, the `kl` figures for `fib`, `partial` and `stdin-sum`
come out two lower for exactly that reason. Both readings are contained,
which is the check doing its job across a real runtime difference rather
than in spite of one.

The three globals of `fib` are `*hush*`, `*property-vector*` and
`*stoutput*` — the first two written by the initialiser, the third a
`portGlobal`. `prolog` adds `shen.*infs*` and `shen.*prolog-memory*`.

Weaving does not move the manifest's decisions: on all four, the traced
shake's `needs-eval=`, `reaches=` and `cannot-reach=` lines are identical to
the untraced shake's. `primitive=` does grow, by the weaver's own
`open`/`write-byte`/`string->n`/`pos`/`tlstr`.

### How it composes with the other two flags

`--prune-init` and `--trace` compose, and deliberately: the weaver is the
last thing that runs before the writer, so it sees the initialiser that
survived pruning and a trace therefore describes the artifact as shipped.
(It is also how the two checks could be pointed at each other — see "Against
stage 4" below.)

`--no-shake` and `--trace` are **refused together**. They ask for
contradictory artifacts: `--no-shake` emits the full program as the
reference A for `scip-check`, and the trace exists to check a *slice*
against its own `reach`. Tracing A would compare a run of the unshaken
program against the shaken program's footprint, which is not a question
anyone asked; and A's initialiser is eval-capable, so the trace would be
dominated by machinery the shipped artifact does not contain. `shakeExpr` in
`trace.go` is the one place that chooses a Shen entry point, and it returns
the error there rather than letting one flag quietly win.

### Targets, and a port caveat

`--target T` takes any target in `builders.json`, plus one that is not in it:
`kl`, the shaken KL run directly on shen-go's bare KLambda VM (`cmd/kl` in
the sibling checkout). It is the most direct reading of the question — the
claim is about the KL the shake writes, and this executes that KL verbatim,
with no backend in between — and it is the fallback that keeps the check
runnable when a stage-2 builder is not.

Which is not hypothetical. On shen-go master at the time of writing,
`cmd/yggdrasil-build` panics in `shen.change-pointer-value` while booting its
own `kernel/klambda/declarations.kl`, before it has seen a shaken artifact at
all — a regression in shen-go commit `5edf47e` ("Native kernel hot paths"),
reproducible from a bare `kl.Eval` loop over `kernelLoadOrder` with no
Yggdrasil involvement. The numbers above were taken against the prior commit
`24b2c00` (`YGGDRASIL_SHEN_GO_DIR` pointed at a checkout of it), which builds
and runs the shaken `fib` correctly. `trace_test.go` therefore establishes
the `go` target's usability by building an untraced fixture first and drops
it from the run with a log line if that fails, so a broken sibling builder
cannot read as a tracing regression — while a builder that works is checked,
and a tracing regression on it still fails.

Buffered output was the other thing to watch for: a trace stream that is
opened and never closed can lose its tail. It does not on either runtime —
`open`/`write-byte` reaches disk, and the record counts above come from runs
whose stdout is correct — so the fallback of buffering names in a global and
flushing them from the last user toplevel form is not needed and is not
implemented. A port that did lose the tail would show up as a `called` set
that is a strict prefix of the run.

### Against stage 4

`readglobal.facts` is written as one symbol per line, TSV, in the facts dir
beside the rest of the dump — the shape stage 4's `liveGlobal` wants. Stage 4
decides liveness syntactically (`readsIn`, `reads`, `portReads`, and any
occurrence in the user KL); `readGlobal` is the *empirical* counterpart. A
global some run reads is live whatever the syntax concluded, so intersecting
`readglobal.facts` with `deadInit` answers "did this run read anything the
pruner was about to drop?" — a check on stage 4 rather than on stage 1.
`trace-check` does not perform that intersection today: it shakes untraced
facts with pruning off, so its `initwrite` is the unpruned `writes` and
`uncoveredRead` cannot see a pruned global. Doing it needs one more
comparison, not new facts, which is why the relation is written in stage 4's
own shape.

Stage 4's `writes` is also where `initwrite` comes from — `initwrite(V) :-
writes(_, V)` in `analysis.dl`, derived rather than dumped, so the two
cannot drift apart.

## Staging

1. **Rules on paper, oracle first.** — **done.**
   `analysis/analysis.dl` is the rule set as Soufflé Datalog; `yggdrasil
   facts PROG DIR` (`yggdrasil.facts` in `yggdrasil.shen`, a sibling of
   `yggdrasil.shake` that reuses its pipeline and writes no artifact) dumps
   twenty-one TSV relations; `analysis/refeval.py` is a stdlib-Python
   semi-naive evaluator of the same rules for developers without Soufflé,
   and `analysis_test.go` runs whichever is available — both, when both
   are — over every fixture in `tests/`, in whichever mode it lands in.
   `.github/workflows/analysis-oracle.yml` does the same with real Soufflé
   in CI and diffs the two engines against each other. On all fourteen
   fixtures, eleven eval-free and three eval-capable, `reach` equals
   `kernel.kl`'s defun set exactly (48–66 defuns eval-free, 548–567
   eval-capable), `needsEval` and `reaches` equal the manifest's
   `needs-eval=` and `reaches=`, and `kernel.kl` is byte-identical to
   what the pre-change binary wrote. The rules as written above needed
   four corrections to describe the shake that exists rather than the one
   this note imagined — `called-fns` is position-insensitive and follows
   `cons` arguments; its four data-table exceptions drop their symbols in
   *both* modes, not only eval-free; `strip-f-error-row` empties
   `shen.f-error`'s whole row, argument edges included; and `prepare-tops`
   and `strip-user-declares` make the toplevel forms and the user symbol
   set themselves mode-dependent, so the dump carries both readings and
   the rules choose. Each is recorded as a numbered deviation (D1–D7) in
   the header of `analysis.dl`; stage 3 has to reconcile them the other
   way, by changing the code.
2. **Init-order check in Shen.** *Done.* `ygg.init-order-check` in
   `yggdrasil.shen` builds the EDB from the final form sequence — the kept
   toplevel forms as `trim-top` leaves them, then the user files' toplevel
   forms in manifest order — reading `(value V)` at any depth except inside
   a `defun`, `lambda` or `freeze` (those bodies do not run while the
   artifact boots), the writes twice (`writes`, the same exclusions, and
   `formwrite`, which does descend into `lambda` and `freeze`), plus the
   per-defun writes, the call edges the transitive half needs and the form
   order as `succ`, and then runs the rules above in `ygg.dl`. A read with
   no write anywhere up to and including its own form — directly, or through
   a function the form applies — and no port global to explain it, prints
   `yggdrasil-shake: FAIL init-order form=N reads=V` and aborts before
   `kernel.kl` is written; the Go driver surfaces that line. A clean run
   records `init-order=checked` in both manifests, after `needs-eval`, or
   `init-order=checked-weak` when some read was discharged only by one of
   the three over-approximations (`weakRead` above). Fixtures:
   `tests/init-order-bad.shen` (refused), `tests/init-order-ok.shen` (same
   reads, boot order), `tests/init-order-setter.shen` (a global written by a
   toplevel call: `checked-weak`), `tests/init-order-sameform.shen` (a form
   that writes and reads the same global, and a write inside a `freeze` the
   same form thaws: `checked-weak`) and `tests/init-order-letvar.shen` (a
   `(value X)` on a let-bound variable, which is a computed name and not a
   read of a global); `initorder_test.go` is the host-gated test, and
   `TestAnalysisOracleMatchesShake` diffs `readBeforeWrite` and `weakRead`
   against the other engines like any other relation. Measured: `kernel.kl`
   byte-identical on every existing fixture, and the shake's user CPU within
   noise of the pre-rules check (`tests/metaeval.shen` 1.02–1.07s before,
   0.91–0.94s after; `tests/partial-eval.shen` 0.94–1.04 → 0.96–1.09).
3. **Rules as the implementation.** — **done.**
   `ygg.dl` in `yggdrasil.shen` is a bottom-up Datalog engine in about 150
   lines; `(value *shake-rules*)` beside it is `analysis/analysis.dl` as
   Shen data, clause for clause and deviation for deviation, so the two can
   be read side by side. `yggdrasil.shake` and `yggdrasil.facts` now get
   their footprint from it: the facts are the ones `ygg.cls-defuns`,
   `ygg.mention-rows` and `function-calls` already extract for the oracle,
   and the mode-dependent relations are handed over in both readings (D4,
   D5) so that the *rules* choose the mode, not the caller.

   Engine design. Facts are tuples `[Pred Arg ...]`; a rule is `[Head |
   Body]` with `[not Lit]` and `[ne A B]` literals; a program is a list of
   strata evaluated in order, and `ygg.dl-check-neg` refuses a `not` on a
   predicate its own stratum derives, so stratified negation is checked
   rather than assumed. Two things make it cheap enough to run inside a
   shake. The database is **indexed**: every tuple is filed under a
   property of the interned key `ygg.dl/Pred/Arg1` — the same `put`/`get`
   trick `called-fns` uses for `defp` — so a goal with its first argument
   bound, which is every step of the `reach` recursion, is one hash lookup
   rather than a scan. Evaluation is **semi-naive**: after round 0 a rule
   is re-fired only with one body literal drawn from the previous round's
   new tuples. The naive unindexed prototype took 6.5 s on the real graph;
   measured on shen-go over all sixteen fixtures, the EDB is 5.9k tuples
   and the fixpoint is **0.12–0.15 s eval-free (12 fixtures) and
   0.22–0.25 s eval-capable (4)**, with 0.21–0.24 s to extract the facts
   and 0.002–0.01 s (eval-free) or 0.07 s (eval-capable) to order the
   result — about 0.35 s eval-free and 0.52 s eval-capable end to end,
   inside a ~4 s shake.

   One thing the rules do not give, and cannot: an order. Datalog derives a
   set, but `lambdatable-entries` walks the footprint *list* to build the
   `(set shen.*lambdatable* ...)` literal, so the order is part of
   `kernel.kl`'s bytes. `ygg.dl-walk-order` renders the rule-derived set as
   a list with the same depth-first walk the worklist always did, following
   only edges the rules derived and emitting only nodes the rules put in
   `reach`; `ygg.dl-covered?` checks the converse. Membership is the rules';
   ordering is presentation. That is recorded as deviation D8 in
   `analysis.dl`, and it is the only place the Shen rules needed help from
   something that is not a rule. `strip-f-error-row` is no longer on the
   shake's path at all — the mode guard in the `edge` clauses (D3) does its
   job — and it, the worklist `reach` and the Warshall closure stay as
   differential oracles. `(yggdrasil.footprints ["prog.shen"])` is the
   test-only entry point that computes the footprint all three ways and
   prints `agree=`; the Warshall leg runs over the graph restricted to the
   footprint (a set closed under edges has the same closure) and is skipped
   above 150 nodes, because O(V^3) over all 686 is minutes.

   Byte-identity, verified the way stages 1 and 2 were: the pre-change
   binary built from 48a5170 and the new one shaken over every fixture in
   `tests/`. `kernel.kl` is **byte-identical on all fifteen fixtures that
   shake** (init-order-bad is refused on purpose), both manifests differ by
   exactly the new `computed-names=` line, and user `.kl` differs only in
   gensym numbering. The Soufflé and `refeval.py` oracles still agree with
   `reach`.

   **Computed names.** The soundness argument for the whole shake is that a
   name the artifact can call occurs syntactically in the artifact.
   `intern` turns a string into a callable symbol, and a `(value X)` /
   `(set X _)` whose `X` is not a literal symbol names a global only at
   runtime; either is outside that argument. The facts `userintern(F)` and
   `userglobal(F)` (F the user defun, or `top` for a file's toplevel forms)
   and the rule `computedName(F)` are new in both the Shen rule set and
   `analysis.dl`, and the oracle test compares the relation between engines
   and against the manifest. It decides **nothing** this stage: it is not an
   eval entry point, interning a name you never apply being perfectly safe.
   The shake prints `yggdrasil-shake: WARN computed-name in F` and records
   `computed-names=none` or `computed-names=F,G` in both manifests after
   `init-order`. Measured when this landed: of the sixteen fixtures then in
   `tests/`, **only `tests/computed-name.shen` triggered it**
   (`computed-names=computed-call`); every other fixture, the eval-capable
   ones included, was `none` — which is the result worth having, since it
   says the hypothesis is not vacuous and not routinely violated. The
   stage-2 fixture `tests/init-order-letvar.shen`, added later, is the
   second: its `(let X *a* (print (value X)))` is a `(value X)` on a
   variable, so it records `computed-names=top`, which is exactly the
   classification that keeps it out of the init-order check. `footprint_test.go` is the host-gated test.
4. **Dead initialisation.** — **done.**
   `readsIn`, `reads`, `writes` and `portReads` join the fact dump (twenty-one
   relations now); `liveGlobal`/`deadInit` join `analysis.dl`,
   `analysis/refeval.py` and — as `(value ygg.*init-rules*)` beside
   `*shake-rules*` — the Shen engine. `yggdrasil shake|build|run
   ... --prune-init` drops every dead form from the synthesised initialiser.
   Off by default.

   **What is prunable.** The rules answer which form indices write a global
   nothing can read. The shake then drops a form only when it is *exactly*
   `(set V Lit)` with `Lit` an atom — a number, a string, a boolean, a symbol
   or `()`. A form whose value is a call is never pruned, however dead its
   global is: evaluating `(set *property-vector* (vector 20000))` or `(set
   shen.*special* (cons @p ...))` is an effect in its own right, and dropping
   it would change what the artifact *does*, not just what it remembers. Nor
   is a form that is not a `set` at all, or one that sets more than one
   global. That is `ygg.prunable?`; on fib it is the difference between 29
   dead globals and 28 dropped forms (`shen.*special*`'s value is a cons
   chain). The initialiser that comes out is therefore always a
   *subsequence* of the one that went in — same forms, same order, fewer of
   them — which is what `prune_test.go` asserts.

   **`port_reads` provenance.** `portReads` is a fact about a backend, so it
   lives next to the backend: a `"port_reads"` array on each target in
   `builders.json`, with `"port_reads_verified"` saying whether it was read
   off that port's runtime. Today exactly one target is verified:

   - **`go`** (`port_reads_verified: true`), five entries, each read out of
     shen-go's `kl/` package: `*stinput*` (`PrimReadByte`'s EOF sentinel in
     `kl/primitives.go`), `*stoutput*` (the port global bound in the same
     file), `*home-directory*` (`ResolveHomePath`, which `open`, `load-file`
     and the native `read-file` all resolve through), `*property-vector*`
     (`kernelArity` in `kl/kernelfast.go`, behind the native `arity` and
     `fn`), and `shen.*lambdatable*` (`nativeFn`, same file). The generated
     `main.go` (`cmd/yggdrasil-build`) reads no global of its own. The lambda
     table and the arity table are therefore live on `go` because its port
     reads them, not because a rule says tables are special; the
     external-symbols `put` is not a `set` and so is never prunable anyway.
   - **every other target** (`port_reads_verified: false`) carries a
     conservative superset: `go`'s five plus every global the kernel's own
     defuns read in the full boot (`readsIn` over the unshaken kernel,
     intersected with what the initialiser writes) — 35 entries. Against that
     list only `shen.*call*` and `shen.*system*` are ever dead, so pruning is
     nearly a no-op until someone reads those runtimes. A `shake` with no
     `--target` uses the union of every list, which is that same superset.

   **Measured**, `--prune-init --target go`, over all sixteen fixtures that
   shake (init-order-bad is refused on purpose). Of the 35 forms the
   initialiser writes, the eval-free fixtures drop 28 (`computed-name`,
   `fib`, `hello`, `init-order-ok`, `joy-sum`, `parity`, `partial`,
   `stdin-sum`, `typed-ok`), 29 (`interpreter`, `typed-bad`,
   `typed-unsigned`) or 26 (`prolog`) — `kernel.kl` 13.5 kB → 12.7 kB, about
   6%. The eval-capable ones drop 10 (`metaeval`, `partial-eval`) or 9
   (`tc-interp`): 253 kB → 252 kB, 0.1%. An eval-capable program reaches
   nearly the whole kernel, so nearly every global has a live reader, which
   is the answer the rules should give. That is the shape the note predicted:
   stage 4 shrinks artifacts, and by little.

   **Byte-identity**, verified as stages 1–3 were, with the pre-change binary
   built from 7644a0a: with the flag off, `kernel.kl` is byte-identical on all
   sixteen fixtures that shake, both manifests differ by exactly the new
   `pruned-init=0` line, and user `.kl` differs only in gensym numbering.

   **Parity.** `yggdrasil parity PROG DIR --target go --prune-init --expect
   tests/PROG.expected` is the gate for this, and it takes `--prune-init`
   so the slice it builds is the pruned one. It has not yet returned a
   verdict: on the machine this stage was written on the `go` stage-2
   builder does not boot at all (`shen-go/cmd/yggdrasil-build` panics in
   `shen.change-pointer-value` loading its own `kernel/klambda/
   declarations.kl`), identically for the pruned slice, the unpruned slice
   and the pre-stage-4 binary — so every one of the six fixtures with a
   golden reports `build failed`, and none of it is about pruning. `go` stays
   off by default until that gate is green somewhere it can run; the flag and
   the verified `port_reads` are what make running it possible.

   **Why it is not on by default.** The parity gate decides that per target,
   and it cannot decide it from a design note. A `port_reads` list that is
   missing an entry is a silent miscompile — the artifact boots with a global
   unbound and fails only when something reaches it — so `port_reads_verified`
   is the gate's precondition, and only `go` has it.
5. **SCIP level-2 oracle.** — **done.**
   `yggdrasil scip-check PROG OUTDIR --target go` builds the program twice
   with the *same* stage-2 builder — `A*`, the shaken slice, and `A`, the
   full program `K + user`, the latter from the new `--no-shake` mode
   (`yggdrasil.shake-full` in `yggdrasil.shen`: every kernel defun, the
   eval-capable initialiser, no `trim-top`, no `rewrite-f-error`, manifest
   `shaken=false`) — indexes each module with `scip-go`, decodes the two
   `index.scip` files, and compares the set of function symbols reachable
   from `main` plus a normalised body hash per function. `scip.go` is the
   whole implementation; `scip_test.go` is the host-gated fixture test and
   the decoder unit test.

   The SCIP index is decoded in-repo, by hand, from the protobuf wire
   format (`Index.documents`, `Document.relative_path`/`occurrences`,
   `Occurrence.range`/`symbol`/`symbol_roles`/`enclosing_range`). The `scip`
   CLI cannot be installed — its `go.mod` carries `replace` directives, so
   `go install github.com/scip-code/scip/cmd/scip@v0.7.1` is refused — and
   the Go bindings are a module dependency, which this repo does not have
   and should not acquire for an optional check. An edge is what the index
   itself says: a reference occurrence lying inside a definition's
   `enclosing_range` is a reference *by* that definition. A `go/ast`
   fallback computes the same graph without an indexer; the subcommand
   prints `path=scip` or `path=go-ast` so the verdict always says which ran.
   Both paths ran here and agreed.

   **Running it.** The `go` target needs a sibling shen-go whose
   `cmd/yggdrasil-build` can boot its own kernel. shen-go master at
   `5edf47e` ("Native kernel hot paths") cannot: it panics in
   `shen.change-pointer-value` part way through `declarations.kl`, because
   the interpreted `put` probes its bucket with `<-vector` in tail position
   inside `trap-error` and the interpreter's `trap-error` does not cover a
   tail call, so the "vector element not found" error escapes as a value
   into the property vector. `cmd/shen` never sees it (it installs the
   native `put` via `InstallKernelFast`); the builder does not install them,
   so it dies. Point `$YGGDRASIL_SHEN_GO_DIR` at a checkout that works
   (`24b2c00` is the last one before the regression). Without one,
   `scip-check` reports the stage-2 failure and the host-gated test skips
   rather than failing — a broken sibling is not a broken shake.

   **What the numbers actually are, and why the interesting one is not the
   SCIP one.** Stage 5 was written expecting shen-go's output to be
   compositional at the Go level: one Go function per KL defun, one direct
   Go call per KL call. It is not. `shen-go/codegen` emits one 0-arity
   module thunk per *chunk* —
   `var KernelChunk0 = MakeNative(func(__e *ControlFlow) { ... })` — inside
   which every defun is an anonymous closure bound at run time
   (`Call(__e, ns2_1set, symF, MakeNative(...))`, `ns2_1set` being `defun`)
   and every call is a run-time symbol lookup
   (`Call(__e, PrimFunc(symF), ...)`). The generated module for `fib` has
   **not one** named Go function per KL defun: its 54 kernel defuns are 136
   anonymous closures inside one `var KernelChunk0`, and the only named
   functions in the whole module are the driver's four. So the Go-level
   main-reachable set is 4 nodes for A and 4 for A* on both `fib` and
   `hello` — `main`, `run`, `runHelper` and `fail`, the generated driver —
   of which 3 have byte-identical `go/printer` bodies across the two builds
   and 1, `main` itself, is *required* to differ: the builder generates it from the
   manifest, so it names the chunks and replays the user arities of
   whichever program it built. That comparison is true and worth having —
   it says the builder packaged both programs the same way — but it is not
   a statement about the shake.

   The node graph Stage 5 wanted is one level down, and `scip-check`
   recovers it from the same generated Go with `go/ast`: the defun bindings
   are the nodes, the `PrimFunc` lookups are the edges. Measured on
   shen-go, wall clock 68-85 s per fixture end to end (two shakes, two
   stage-2 builds, two `scip-go` runs):

   | | `fib` | `hello` |
   |---|---|---|
   | Go-level main-reachable, A\* / A | 4 / 4 | 4 / 4 |
   | Go-level identical bodies | 3 | 3 |
   | KL defuns emitted, A\* (kernel + user) | 55 (54+1) | 54 (54+0) |
   | KL nodes reachable from the initialiser, A\* | 53 | 52 |
   | KL defuns emitted, A | 688 | 687 |
   | KL nodes reachable, A | 604 | 603 |
   | shake footprint `reach` | 53 | 53 |
   | A\* nodes missing from A | 0 | 0 |

   **The delta.** It is small and it is entirely explained, which is the
   useful outcome. `kernel.kl` carries 54 defuns for `fib`: the 53 the
   rules put in `reach`, plus the synthesised `shen.initialise`. Of the 55
   defuns the backend emits (those 54 plus the user's own `fib`), the
   backend-level walk reaches 53. The two it does not reach are:

   - `shen.initialise`, which is not a `reach` member and never was --
     nothing inside the program calls it; the builder's generated `main`
     does, from the manifest's `init=` key; and
   - **`do`**, which is the direct-vs-lookup half of the delta in one
     word. It is a kernel defun the shake keeps, and the Shen-to-Go
     compiler lowers it as a special form at every call site, so its name
     is never looked up and no `PrimFunc` edge to it exists.

   Running the other way, the seed set the backend's output yields is 82
   names where the rules' `reach` is 53, because the initialiser mentions
   names that are data -- arity-table pairs and the external-symbols list
   -- rather than calls. Neither direction is a bug; both are the backend
   and the rules disagreeing about what an *edge* is, which is exactly the
   disagreement this stage exists to measure. `hello` is the same story
   with no user defun, so its 52 against a footprint of 53 is `do` alone.

   **What this proves.** For every fixture checked, every node A\* can
   reach, A can reach too, and the bodies the two builds share are
   identical after `go/printer` normalisation. That is compositional
   level-2 *inclusion*: the backend did not invent an edge the rules never
   saw, and it did not compile a kept function differently because its
   neighbours were gone. Disagreement would be a bug in the builder or in
   the rules, the status the Soufflé oracle has for stage 1.

   **What it cannot see.** Exactly what the shake cannot see, and this is
   the point of it being an *independent* oracle for the same
   over-approximation rather than a soundness proof: a runtime `(fn F)`
   lookup or a name computed with `intern` is a string until it is applied,
   so it appears in neither graph. An eval-capable A\* keeps the machinery
   that could resolve such a name, and the check would not notice if the
   backend resolved one differently. It is also only meaningful for a
   builder whose output has a node graph at all: a target that routes every
   call through one dispatch table has nothing to compare, which is why
   `--target` refuses anything but `go` today.

Stages 1 and 2 are the ones that change what Yggdrasil can claim. Stage 3
is what makes the trust argument true in the code rather than in a
document, and it is now true there. Stage 4 is the only one that shrinks
artifacts, and by little — 6% of `kernel.kl` on an eval-free program, 0.1%
on an eval-capable one, which is the measurement that says where the
remaining bytes are and that it is not here. Stage 5 is the only one that
checks stage 2, and what it found first was that the reference backend is
not compositional at the level the note assumed — see its entry.
