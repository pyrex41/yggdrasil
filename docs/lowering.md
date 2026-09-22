# Design note: port-specific lowering as declared equivalences

**Status**: proposed, with a measurement (Yggdrasil, September 2026)
**Builds on**: [port-contract.md](port-contract.md) (`native_overrides`),
[analysis-rules.md](analysis-rules.md) (the rule set, the runtime trace),
[verification-guide.md](verification-guide.md) section 12
**Question**: every port already rewrites kernel functions into natives,
invisibly, at boot. Can that rewriting be made an explicit, per-port
KL-to-KL pass whose correctness is checkable, so that the shake, the
weave and the static checks see it, and so that the port's own runtime
becomes reachable by the same analysis?

## The measurement

shen-go's `InstallKernelFast` rebinds **58** kernel functions to Go
natives at startup (the list is `native_overrides` on the `go` entry of
`builders.json`, with its source and the test that checks it).
Intersected with shaken slices, re-measured against shen-go da55c5d:

| slice | kernel defuns kept | of which shen-go replaces natively |
|---|---|---|
| `tests/fib.shen` (eval-free) | 54 | 18 |
| `tests/partial-eval.shen` (eval-capable) | 549 | 49 |

The count was **54** and the intersections **17** and **45** until
`TestNativeOverridesMatchKernelFast` parsed `InstallKernelFast` and found
four rebindings the hand-kept list had never carried: `<-vector`, `==`,
`@p` and `shen.hds=?`. The list had said `native_overrides_verified: true`
the whole time, which is why that flag is gone and a `_checked_by` naming
a test is what the word "verified" costs now.

**When** the swap happens is part of the measurement, not a footnote: the
generated `main` runs `shen.initialise` before
`runHelper("InstallKernelFast", ...)`, so these KL bodies *do* execute
during boot and are replaced only afterwards. `yggdrasil trace-check
tests/fib.shen OUT --target go` records a KL entry for 11 of fib's 18.

A third of an eval-free slice is code the port never runs as KL. Today
that is invisible to everything in the verification guide: the shake
keeps the KL body, the weave instruments it, the trace records an entry to
it only if the program reached it during boot before the natives were
installed (afterwards the native is entered instead), and the level-3 graph
recovery sees a binding that is dead on arrival. The port contract
records this as an unverified obligation; this note is the mechanism that
discharges it.

## The idea

A port-specific lowering is a **table of declared equivalences**, not a
dialect:

```
equiv(F, P, Arity)     kernel function F is implemented by port primitive P
```

and a pass that rewrites `(F a1 .. an)` to `(P a1 .. an)` wherever F is
called, after which the shake drops F because nothing calls it. The pass
is permitted to do exactly that substitution and nothing else: no change
to control flow, evaluation order, or any value. A lowered program is KL
plus that port's primitives, never a new language.

Three consequences, each a layer of the correctness argument:

1. **Each row is one lemma, owned by the port.** F has a KL body; P is
   native. "P implements F" is checkable by differential testing: run F's
   KL body (on any host, including this one) and P (on the port) over the
   same inputs and diff. Shen-Backpressure's `shen-derive` already does
   this shape of check for user code; here it is applied to the port's
   own natives. Each row therefore carries `verified` and a provenance,
   as `port_reads` does.
2. **The pass is correct if it applies only declared rows.** A syntactic
   property, one Datalog rule (below). The pass never invents a rewrite.
3. **The composed artifact is equivalent by transitivity.** Shake
   preserves the program; each substitution preserves it by its row's
   lemma; the backend sees KL plus primitives it already implements.

## Rules

Facts, from the port's table and from the lowered KL:

```
equiv(F, P)           declared by the port (builders.json)
equivVerified(F, P)   the row's differential test passed on this port
lowered(F, P)         the pass replaced a call to F with P somewhere
primDef(P, G)         from the port's static graph (SCIP or extractor):
                      P's native definition references runtime function G
targetEdge(G, H)      the port's runtime call graph, same source
```

Rules:

```
badLowering(F, P) :- lowered(F, P), !equiv(F, P).
unverifiedLowering(F, P) :- lowered(F, P), !equivVerified(F, P).
runtimeReach(G) :- reach(F), lowered(F, P), primDef(P, G).
runtimeReach(G) :- reach(F), prim(F), primDef(F, G).
runtimeReach(G) :- runtimeReach(H), targetEdge(H, G).
```

`badLowering` must be empty: that is the whole correctness check of the
pass. `unverifiedLowering` is reported, not failed, and is what the
conformance table shows as `declared` rather than `verified`.
`runtimeReach` is the new thing: the port's runtime footprint as a
computed relation, joined to the KL-level `reach` through the lowering
table. The target-level trace (Go's `-cover -coverpkg=all`, or a
hand-written aspect on another port) then checks it exactly as the
KL-level trace checks `reach`: executed runtime functions must be
contained in `runtimeReach`. On the fib artifact today the runtime has
314 functions of which 94 execute; with `runtimeReach` computed, the
94 must be a subset, and any function outside it names a missing
`equiv` row or a hole in the extractor.

## Keeping the existing guarantees

A lowered slice is not portable, so it cannot go through the eight-host
byte-identity check or the cross-target parity gate. Lowering is
therefore a separate stage *after* the canonical shake, and the canonical
slice stays the artifact of record. Parity for a port P becomes three
runs: canonical on the reference host, canonical on P, lowered on P. The
third differing from the second is a wrong `equiv` row, and the check
names it.

The manifest of a lowered directory records `lowered-for=P` and one
`lowered=F P` line per substitution, so every downstream check can see
what was done.

## What is per port and what is shared

Per port: the `equiv` table, its differential tests, the extractor that
produces `primDef`/`targetEdge`, and the target-level trace. Shared: the
pass, the `badLowering` rule, the three-way parity arrangement, and
`runtimeReach`. A new port opts in by writing a table and its tests, not
a compiler pass.

## Staging

1. **Table and report (measurement before mechanism).** `native_overrides`
   for go is in `builders.json` now. Add `yggdrasil why --target go` a
   line per kept kernel function that the port replaces natively. The
   summary count exists: `yggdrasil contract --target go` prints the
   declaration, its provenance, and `installed_after=shen.initialise` --
   the phase that decides whether the KL bodies below ever run. No output
   change.
2. **Differential tests per row, in the port's repository.** The prompt
   for that work is [`lowering-shen-go-prompt.md`](lowering-shen-go-prompt.md).
   The port exports its table as JSON (`equiv`, arity, verified,
   provenance) and Yggdrasil's `builders.json` imports it.
3. **The lowering pass**, `yggdrasil lower OUTDIR LOWDIR --target P`,
   as substitution against the imported table, with `badLowering` in all
   three engines and the three-way parity in the test suite.
4. **Two-level reachability.** `primDef` and `targetEdge` from the
   shen-go extractor (today `go/ast` over the generated module, as
   `scip-check` does, plus the `kl` package), `runtimeReach` in the rules,
   and the Go coverage trace as its check.
5. Repeat 2 to 4 for a second port.
