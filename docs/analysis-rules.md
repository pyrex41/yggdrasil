# Design note: the shake as a rule set

**Status**: stages 1–3 shipped, with the runtime trace beside them; stages
4–5 proposed (Yggdrasil, September 2026)
**Code**: `analysis/analysis.dl`, `analysis/refeval.py`; `yggdrasil.shen` —
`ygg.dl`, `*shake-rules*`, `*trace-rules*`, `yggdrasil.facts`,
`yggdrasil.shake-traced`, `yggdrasil.trace-check`, `yggdrasil.footprints`;
`main.go` — `cmdFacts`; `trace.go` — `cmdTraceCheck`; `analysis_test.go`,
`footprint_test.go`, `trace_test.go`

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
`mentionsprim`, and `initprim` added; `writes` dumped as `initwrite` (with
`defunwrite` beside it) and `called`/`readGlobal` declared for the runtime
trace, empty until `yggdrasil trace-check` fills them. `reads`/`readsIn`/
`portReads` are stage 4 and are not dumped yet. `portGlobal` is dumped and
declared now so the fact set does not move under stage 2. See the
declarations at the top of `analysis/analysis.dl`.

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

Initialisation order. This is the check the thread asked for and the one
the shake does not have. A form may read a global only if an earlier kept
form, or the port, wrote it. `before` is the index order of kept toplevel
forms; user files come after the synthesised initialiser, in source order.

```
readBeforeWrite(N, V) :- reads(N, V), !portGlobal(V),
                         !(writes(M, V), M < N).
```

Any `readBeforeWrite` fact fails the shake with the form and the variable
named. Measured today on `tests/fib.shen`: the slice reads 6 globals, the
initialiser writes 35, and the only read without a prior write is
`*stoutput*`, which is a port global. So the rule passes on current output;
its value is that it keeps passing when the kernel's boot order or the
user's toplevel forms change.

Dead initialisation.

```
liveGlobal(V) :- readsIn(F, V), reach(F).
liveGlobal(V) :- reads(_, V).
liveGlobal(V) :- portReads(V).
deadInit(N, V) :- writes(N, V), !liveGlobal(V).
```

A `deadInit` form can be dropped from the synthesised initialiser. On fib
that is up to 29 of 35 sets, before `portReads` is subtracted. This is the
one place the rule set makes the artifact smaller, and it is gated on a
per-builder fact list that has to be written by reading each port's
runtime, so it ships after the checks, not with them.

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
`ygg.io-writes` the init-order check uses); `defunwrite` is the globals a
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

### For stage 4

`readglobal.facts` is written as one symbol per line, TSV, in the facts dir
beside the rest of the dump. That is the shape stage 4's `liveGlobal` wants:
a global that no run ever reads is a candidate for `deadInit` pruning, and a
global some run does read is direct evidence that it is not. `initwrite.facts`
is stage 4's `writes` relation, already dumped.

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
   `yggdrasil.shen` scans the final form sequence — the kept toplevel
   forms as `trim-top` leaves them, then the user files' toplevel forms
   in manifest order — for `(value V)` and `(set V _)` at any depth
   except inside a `defun`, `lambda` or `freeze` (those bodies do not run
   while the artifact boots). A read with no earlier write, and no port
   global to explain it, prints
   `yggdrasil-shake: FAIL init-order form=N reads=V` and aborts before
   `kernel.kl` is written; the Go driver surfaces that line. A clean run
   records `init-order=checked` in both manifests, after `needs-eval`.
   Fixtures: `tests/init-order-bad.shen` (refused) and
   `tests/init-order-ok.shen` (same reads, boot order); `initorder_test.go`
   is the host-gated test. Measured: `kernel.kl` byte-identical on every
   existing fixture, manifests differing only by the new line.
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
   `init-order`. Measured: of the sixteen fixtures, **only the new
   `tests/computed-name.shen` triggers it** (`computed-names=computed-call`);
   every other fixture, the eval-capable ones included, is `none` — which is
   the result worth having, since it says the hypothesis is not vacuous and
   not routinely violated. `footprint_test.go` is the host-gated test.
4. **Dead initialisation.** Write `portReads` for each builder by reading
   its runtime, enable `deadInit` pruning behind a flag, and let the
   parity gate decide per target whether it is safe to default on. The
   runtime trace supplies two of its inputs already: `initwrite.facts` is
   the `writes` relation, and `readglobal.facts` is per-run evidence for
   `liveGlobal` — a global some run reads is demonstrably live, whatever
   the syntax says. Both are TSV, one symbol per line, in the facts dir
   (see "Runtime trace" above).
5. **SCIP export**, optional and last, and a level-2 oracle rather than a
   picture. Emit a SCIP index of the shaken program — each symbol carrying
   its `adds` and `exclusive` in the documentation field, so an editor can
   colour-band by footprint as the thread suggested — and a second index of
   the *full* compiled artifact. Restricted to the nodes reachable from
   `main`, the two must agree node for node for a compositional builder,
   one whose backend emits a direct call per KL call (shen-go's direct-call
   output is the reference case). That makes SCIP a check on stage 2 and
   not only on stage 1: the shake's claim is about the KL it writes, and a
   node-for-node agreement between the index of the shaken artifact and the
   index of the unshaken one says the backend did not invent an edge the
   rules never saw. Disagreement is a bug in the builder or in the rules,
   the same status the Soufflé oracle has for stage 1. It stays optional
   because it only holds for builders that compile calls compositionally;
   a builder that routes everything through a dispatch table has no node
   graph to compare.

Stages 1 and 2 are the ones that change what Yggdrasil can claim. Stage 3
is what makes the trust argument true in the code rather than in a
document, and it is now true there. Stage 4 is the only one that shrinks
artifacts, and by little; stage 5 is the only one that checks stage 2.
