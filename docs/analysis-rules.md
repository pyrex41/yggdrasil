# Design note: the shake as a rule set

**Status**: stages 1–3 and 5 shipped, stage 4 proposed (Yggdrasil, September 2026)
**Code**: `analysis/analysis.dl`, `analysis/refeval.py`; `yggdrasil.shen` —
`ygg.dl`, `*shake-rules*`, `yggdrasil.facts`, `yggdrasil.footprints`,
`yggdrasil.shake-full`; `scip.go` — `scip-check`;
`main.go` — `cmdFacts`; `analysis_test.go`, `footprint_test.go`

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
`portReads` are stage 2 and 4 and are not dumped yet. `portGlobal` is
dumped and declared now so the fact set does not move under stage 2. See
the declarations at the top of `analysis/analysis.dl`.

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
4. **Dead initialisation.** Write `portReads` for each builder by reading
   its runtime, enable `deadInit` pruning behind a flag, and let the
   parity gate decide per target whether it is safe to default on.
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
artifacts, and by little; stage 5 is the only one that checks stage 2, and
what it found first was that the reference backend is not compositional at
the level the note assumed — see its entry.
