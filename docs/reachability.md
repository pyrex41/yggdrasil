# Design note: seed-set reachability, not Warshall closure

**Status**: decided (Yggdrasil, June 2026)
**Code**: `yggdrasil.shen` — `call-graph`, `footprint`, `reach`
**Decision**: compute the shaken footprint with a worklist traversal (BFS/DFS)
over a cached direct call graph. Do **not** compute the transitive closure
(Warshall), and do **not** add an external compute step (e.g. Julia) to speed
a closure up — the closure itself is the wrong tool, not its implementation.

## The problem Yggdrasil actually solves

Tree-shaking is: given the kernel call graph and a seed set
(`shen.initialise` plus every function the user program calls), find all
kernel defuns *reachable from the seeds*, and discard the rest.

That is **single-source (multi-seed) reachability** — one row of the
reachability relation, unioned over a handful of seeds. It is not all-pairs
reachability.

## Why the original Yggdrasil used Warshall, and why Yggdrasil dropped it

Tarver's original Yggdrasil computed the full transitive closure of the
kernel call graph with Warshall's algorithm: Θ(N³), and — because `fdg`
builds the matrix over `extract-Fs Code`, *every symbol* in the kernel, not
just the defuns — N was the symbol count (thousands), not the ~1,000 defuns.
The kernel it shipped against was not small either: the bundled 34.6-era
`KLambda/` is 1,043 defuns / 355 KB, the same order as 41.2. So the cubic
cost was never actually cheap. The evidence suggests it was meant to be run
once and persisted — `fdg` reuses an already-bound `*ttable*`, and there is
a `write-table-to-file`/`ttrans.shen` writer — but that writer is commented
out and the `array`/`:=`/`for` DSL it needs never shipped, so it is doubtful
the closure was ever successfully run at full kernel scale. Against
Tarver's refreshed S41.2 kernel the per-shake numbers are:

| | S41.2-refresh numbers |
|---|---|
| Kernel defuns (V) | 683 (S41.2 refresh) |
| Call edges (E) | 2,568 (≈ 4 per node — very sparse) |
| Kernel KL source | ~280 KB |
| Warshall closure | V³ ≈ 3.2 × 10⁸ ops, plus a V×V matrix |
| Worklist reachability | O(V + E) ≈ 3.3 × 10³ graph ops per shake |

Beyond the constant-factor pain, the closure computes ~466 thousand
pairwise answers per shake and then throws away all but one row's worth.


## What Yggdrasil does instead

1. **Build the direct call graph once** (`build-call-graph`): for each
   `defun`, record which kernel-defined names appear in its body. This is
   the only expensive pass (it walks every symbol leaf of ~280 KB of KL),
   so it is cached to disk (`KLambda/callgraph-s42-20260825.shen`) and reloaded on
   subsequent shakes. The cache is keyed to the kernel version, which only
   changes when the kernel does.
2. **Per shake, traverse from the seeds** (`footprint` / `reach`): a pure
   worklist — pop a function, skip if seen, otherwise mark it and push its
   callees. The visited set *is* the result.

Note on terminology: `reach` pushes callees onto the *front* of the
worklist, so the traversal order is depth-first. This is immaterial — BFS,
DFS, and any other correct worklist order compute the identical reachable
set. The choice that matters is *traversal vs. closure*, not *which
traversal*.

Conservatism note: `called-fns` collects every kernel-defined symbol that
appears anywhere in a body, not just call position. That over-approximates
(a function named as data keeps its target alive) but never
under-approximates, which is the correct failure direction for a shaker —
especially with `eval-kl` and higher-order kernel code in play.

## Why not Julia (or bitsets, or any faster Warshall)

The temptation: Warshall closure "feels idiomatic" and a tuned
implementation (Julia, bitset rows at word-parallelism w=64) gets closure
down to O(V³/w) ≈ 2.2 × 10⁷ word ops. Objections, in order of importance:

