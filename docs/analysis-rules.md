# Design note: the shake as a rule set

**Status**: stages 1–4 shipped, stage 5 proposed (Yggdrasil, September 2026)
**Code**: `analysis/analysis.dl`, `analysis/refeval.py`; `yggdrasil.shen` —
`ygg.dl`, `*shake-rules*`, `ygg.*init-rules*`, `yggdrasil.facts`,
`yggdrasil.footprints`; `main.go` — `cmdFacts`; `prune.go`;
`builders.json` — `port_reads`; `analysis_test.go`, `footprint_test.go`,
`prune_test.go`

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
`portReads` were stage 2 and 4. All four are dumped now (stage 4), so the
dump carries twenty-one relations. See the declarations at the top of
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

## Staging

1. **Rules on paper, oracle first.** — **done.**
   `analysis/analysis.dl` is the rule set as Soufflé Datalog; `yggdrasil
   facts PROG DIR` (`yggdrasil.facts` in `yggdrasil.shen`, a sibling of
   `yggdrasil.shake` that reuses its pipeline and writes no artifact) dumps
   fifteen TSV relations; `analysis/refeval.py` is a stdlib-Python
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
artifacts, and by little — 6% of `kernel.kl` on an eval-free program, 0.1%
on an eval-capable one, which is the measurement that says where the
remaining bytes are and that it is not here. Stage 5 is the only one that
checks stage 2.
