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

A table of overlaps is a property of a **phase of a build configuration**,
not of a slice, and the headline this note used to carry -- "a third of an
eval-free slice is code the port never runs as KL" -- was false for the
artifact as shipped. The overlap is a third; "never runs as KL" is not
what happens.

Taken on Yggdrasil `3c499a1`, shen-go `da55c5d`, host shen-cl (`shake`
then `trace-check --target go`), 2026-09-22:

| slice | kernel defuns kept | in `native_overrides` | of those, recorded a KL entry |
|---|---|---|---|
| `tests/fib.shen` (eval-free) | 54 | 18 | 11 |
| `tests/partial-eval.shen` (eval-capable) | 549 | 49 | not measured |

shen-go's `InstallKernelFast` rebinds **58** kernel functions to Go
natives (the list is `native_overrides` on the `go` entry of
`builders.json`, with its source and the test that checks it). The
intersections were **17** and **45** until
`TestNativeOverridesMatchKernelFast` parsed `InstallKernelFast` and found
four rebindings the hand-kept list had never carried: `<-vector`, `==`,
`@p` and `shen.hds=?`. The list had said `native_overrides_verified: true`
the whole time, which is why that flag is gone and a `_checked_by` naming
a test is what the word "verified" costs now.

**Where the list came from matters as much as its contents.** The
original list was read by hand at shen-go `24b2c00` and went four names
short; the table above is re-taken at `da55c5d`, and
`TestNativeOverridesMatchKernelFast` now re-reads
`kl/kernelfast.go` on every run rather than trusting a transcription. The
phase did not move between those two commits -- the generated `main`
emits `runHelper("InstallKernelFast", ...)` after `shen.initialise` at
both -- so what changed is the contents, not the shape. Any figure in
this note that is not attributed to a shen-go commit should be read as
not re-taken.

**When the swap happens is the rest of the fact.** The generated `main`
runs `shen.initialise` before `runHelper("InstallKernelFast", ...)`, so
these KL bodies *do* execute during boot and are replaced only
afterwards. `yggdrasil trace-check tests/fib.shen OUT --target go`
records a KL entry for **11 of fib's 18**: `<-vector`, `empty?`, `fail`,
`hdstr`, `limit`, `map`, `put`, `reverse`, `shen.+string?`, `vector`,
`vector->`. Every one of the 11 is tagged `b` in the trace's phase
column, i.e. in the boot, and not one override is entered in the program
phase. The honest reading of the table is therefore:

- in the **boot** phase of a shen-go build, none of the 18 is native;
- in the **program** phase, all 18 are, and the 7 that never recorded an
  entry are the ones this input's boot did not reach either.

A different port, or shen-go with the install moved before
`shen.initialise` (which is the real fix, and belongs to the port -- see
the deferred list in the campaign notes), would give a different table
from the same slice.

What remains true is the gap this note exists to close: for the program
phase the shake keeps a KL body nothing will enter, the weave instruments
it, and the level-3 graph recovery sees a binding that is dead on
arrival. The port contract records this as an open obligation; the
mechanism below is a proposal to discharge it.

## The idea

A port-specific lowering is a **table of declared equivalences**, not a
dialect:

```
equiv(F, P, Arity)     kernel function F is implemented by port primitive P
```

and a pass that rewrites `(F a1 .. an)` to `(P a1 .. an)` wherever F is
called, after which the shake drops F because nothing calls it. The pass
is permitted to do exactly that substitution and nothing else -- it never
reorders, never inlines, never touches a form it has no row for. That is
a constraint on the *pass*, and it is checkable. It is not the same as
"the lowered program computes what the original did": a native in place
of a KL body changes stack behaviour and can change the identity of an
error even when every value agrees, which is why point 3 below is an
argument with a hole rather than a proof. A lowered program is KL plus
that port's primitives, never a new language.

Three consequences, each a layer of the correctness argument:

1. **Each row is a differential test on a sample, not a lemma.** F has a
   KL body; P is native. "P implements F" is checked by running F's KL
   body and P over the same inputs and diffing. That is a test, and it
   establishes what a test establishes: agreement on the inputs tried. It
   is worth having -- Shen-Backpressure's `shen-derive` does this shape of
   check for user code -- and it is not a proof, so a row's status is
   `declared` until a named test exists and `verified` after, exactly as
   `port_reads` works.

   There is a second gap, and it is not about sample size. The test runs
   F's KL body on a **full kernel VM**. The pass applies the substitution
   inside a **shaken** program whose property vector, lambda table and
   arity table have been trimmed to the footprint and whose `shen.f-error`
   has been replaced. A row can hold on the first and not on the second:
   F's body may reach a name the trimmed tables no longer carry, or fail
   differently because the failure handler is not the same one. Nothing
   below closes that, and it should not be described as closed.
2. **The pass is correct if it applies only declared rows.** A syntactic
   property, one Datalog rule (below). The pass never invents a rewrite.
   This one really is checkable, and cheaply.
3. **The composed artifact is *argued* equivalent by transitivity -- and
   the argument has the hole above in it.** Shake preserves the program;
   each substitution preserves it as far as its row's test went; the
   backend sees KL plus primitives it already implements. Transitivity is
   only as strong as its weakest link, and the weakest link here is a
   sample-based test discharged in a different environment from the one
   the substitution lands in. "No change to control flow or any value" is
   also more than a native substitution can promise in general: swapping a
   KL body for a native changes stack behaviour and can change the
   identity of an error. The claim to make is narrower, and the check that
   would actually support it is parity of the lowered slice against the
   canonical slice on the same port -- which the three-way parity below
   does, and which is why it, not transitivity, is the load-bearing part.

## Rules

Facts, from the port's table and from the lowered KL:

```
equiv(F, P)           declared by the port (builders.json)
equivVerified(F, P)   the row's differential test passed on this port
lowered(F, P)         the pass replaced a call to F with P somewhere
primDef(P, G)         from the port's static graph (an extractor; on
                      shen-go that means go/ast over the generated module
                      and the kl package, as scip.go does -- the Go-level
                      SCIP index path was deleted, see analysis-rules.md
                      stage 5): P's native definition references runtime
                      function G
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
pass. `unverifiedLowering` is reported, not failed, and is what `yggdrasil
contract --target P` would show as `declared` rather than `verified`
(there is no `yggdrasil conformance` command; the level-1 report is
`contract`).
`runtimeReach` is the new thing: the port's runtime footprint as a
computed relation, joined to the KL-level `reach` through the lowering
table. The target-level trace (Go's `-cover -coverpkg=all`, or a
hand-written aspect on another port) then checks it exactly as the
KL-level trace checks `reach`: executed runtime functions must be
contained in `runtimeReach`. The "314 functions of which 94 execute"
figure this note used to quote here has been removed rather than carried
forward: it was taken with the deleted Go-level SCIP path, on a shen-go
commit that is no longer the one the artifact builds against, and nothing
in the repository re-takes it.

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
   all three rule evaluators (two transcriptions and Soufflé -- see
   `verification-guide.md` section 6) and the three-way parity in the
   test suite. The
   three-way parity is the part that carries the correctness argument;
   the per-row differential tests narrow where a failure came from.
4. **Two-level reachability.** `primDef` and `targetEdge` from the
   shen-go extractor (today `go/ast` over the generated module, as
   `scip-check` does, plus the `kl` package), `runtimeReach` in the rules,
   and the Go coverage trace as its check.
5. Repeat 2 to 4 for a second port.