1. **Wrong asymptotics for the question asked.** Closure is Ω(V²) just to
   write its output. Traversal is O(V + E). On a graph this sparse the gap
   is ~190× even against an ideal bitset closure, and the closure's extra
   work buys answers (reachability between arbitrary non-seed pairs) that
   no part of the pipeline consumes.
2. **Portability is the product.** Stage 1's contract (see the header of
   `yggdrasil.shen`) is that it is pure Shen against the certified kernel
   API — no external toolchain. (Host portability in practice is narrower
   than "any certified Shen" because the user KL inherits the host's
   `bootstrap` compiler; see the README gotcha. The shake *logic* is
   portable: shen-lua as host reproduces `kernel.kl` byte-for-byte.)
   Adding a Julia (or C, or anything) sidecar for graph math breaks the
   one property the tool exists to provide. There is no performance
   problem to justify it: per-shake reachability over 683 nodes is
   milliseconds in pure Shen.
3. **The actual bottleneck was never the traversal.** Profiling during the
   41.1 retarget showed the cost is in *building* the graph (one walk of
   280 KB of KL — solved by the disk cache and the `defp` property-list
   membership test) and in reading/writing KL files. Speeding up the
   per-shake traversal optimizes a rounding error.

If per-shake traversal ever did show up in a profile, the in-language fix
is to replace `row-calls`'s linear scan of the row list (O(V) per pop,
O(V·E) per shake — still ≪ closure) with a property-list lookup, the same
trick `kernel-defun?` already uses. That is a 10-line change, not a new
toolchain.

## Capability reporting (`reaches=` / `cannot-reach=`)

The one closure-shaped idea that earns its keep here is *sink* reachability,
not all-pairs. The manifest now reports, per shake, which effectful
capabilities the emitted artifact can invoke. The gateways are grouped
primitives (`*capabilities*` in `yggdrasil.shen`): `eval` → `eval-kl`,
`read` → `read-byte`, `write` → `write-byte`, `file` → `open`/`close`,
`clock` → `get-time`. A capability is *unreachable* exactly when the
emitted KL contains none of its gateways, so it is derived for free from
the primitive set `find-primitives` already computes — no extra traversal.

`cannot-reach=eval` is a static, certifiable property of the artifact (the
code literally has no occurrence of `eval-kl`), which is the kind of
guarantee Tarver's safety-critical framing wants. It also stays in
lock-step with the eval-strip: `eval-kl` leaves `Prims` precisely when the
program is eval-free, so `cannot-reach` lists `eval` exactly then.

A future sharper version would precompute *reverse* reachability over the
transpose graph (which kernel functions can reach a given sink) to drop
functions kept alive only by an eval edge, and to publish an auditable
"who can reach X" table for the whole kernel. That is single-source on the
transpose, still not all-pairs Warshall.

## Optional Warshall closure (homage)

`warshall-footprint` in `yggdrasil.shen` is the finished version of
Tarver's original — the same iterative Warshall (pivot outermost), built on
Shen vectors instead of the `array`/`:=`/`for` DSL that never shipped, so it
actually runs. It is off by default; `(set *use-warshall* true)` routes
`footprint` through it. It exists for coherence with 1.0 and as a
differential oracle: on a given graph it must yield the same footprint as
the worklist `reach`. It fixes 1.0's irreflexive-closure leaf-drop (each
seed is unioned into its own row). Cost is the catch — O(V³) over a V×V
matrix, fine on the fixtures' small graphs, impractical on the full kernel —
which is the whole reason the worklist is the default.

## When to revisit

Reconsider only if a future feature needs *many-pair* reachability over a
static graph — e.g. "which functions can reach `error`?" asked for every
function, or cycle/SCC structure for load ordering. Even then the standard
answer is Tarjan SCC + condensation (linear time) or per-query traversal,
not Warshall. Full closure likely makes sense only when queries are dense over
the pair space and the graph is small; neither holds here.
