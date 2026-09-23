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
29 relations at `3c499a1` (`ls OUTDIR/*.facts | wc -l` after
`yggdrasil facts`). See the declarations at the top of
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
three evaluators run the same rules rather than three dialects of them
(D11) — two of them being transcriptions of the shake and Soufflé the one
independent engine, as `docs/verification-guide.md` section 6 sets out.
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
on fib against the `go` runtime's `portReads` — the only `portReads` list
in the repository that was read off a runtime; every other target's is
`unknown`, and stage 4 refuses to prune against it: 29 of the 35 globals
the initialiser sets are dead, and 28 of the forms that set them are
prunable.
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

The record format is a tag byte, a tab, the name, a tab, a phase byte, a
newline: `f<TAB>NAME<TAB>b` for an entry, `v<TAB>NAME<TAB>p` for a read. The
phase is `b` while `shen.initialise` is running and `p` afterwards — the flip
is the last action of the woven initialiser body — because without it a `fib`
run reads as 49,076 records of which 20,000 are `shen.fillvector` out of the
property vector's initialiser, and "the program called X" is unanswerable.
The run's last record is `e<TAB>end`, written by `ygg.trace-end` from an
extra toplevel form appended after the last form of the last user file; it is
what distinguishes a finished run from a trace cut short (see "The trace has
to be able to say it ended" below). Tag, separators, phase and terminator are
written as bytes, so `kernel.kl` carries no string literal with a control
character in it.

A reader must accept the two-column form as well, with the phase unknown: an
artifact woven by an older shaker is a smaller witness, not a run that called
nothing. `trace.go:parseTrace` counts those in `unphased-records=`.

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
nothing outside its footprint, and that every global it read is one that
*some* kept defun body or toplevel form writes somewhere. That is evidence
for soundness obligation 1, **not a proof**: a run exercises one path, and
a different input can enter a function this one did not.

`uncoveredRead` is weaker than its name suggests, and weaker than "read no
global nothing writes". A read is covered if any kept defun body contains
a `(set V _)` at all -- `defunwrite` is a syntactic relation over bodies,
with no notion of whether that body ran, or ran first. A program that
reads `V` before the only writer of `V` executes is not flagged. That is
deliberate and it is what makes the rule usable: without the `defunwrite`
leg, every counter the kernel maintains at run time (`shen.*call*`,
`shen.*infs*`, `shen.*gensym*`) would be a finding on every run. It is a
good filter for exactly that class -- the kernel's own counters -- and it
is not an initialisation-order check. The ordering question is
`readBeforeWrite`'s, over toplevel forms, where `succ` gives it an order
to reason about.

The converse containment does not hold and is not asserted. `reach ⊋ called`
on every fixture, which is ordinary imprecision (`called-fns` is a
position-insensitive cons walk, D1) plus the fact that one run takes one
path. Shrinking that gap is not what this is for; `docs/why.md` explains why
precision is the wrong thing to optimise on a 686-node graph.

### Measured

Yggdrasil `3c499a1`, shen-go `da55c5d`, stage-1 host shen-cl (an SBCL
build of its refreshed master, on `$YGGDRASIL_HOST`), stage-2 shen-go's
`yggdrasil-build`, 2026-09-22. `yggdrasil trace-check FIXTURE OUT --target
T`. All `OK`; Soufflé and `analysis/refeval.py` agree with the Shen engine
on `uncoveredCall` and `uncoveredRead` over the same fact dirs, both on
these traces and on traces with an out-of-footprint call injected.

| fixture | mode | `reach` | `called` (go) | `called` (kl) | `readGlobal` | records (go) |
|---|---|---|---|---|---|---|
| `fib` | eval-free | 53 | 34 | 32 | 3 | 49,076 |
| `partial` | eval-free | 53 | 34 | 32 | 3 | 27,174 |
| `stdin-sum` | eval-free | 54 | 38 | skipped | 4 | 27,520 |
| `prolog` | eval-free | 66 | 41 | 43 | 5 | 27,620 |

So a go run enters 62–70% of the footprint. The rest is the kernel's boot
machinery on paths this input does not take, plus the over-approximation
the edge rule is built on; it is what the shake keeps because it cannot
prove otherwise, which is the correct trade.

The `kl` target does **not** agree with `go` name for name, and an earlier
version of this table said it did. The differences, measured at the
commits above:

| fixture | in `go` only | in `kl` only |
|---|---|---|
| `fib` | `<-vector`, `vector->` | -- |
| `partial` | `<-vector`, `vector->` | -- |
| `prolog` | -- | `shen.pvar?`, `thaw` |

All four names are entries in `go`'s `native_overrides`, and the trace is
a property of the **runtime**, not only of the KL: a name whose binding is
native when the program reaches it records no KL entry. The two targets --
one builder's compiled output, one builder's interpreter, both out of the
same shen-go checkout -- install natives differently. shen-go's generated
`main` calls `shen.initialise` *before* `InstallKernelFast`, so a `go` artifact records
the boot's KL bodies -- which is where `<-vector` and `vector->` come from
-- and misses any override entered afterwards. `thaw` and `shen.pvar?`
are absent from `prolog`'s `go` trace and present in its `kl` one, which
on that reading means the `go` run reached them only after the natives
were installed; the trace cannot show an entry that did not happen, so
this is the natural explanation rather than a measurement. `cmd/kl` never
calls `InstallKernelFast` at all; what it lowers instead is a handful of
forms
in its own compiler (`kl/compiler.go` special-cases `<-vector` and `thaw`),
and that is a different set.

Both readings are contained, which is the check doing its job across a
real runtime difference rather than in spite of one. What the disagreement
costs is the stronger claim the old text made -- that the compiled backend
introduced no call the interpreter did not -- which this table cannot
support in either direction.

`stdin-sum` on `kl` is a named **skip**, not a pass, for the reason in
"The trace has to be able to say it ended" below.

The three globals of `fib` are `*hush*`, `*property-vector*` and
`*stoutput*` — the first two written by the initialiser, the third a
`portGlobal`. `stdin-sum` adds `*stinput*`; `prolog` adds `shen.*infs*`
and `shen.*prolog-memory*`.

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

`--target T` takes any target in `builders.json`. `kl` is one of them.

**`kl` is a target now** (hickey-13). It used to be the exception: a
runner special-cased inside `trace.go` — its own `go build ./cmd/kl` in
the sibling checkout, its own driver file, its own stdin convention, and
the `go` entry's `dir_env` borrowed at run time to find that checkout —
reachable from `trace-check` and from nothing else. It had no `port_reads`,
no row in `yggdrasil contract`, and no place in `yggdrasil targets` or the
parity gate, while the docs counted it as one of "two runtimes". A target
that is not a target is a thing every consumer has to know about
separately, and each of them knew about it differently.

It is an entry in `builders.json` like the rest: `yggdrasil targets` lists
it, `build`, `run`, `parity`, `trace-check` and `contract` all reach it
through the one data path, and `go build ./cmd/kl` plus the feed file are
its two `build` steps. What is peculiar about it is **declared on the
entry** rather than matched on its name in Go:

| fact | value | what reads it |
| --- | --- | --- |
| `program_file` | `{outdir}/_klvm_feed.kl` | the run feeds this file to stdin before anything else (`programFileFor`, `openRunStdin`) |
| `stdin` | `appended-to-program` | `trace-check` refuses a fixture stdin by name; the parity gate skips such a target when `--stdin` is given |
| `stdout` | `repl-transcript` | the golden is checked by **containment** in the transcript, on `trace-check`'s stdout leg and on every leg of the parity gate |

The feed file is written by a named build helper, `{yggdrasil} program-file
OUTDIR FILE`, because concatenating the kernel, `(shen.initialise)` and the
user files **in manifest order** is logic and not a command line; the step
runs in process and is also reachable as `yggdrasil program-file` for
reproducing it by hand. `TestNoTargetNameIsSpecialCasedInGo` (`trace_test.go`)
fails if the string `"kl"` reappears in a non-test Go source.

What `kl` is good for is unchanged, and is why it was worth keeping: the
claim is about the KL the shake writes, and `cmd/kl` executes that KL
verbatim with no backend in between, so it is the fallback that keeps the
check runnable when a stage-2 builder is not. It is still one builder's
compiled output and one builder's interpreter from the same repository —
"two runtimes" is two ways of running one port's code, not two ports — and
the table above is where the difference is now written down instead of
being implied.

**The fallback is not hypothetical.** shen-go commit `5edf47e` ("Native kernel hot
paths") made `cmd/yggdrasil-build` panic in `shen.change-pointer-value`
while booting its own `kernel/klambda/declarations.kl`, before it had seen
a shaken artifact at all — reproducible from a bare `kl.Eval` loop over
`kernelLoadOrder` with no Yggdrasil involvement. For a while the only
usable commit was the prior `24b2c00`, and the first version of the table
above was taken there. It is fixed: the numbers above are re-taken at
`da55c5d`, where `cmd/yggdrasil-build` boots and builds. CI does not use
either — `.github/shen-go.ref` pins its own SHA for all three workflows
that need a host, and that pin is a separate decision from whatever a
developer's sibling checkout happens to be. `trace_test.go` therefore establishes
the `go` target's usability by building an untraced fixture first and drops
it from the run with a log line if that fails, so a broken sibling builder
cannot read as a tracing regression — while a builder that works is checked,
and a tracing regression on it still fails.

Buffered output was the other thing to watch for: a trace stream that is
opened and never closed can lose its tail. It does not on either runtime —
`open`/`write-byte` reaches disk — but "it does not on the two runtimes
measured" is not a property of the format, and a `called` set that is a
strict prefix of the run is not self-identifying, so the fallback is now
implemented and unconditional: the weaver appends one extra toplevel form,
`(ygg.trace-end)`, after the last form of the last user file, and it writes
the `e<TAB>end` record and then `close`s the stream. `close` was already in
the manifest's primitive list, so no capability is added. Obligation F of
[port-contract.md](port-contract.md) is discharged by this for every port,
not only for ports that declare a need for it.

### The trace has to be able to say it ended

Nothing above detects loss on its own, which is the point: a relation read
out of a file cannot tell you what is missing from it. Three guards, and they
are deliberately at different layers:

1. **The end-of-run record.** `trace-check` refuses a trace with no
   `e<TAB>end` in it —
   `yggdrasil-trace-check: FAIL truncated=no-end-record`. This covers the
   artifact side: a crash, an early exit, a port that lost the buffered tail.
2. **The run's stdout against the fixture's committed golden**,
   `tests/<name>.expected`, compared in the same invocation against the same
   artifact (and with the same `canon()` the parity gate uses, so one golden
   cannot mean two things). The end record says the last form ran; the golden
   says it ran correctly. A trace of a run that produced the wrong answer is
   not evidence for anything.
3. **The facts' declared row counts.** The two guards above live in the Go
   driver and cannot see a `called.facts` edited afterwards, which is the
   half the original repro exercised: `head -1 called.facts` and the
   host half alone once reported `OK called=1 reach=53`. So the writer
   declares, in `FactsDir/trace.meta`, how many rows it wrote and whether the
   trace it read had ended; `yggdrasil.trace-check` compares and reports
   `counts=verified` / `counts=unverified` on the OK line, and fails with
   `truncated=called-count read=N declared=M` when they differ. Because
   `called.facts` is written sorted, the older guard — "`shen.initialise` must
   be in the relation" — catches only a cut short enough to drop that one
   name (line 21 of 34 for `fib`); the row count catches any prefix. Both
   detect **loss**, not forgery: a hand that rewrites `trace.meta` too is
   writing a fiction, and no check confined to one directory can say
   otherwise. A directory with no `trace.meta` is reported `counts=unverified`
   rather than refused, because nobody declared a count there.

There is a fourth case where no evidence is obtainable at all, and it is a
named **skip**, never a pass: shen-go's `cmd/kl` reads its program from
stdin and takes no file argument, so the run appends a fixture's `.stdin`
bytes after the KL forms, the VM eats them as further toplevel forms, and
the program reads EOF. That is exactly what `kl`'s entry declares as
`stdin: appended-to-program`, and `evidencePossible` reads the **fact**
rather than the target's name: `trace-check` on a fixture with stdin, for
any target declaring that value, prints
`yggdrasil-trace-check: SKIP stdin-appended-to-program` and exits 3. A
second runtime with the same property is refused the day its entry says so,
with no edit to `trace.go`; deleting the fact deletes the skip.

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
   29 TSV relations at `3c499a1`; `analysis/refeval.py` is a stdlib-Python
   semi-naive evaluator of the same rules for developers without Soufflé,
   and `analysis_test.go` runs whichever is available — both, when both
   are — over every fixture in `tests/`, in whichever mode it lands in.
   `.github/workflows/analysis-oracle.yml` does the same with real Soufflé
   in CI and diffs the two engines against each other. Re-taken at
   Yggdrasil `3c499a1`, shen-go `da55c5d`, host shen-cl, 2026-09-22: of
   the **20** files `ls tests/*.shen` lists, **19 shake** (only
   `init-order-bad` is refused, on purpose), **16 eval-free and 3
   eval-capable**, and on every one of the 19 `reach` equals `kernel.kl`'s
   defun set exactly (48–66 defuns eval-free, 548–567 eval-capable),
   `needsEval` and `reaches` equal the manifest's `needs-eval=` and
   `reaches=`. The counts here and everywhere below are derived from the
   tree, by shaking each fixture and reading `needs-eval=` out of its
   manifest, not maintained by hand; earlier versions of this note said
   "fourteen", "fifteen" and "sixteen" in three places at once. Byte
   identity against a pre-change binary is a per-stage, by-hand
   measurement, recorded at each stage below with the commit it was taken
   against; nothing re-runs it. The rules as written above needed
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
   measured on shen-go over the sixteen fixtures then in `tests/`, the EDB
   is 5.9k tuples and the fixpoint is **0.12–0.15 s eval-free and
   0.22–0.25 s eval-capable**, with 0.21–0.24 s to extract the facts and
   0.002–0.01 s (eval-free) or 0.07 s (eval-capable) to order the result
   — about 0.35 s eval-free and 0.52 s eval-capable end to end. Those are
   a snapshot taken when this stage landed, on a host and a fixture set
   that have both moved since; nothing re-takes them.

   What a shake costs **today** -- Yggdrasil `3c499a1`, host shen-cl
   (an SBCL build of its refreshed master), sibling shen-go `da55c5d`
   (present but unused: a bare `shake` runs stage 1 only), 2026-09-22,
   user CPU of `yggdrasil shake FIXTURE OUT` including the
   host it spawns, one run each, no warm-up:

   | fixture | mode | user CPU | kernel.kl defuns |
   |---|---|---|---|
   | `fib` | eval-free | 0.88 s | 54 |
   | `hello` | eval-free | 1.05 s | 54 |
   | `prolog` | eval-free | 1.21 s | 67 |
   | `stdin-sum` | eval-free | 1.20 s | 55 |
   | `interpreter` | eval-free | 1.37 s | 67 |
   | `metaeval` | eval-capable | 1.41 s | 551 |
   | `partial-eval` | eval-capable | 1.40 s | 549 |
   | `tc-interp` | eval-capable | 1.34 s | 568 |

   Across all 19 fixtures that shake the range is 0.82 s to 1.41 s. The
   defun counts include the synthesised `shen.initialise`, which is one
   more than `reach`. This note has never carried a before/after CPU
   comparison for the rules work that could be re-taken here -- the
   pre-rules binary is several stages back -- so none is quoted.

   One thing the rules do not give, and cannot: an order. Datalog derives a
   set, but `lambdatable-entries` walks the footprint *list* to build the
   `(set shen.*lambdatable* ...)` literal, so the order is part of
   `kernel.kl`'s bytes. It is **defined**, not walked: `ygg.rule-footprint`
   renders the derived set as kernel load order — `(map (fn row-head)
   Graph)`, the order `graph-rows` read the kernel in — restricted to
   `reach`, followed by the seeds the rules do not reach, deduplicated.
   That remainder is exactly the non-kernel seeds (primitives, user
   function names, data symbols), which D6 keeps out of `reach` and which
   `keep-set`/`trim-arity-pairs`/`eta-if-fn` still read. Membership is the
   rules'; the order is a value.

   Stage 3 originally kept the *old* order instead, by re-running the
   pre-rules depth-first walk over the raw graph rows and emitting only
   nodes the rules had put in `reach`, with `ygg.dl-covered?` erroring if
   the rules derived anything the list lacked. That was a second
   reachability implementation over a second graph from a second seed
   computation, alive only so `kernel.kl`'s bytes would not move, and it is
   deleted: `ygg.dl-walk-order`, `ygg.dl-covered?`, `ygg.dl-succs` and
   `ygg.dl-edge?` no longer exist. The bytes moved once, at the commit that
   deleted them ("D8: one defined footprint ordering, the old depth-first
   walk deleted"): one line of `kernel.kl` on each eval-free fixture, the
   synthesised `shen.initialise`, whose lambda-table literal is a
   permutation of the same entries; the eval-capable fixtures build that
   table at boot and are byte-identical. The defun set, both manifests and
   every `tests/*.expected` are unchanged, and `fn` reads the table with
   `assoc`, so the order was never semantically meaningful. Byte identity
   *across ports* is the guarantee and is untouched; byte identity across
   versions of Yggdrasil was a self-imposed constraint, given up there,
   once, deliberately. `footprint_test.go`'s
   `TestFootprintOrderIsKernelLoadOrder` now checks the definition — the
   footprint restricted to kernel defuns is a subsequence of kernel load
   order — on every fixture that shakes; the old walk's order fails it, so
   it is a regression test, not a golden. That is recorded as deviation D8
   in `analysis.dl`. `strip-f-error-row` is no longer on the
   shake's path at all — the mode guard in the `edge` clauses (D3) does its
   job — and it, the worklist `reach` and the Warshall closure stay as
   differential oracles. `(yggdrasil.footprints ["prog.shen"])` is the
   test-only entry point that computes the footprint all three ways and
   prints `agree=`; the Warshall leg runs over the graph restricted to the
   footprint (a set closed under edges has the same closure) and is skipped
   above 150 nodes, because O(V^3) over all 686 is minutes.

   Byte-identity, verified the way stages 1 and 2 were: the pre-change
   binary built from 48a5170 and the new one shaken over every fixture in
   `tests/`. `kernel.kl` was **byte-identical on all fifteen fixtures that
   shake** (init-order-bad is refused on purpose), both manifests differ by
   exactly the new `computed-names=` line, and user `.kl` differs only in
   gensym numbering. The Soufflé and `refeval.py` oracles still agree with
   `reach`. The later D8 ordering change above is the one place that
   equality was given up: from that commit on, the eval-free fixtures'
   `kernel.kl` differs from a pre-D8 shake's in exactly the one
   `shen.initialise` line, manifests and defun sets unchanged.

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
   classification that keeps it out of the init-order check.
   `footprint_test.go` is the host-gated test. Both counts are as of the
   commit that landed each; at `3c499a1` the fixture set is 20, and
   `computed-name` and `init-order-letvar` are still the only two.
4. **Dead initialisation.** — **done.**
   `readsIn`, `reads`, `writes` and `portReads` join the fact dump (21
   relations at the time; 29 at `3c499a1`); `liveGlobal`/`deadInit` join
   `analysis.dl`,
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
   lives next to the backend: a `"port_reads"` array in `builders.json`,
   with `"port_reads_source"` naming the runtime lines it was read off and
   `"port_reads_checked_by"` naming the test that fails when it drifts (or
   `none`). A list whose `_checked_by` is `none` is *declared*, never
   verified — see docs/port-contract.md for why the old
   `port_reads_verified` boolean had to go. Today exactly one target
   declares a list of its own:

   - **`go`**, five entries, each read out of
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
   - **every other target** declares no `port_reads` at all and inherits
     the `_default` block, whose value is the literal string `unknown`
     (hickey-14). `--prune-init --target T` on such a target is refused by
     name; a `shake` with no `--target` prunes against the union over the
     targets that *have* declared a list — today `go`'s five — and the
     `WARN` names both the targets the union is over and the targets it
     therefore says nothing about.

     `_default` used to hold a conservative superset instead: `go`'s five
     plus every global the kernel's own defuns read in the full boot
     (`readsIn` over the unshaken kernel, intersected with what the
     initialiser writes), 35 entries, `port_reads_checked_by: none`.
     Against that list only `shen.*call*` and `shen.*system*` were ever
     dead, so pruning was nearly a no-op on any target but `go` — and, more
     to the point, every unmeasured target resolved to a list of globals
     indistinguishable downstream from a measured one. The superset had
     already been collapsed from twelve pasted copies to one; collapsing it
     further, to the word `unknown`, is the same repair carried to its
     end. `yggdrasil.shen` keeps its own copy of the union of the declared
     lists, for a direct host invocation with no Go driver to push a list
     in, and `TestPortReadsDefaultMatchesShen` fails if the two diverge —
     it is no longer a conservative superset for an unmeasured port, and
     its comment in `yggdrasil.shen` says so.

   **Measured** when this stage landed, `--prune-init --target go`, over
   the sixteen fixtures then in `tests/` that shake (init-order-bad is
   refused on purpose); 19 shake today, and these proportions have not
   been re-taken. Of the 35 forms the
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
   built from 7644a0a: with the flag off, `kernel.kl` was byte-identical on
   the sixteen fixtures that shook at that commit, both manifests differed
   by exactly the new `pruned-init=0` line, and user `.kl` differed only in
   gensym numbering. A by-hand measurement at that commit; nothing re-runs
   it, and the manifests have since gained a version bump and further keys.

   **Parity.** `yggdrasil parity PROG DIR --target go --prune-init --expect
   tests/PROG.expected` is the gate for this, and it takes `--prune-init`
   so the slice it builds is the pruned one. It has still not been run to
   a verdict over the fixture set. When this stage was written the `go`
   stage-2 builder did not boot at all on the machine it was written on
   (`shen-go/cmd/yggdrasil-build` panicked in `shen.change-pointer-value`
   loading its own `kernel/klambda/declarations.kl`), identically for the
   pruned slice, the unpruned slice and the pre-stage-4 binary, so every
   fixture with a golden reported `build failed` for a reason that was not
   about pruning. That obstacle is gone -- at shen-go `da55c5d` the
   builder boots and `trace-check --target go` completes on every fixture
   tried -- but nobody has since run the pruned parity sweep, so `go`
   keeps `--prune-init` off by default. The single runtime data point that
   does exist is `TestPruneInitGoArtifactStillRuns`, described below.

   **Why it is not on by default, and what actually refuses it.** A
   `port_reads` list that is missing an entry is a silent miscompile — the
   artifact boots with a global unbound and fails only when something
   reaches it.

   The CLI no longer leaves that to a gate. `wrapShakeExpr` in `prune.go`
   **refuses** `--prune-init --target T` outright when `T`'s `port_reads`
   is not backed by a named test (`portReadsVerified`), naming the three
   ways out: shake without `--target` (the union over the targets that
   have DECLARED a list, which the `WARN` names, along with the targets it
   therefore says nothing about), drop the flag, or pass
   `--prune-init-unverified` to prune against that union on the named
   target anyway and take a warning on stderr. Only `go` passes today;
   every other target inherits `_default`, whose `port_reads` is the
   literal string `unknown` and whose `port_reads_checked_by` is `none`,
   so the refusal for those says that nobody has measured that runtime
   rather than that its list is unchecked -- different claims, and
   different repairs. `TestPruneInitRefusesUnverifiedTarget` and
   `TestTargetAgnosticPruneNamesWhatTheUnionIsOver` are what fail if that
   stops being true.

   What is *not* checked is the pruned artifact itself. `trace-check`
   shakes with pruning **off**, so its `initwrite` is the unpruned
   `writes` and `uncoveredRead` cannot see a global the pruner dropped;
   the only runtime evidence for stage 4 anywhere in the repository is
   `TestPruneInitGoArtifactStillRuns`, which builds the pruned `fib` slice
   on `go` and compares one stdout against `tests/fib.expected`. One
   fixture, one target, one input. The intersection that would close this
   — `trace-check --prune-init`, then `readglobal.facts` against
   `deadInit` — is described under "Against stage 4" above and does not
   exist.
5. **SCIP level-2 oracle.** — **done.**
   `yggdrasil scip-check PROG OUTDIR --target go` builds the program twice
   with the *same* stage-2 builder — `A*`, the shaken slice, and `A`, the
   full program `K + user`, the latter from the new `--no-shake` mode
   (`yggdrasil.shake-full` in `yggdrasil.shen`: every kernel defun, the
   eval-capable initialiser, no `trim-top`, no `rewrite-f-error`, manifest
   `shaken=false`) — recovers the KL-level call graph from each generated
   module with `go/ast`, and asserts **by name** that every defun the shake
   kept is reachable in the graph the backend actually emitted, and that
   every name the shaken build reaches the full build reaches too.
   `scip.go` is the whole implementation; `scip_test.go` is the host-gated
   fixture test plus unit tests for the residue accounting, the footprint
   parser and the declaration audit.

   The first version did index each module with `scip-go` and decode the
   two `index.scip` files in-repo, by hand, from the protobuf wire format —
   the `scip` CLI cannot be installed here (its `go.mod` carries `replace`
   directives) and the Go bindings are a module dependency this repo should
   not acquire for an optional check. That comparison is gone, and the
   paragraphs below say why: it compared four generated driver functions,
   which is the trivial direction of the inclusion and could not fail. The
   name `scip-check` is kept because the *contract* is the SCIP one — a
   graph extracted from the artifact, checked against the rules' graph —
   not because an indexer runs.

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
   are the nodes, the symbol occurrences in their bodies are the edges.

   **Two things about that graph that have to be right.** Both were wrong
   in earlier cuts of this check, and both make it say nothing.

   *An edge is an occurrence, not a call.* The generated code compiles a
   call to `PrimFunc(symF)` and a quoted symbol to `PrimCons(symF, ...)`.
   Only the first looks like an edge, but D1 above is explicit that the
   shake counts **every** kernel-defined symbol leaf of a body in any
   position, because the kernel constructs KL it may later evaluate. A
   call-position-only edge relation on the artifact side therefore reports
   every name kept for a constructed-code occurrence as unreachable, and
   manufactures a residue out of the two sides disagreeing about what an
   edge is: on `metaeval`, `partial-eval` and `tc-interp` that was the same
   15 names apiece (`==`, `fail-if`, `function`, `hdv`, `tlv`, `input`,
   `lineread`, `protect`, `unput`, `shen.+vector?`, `shen.f-error`,
   `shen.input-h+`, `shen.input-track`, `shen.output-track`,
   `shen.terpri-or-read-char`), not one of them a finding. With the two
   edge relations aligned the residue is empty on all three.

   *The body is behind a temporary.* `yggdrasil-build` emits
   `tmpN := MakeNative(func(__e *ControlFlow) { ... }, 2)` and then
   `tmpM := Call(__e, ns2_1set, symdo, tmpN)`, so a binding's fourth
   argument is an identifier, not the closure. An extractor that reads that
   argument as the body finds no edges at all: every lookup then sits
   outside every body and lands in the seed set, the reach set becomes the
   whole node set, and the comparison is vacuous. Following the one hop is
   what gives `fib` 7 seeds instead of 153.

   **The synthesised initialiser is not read the same way.** `trim-top`
   rewrites the arity table, the external-symbols list and the lambda table
   inside `shen.initialise` *against the footprint* (D7), so every kept name
   is quoted there by construction; following those quotations saturates
   the reach set and makes the check vacuous again, which is exactly the
   circularity the rules avoid by dropping those sites from `called-fns`
   (D2). So the check counts what the initialiser **calls** and not what it
   **quotes**. What it calls is real: `put`, `vector`,
   `shen.initialise-arity-table` and the rest of the fixed preamble.

   **The declared subtraction is transitive.** shen-go emits
   `PrimIsSymbol` at every call site of `symbol?`, so the kept defun
   `symbol?` is never looked up -- and neither is `shen.analyse-symbol?`,
   which nothing else calls. Subtracting the declared name without its
   subtree reports the subtree as a finding. The subtree is *derived* from
   the same declaration over the graph the backend emitted, never declared
   separately, and the verdict prints it apart from the declaration as
   `called-only-from-them=`.

   Yggdrasil `3c499a1`, shen-go `da55c5d`, host shen-cl, 2026-09-22; wall
   clock 12-32 s per fixture end to end (two shakes, two stage-2 builds).
   Re-checked for `fib` at these commits: `scip-check` prints
   `footprint=53 reachable=53; full defuns=688 reachable=545`,
   `special_forms=do,not called-only-from-them=none user-defuns=fib
   initialise=1`, and `OK ... accounted=2`.

   | fixture | footprint | A\* reachable | A defuns | A reachable | `applied` | `called-only-from-them` | verdict |
   |---|---|---|---|---|---|---|---|
   | `fib` | 53 | 53 | 688 | 545 | `do,not` | -- | OK |
   | `hello` | 53 | 52 | 687 | 544 | `do,not` | -- | OK |
   | `prolog` | 66 | 66 | 688 | 545 | `do,not` | -- | OK |
   | `metaeval` | 550 | 547 | 688 | 547 | `symbol?,variable?` | 3 | OK |
   | `partial-eval` | 548 | 545 | 688 | 545 | `symbol?,variable?` | 3 | OK |
   | `tc-interp` | 567 | 561 | 687 | 561 | `read-file-as-bytelist,symbol?,variable?` | 4 | OK |
   | `interpreter` | 66 | 58 | 697 | 544 | `not,variable?` | 7 | **FAIL** |

   `interpreter` fails with `kl-missing-in-full fix` and
   `kl-missing-in-full shen.fix-help` -- the delta itself is empty on both
   sides, so this is check (2), not check (1). The cause is the other half
   of `trim-top`: the *shaken* initialiser materialises the lambda table as
   `(cons fix (lambda X1 (lambda X2 (fix X1 X2))))`, a real call site the
   backend compiles to a lookup, while the full build's initialiser calls
   `shen.build-lambda-table` and fills the table at run time. So A\*
   reaches `fix` statically and A does not, though A reaches it when it
   runs. It is an open finding: either the A-side extractor learns to
   follow `shen.build-lambda-table`, or check (2) stops counting the
   initialiser's own edges. Both are changes to the *graph*; neither is a
   subtraction from the residue, and the choice is a person's.

   **The delta, and how it is accounted for.** It is small and it is
   entirely *declared*, which is the useful outcome: there is no integer to
   eyeball and no subtraction a person performs in their head. `kernel.kl`
   carries 54 defuns for `fib`: the 53 the rules put in `reach`, plus the
   synthesised `shen.initialise`. Of the 55 defuns the backend emits (those
   54 plus the user's own `fib`), the backend-level walk reaches 53. The
   three declared causes, and there is no fourth:

   - `shen.initialise`, which is not a `reach` member and never was --
     nothing inside the program calls it; the builder's generated `main`
     does, from the manifest's `init=` key;
   - the user's own defuns, which are in the emitted graph but not in the
     kernel footprint because they live outside `kernel.kl`; and
   - the target's **special forms**, `builders.json`'s `special_forms`
     with a `special_forms_source` citing the line of the port for each
     name, **together with whatever only they reach**. These are kernel
     defuns the shake keeps and the backend lowers inline, so the generated
     code never names them and the recovered graph has no edge to them at
     all. For `go` that is seven names: `do`, which
     `shen-go/src/compiler.shen` handles as a parse head, and the six
     entries of `codegen.go`'s `shenPrimitive` table that are also kernel
     defuns -- `not`, `symbol?`, `variable?`, `integer?`,
     `read-file-as-bytelist`, `read-file-as-string` -- each emitted as a
     bare `PrimX` call because kernel chunks compile sealed.

   The declaration is a property of the **port**, not of the program, and
   deriving it from a fixture is a live trap: `fib` and `hello` leave only
   `do` and `not` in residue, and a `special_forms` list fitted to them
   fails `metaeval` and `tc-interp` on names that are not findings. Which
   of the seven a given slice needs subtracted varies; the declared class
   does not. The check therefore audits the declaration too, reporting
   which names it `applied` and which went `unused` in this run, and
   failing on any declared name that is not a kernel defun at all.

   Anything outside those three causes is printed name by name
   (`kl-delta-missing`, `kl-delta-extra`, `kl-missing-in-full`,
   `special-forms-unknown`) and fails the check.

   The `applied` / `called-only-from-them` columns of the table above are
   that accounting per fixture: `do,not` on the small programs, and
   `symbol?`/`variable?`/`read-file-as-bytelist` with their helper subtrees
   on the ones that exercise more of the kernel. Which of the seven a slice
   needs is a property of the program; the class is a property of the port.

   **What this proves.** On six of the seven repo fixtures, every name A\*
   can reach A can reach too, and every defun the shake kept is either
   reachable in A\*'s own emitted graph or accounted for by a declared
   cause. That is compositional level-2 *inclusion*: the backend did not
   invent an edge the rules never saw, and it did not drop a kept function
   because its neighbours were gone. The seventh, `interpreter`, is the
   open finding above, and it is check (2) disagreeing with itself about
   how the two initialisers build the lambda table -- printed by name, not
   subtracted.

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
