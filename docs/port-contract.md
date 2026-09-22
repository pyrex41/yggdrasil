# Design note: the port contract for a verifiable shake

**Status**: proposed (Yggdrasil, September 2026)
**Builds on**: `docs/analysis-rules.md` (the rule set, the runtime trace,
the SCIP level-2 oracle), `docs/parity.md`, `builders.json`
**Question**: the checks shipped so far were built against one port
(shen-go). What does an arbitrary port have to declare and satisfy so the
same checks, and the equivalence argument they support, apply to it? SCIP
will not exist for every target; the contract must not depend on it.

## The claim a port is being asked to support

For a Shen program A, its shaken slice A*, and a port P with builder B_P:
on every input, B_P(K + A) and B_P(K* + A') produce the same observable
trace, up to the documented partial-function refinement. The argument in
`analysis-rules.md` splits this into KL-level obligations (reachability
soundness, footprint closure, initialiser equivalence) that Yggdrasil owns,
and a transfer step through B_P that the port owns. The port's part of the
contract is what makes the transfer step checkable rather than assumed.

Today every builder satisfies **level 0** below, and shen-go alone has been
taken through levels 1 to 3. The contract is the ladder itself; a port's
conformance is how far up it has been taken, stated per rung, never as a
single yes.

## Level 0: the builder contract (exists today)

Load the defuns in `kernel.kl`, call `shen.initialise`, run each user file's
forms in manifest order, ignore manifest keys you do not recognise. This is
what `builders.json` encodes and every target already meets. Nothing here
is verifiable beyond "the artifact runs".

## Level 1: self-description (static facts the port declares)

A per-target block in `builders.json`. These are inputs to the Datalog
rules, so a wrong declaration produces a wrong footprint or a wrong check
verdict, which is why each carries a `_verified` flag and a provenance
comment naming the runtime source it was read from. Two exist already;
four are new.

| key | meaning | consumed by | exists |
|---|---|---|---|
| `port_reads` | globals the runtime reads natively | `liveGlobal` (dead-init) | yes, verified for go only |
| `port_writes` | globals the runtime binds before `shen.initialise` runs | `readBeforeWrite`, `uncoveredRead` | today hardcoded as `*stinput*`/`*stoutput*` in `*global-primitives*`; should move here |
| `special_forms` | KL names the port's compiler lowers without a lookup (`do` on shen-go) | level-3 delta accounting: a kept defun that is never entered | new |
| `native_overrides` | kernel defuns the port replaces with natives (`InstallKernelFast` on shen-go, `overrides.scm` on shen-scheme) | see the obligation below | new |
| `native_deps` | for each override, the kernel names its native implementation calls back into | added as `edge(F, G)` facts | new |
| `call_style` | `direct`, `lookup`, or `mixed`: how a compiled call resolves | decides whether level 3 is static inclusion or graph recovery | new |

**Obligation N (native overrides).** The footprint is computed from the
kernel's KL. If the port swaps a defun for a native, the native's callees
are invisible to the rules. So: a native override may call back into the
kernel only through names listed in `native_deps`, and Yggdrasil adds those
as edges before reachability. A port that cannot enumerate them declares
`native_overrides_verified: false`, and the conformance report says the
footprint is unsound for that port at level 1. shen-go's list is readable
from `kl/kernelfast.go`; shen-scheme's from `overrides.scm`.

**Obligation D (dispatch).** A symbol applied as a function resolves
through the artifact's own table (the trimmed lambda table and arity table
the shake emits), and a name absent from it is an error, not a fallback
into a full kernel the port happens to link. A port that links the whole
kernel behind the slice (ShenScript `--linked`, an interpreter that loads
its own kernel first) is not running the shaken program and must say so:
`dispatch: full-kernel`. Every check above level 0 is vacuous for such a
build, and the report must print that rather than a pass.

## Level 2: dynamic evidence (the runtime trace)

`--trace` weaves at the KL level, so no port code is involved and every
port gets it for free. The port's obligations are about the trace reaching
disk:

- **Obligation F (flush).** Bytes written with `write-byte` to a stream
  opened with `open` reach the file by process exit. Nothing is asked of
  the port here any more, and no port declares anything: the weaver now
  appends one extra toplevel form after the last form of the last user
  file, `(ygg.trace-end)`, which writes the run's `e<TAB>end` record and
  then `close`s the stream. `close` is a KL primitive and was already in
  the manifest's primitive list, so the obligation is discharged for
  every port at the cost of no new capability. The consequence is that
  the trace is a complete document only for a program that terminates
  normally, and that is exactly what `trace-check` requires: a trace with
  no end record is refused
  (`yggdrasil-trace-check: FAIL truncated=no-end-record`) rather than read
  as a shorter run.
- **Obligation E (entry).** Every kept defun is entered through its KL
  body, so the woven `(ygg.traced F)` runs. A native override (Obligation
  N) is entered through the native, so it never records itself; its name
  is excluded from `called` on that port, and the report says how many.

The check is `uncoveredCall(F) :- called(F), kernel(F), !reach(F)` empty,
per run, per port. It is the one piece of evidence that is identical in
form on every target, which is why it is the floor of the ladder above
level 0, not SCIP.

**What emptiness means here, and what it does not.** On a port whose
artifact *is* the slice, that query is empty by construction: a call to a
kernel defun outside `reach` is a name the artifact does not contain, so
it is an undefined-function crash and there is no finished run to read
facts from. The query has teeth on a `dispatch: full-kernel` build, on a
host-side facts dump, and against a `reach` computed from a different
program than the one that ran — and nowhere else. What level 2 does
produce on every port is **coverage**: which kernel defuns and globals a
real run entered, split into the boot (`shen.initialise`) phase and the
program phase by the record's third column. `reach` strictly containing
`called` is expected and is reported, never failed.

## Level 3: static inclusion (a graph extractor, of which SCIP is one)

The claim is: the artifact's call graph, restricted to what is reachable
from its entry, has no node the rules did not derive, and every kept
function was compiled the same way in A and A*. Stage 5 established this
for shen-go by recovering the KL-level graph from the generated Go, after
finding that the Go-level SCIP graph is four driver functions.

The port-agnostic contract is an **extractor**: a command declared per
target that, given a built artifact directory, writes three fact files in
the KL namespace:

```
node(F)          F is bound as a function in the artifact
edge(F, G)       F's compiled body resolves a call to G (direct or lookup)
body(F, H)       H is a hash of F's compiled body, normalised
```

Yggdrasil then runs one Datalog query, the same for every port: the
main-reachable closure of `edge` from the entry nodes, `missing(F) :-
artreach(F), !reach(F), !special(F), F != shen.initialise`, and
`differs(F)` where the two artifacts' `body` hashes disagree.

How a port implements the extractor is its business:

| port kind | extractor | example |
|---|---|---|
| compiles to a language with a SCIP indexer, direct calls | SCIP index, occurrences inside definition ranges | a hypothetical direct-call Go or Rust backend |
| compiles to a language, lookup calls | pattern the generated code for binding and lookup sites | shen-go today (`scip.go` does this from the Go source) |
| compiles to a language without an indexer | the language's own parser (go/ast, esprima, Lua's `luac -l`) | shen-lua, ShenScript |
| interpreter, artifact is the KL slice | identity: `node`/`edge` from `kernel.kl` itself | shen-swift, the `kl` runner |
| whole-program optimising compiler | not compositional; `body` is meaningless, only `node`/`edge` are compared, and the report says so | SBCL, Chez |

SCIP is therefore the *first row*, not the contract. What is fixed is the
three relations and the query.

**Delta accounting.** The extractor's reachable set will not equal `reach`
exactly on any real port, and the contract requires the difference to be
explained by declared facts, never waved through: `shen.initialise` (the
driver calls it), `special_forms` (never looked up), `native_overrides`
(bound natively, no KL body). Anything else in the delta is a finding.

## Level 4: behavioural parity (exists today)

`yggdrasil parity`: same slice, every target, output diffed against a
golden and against itself across two boots and two passes. This is the
only level that observes the L runtime rather than the artifact's text,
and the only one that can catch integer width, hash order, and
memoisation drift. It stays the top rung.

## The conformance report

One command, `yggdrasil conformance --target P`, runs what the port's
declaration permits and prints one line per obligation:

```
yggdrasil-conformance: target=go
level0 builder            ok
level1 port_reads         verified   (5 entries, kl/primitives.go, kl/kernelfast.go)
level1 port_writes        declared   (2 entries)
level1 special_forms      declared   (do)
level1 native_overrides   verified   (12 entries, native_deps 3 edges added)
level1 dispatch           slice
level2 trace              ok         (4 fixtures, uncoveredCall empty, 3 natives excluded)
level3 extractor          ok         (graph recovery; delta = shen.initialise, do)
level4 parity             ok         (6 goldens)
```

with `unsupported`, `unverified`, or `vacuous` where the declaration says
so. This table, per port, is the honest form of "the guarantees survive
translation": a list of which rungs were climbed and which were declared
away. It is also the artifact to point the Shen group at when the question
is "what does Yggdrasil add to the core of trust for port P": exactly the
declarations marked `declared` rather than `verified`, and nothing else.

## What this does not solve

- **Backend correctness.** Level 3 checks that B_P compiled the same
  functions the same way in A and A*. It says nothing about whether B_P
  compiles KL correctly; that is the port's kernel test suite and Fetzer's
  core of trust, and it is the same for A and A*.
- **Computed names.** A program that builds a function name at runtime
  escapes every level here except 4. The `computed-names=` manifest line
  is the precondition the whole ladder rests on, and a port's conformance
  is conditional on it being `none`.
- **Resource behaviour.** Nothing above speaks to time or memory, as the
  thread already conceded.

## Staging

1. Move `*global-primitives*` into `builders.json` as `port_writes`; add
   `special_forms`, `native_overrides`, `native_deps`, `call_style`,
   `dispatch` for shen-go by reading its source; conservative or
   `unverified` for the rest. Feed `native_deps` into the edge facts.
   Byte-identical output on every fixture unless a `native_deps` edge is
   real, in which case the footprint was wrong before and the doc records
   the change.
2. Generalise `scip-check` into `graph-check` behind the extractor
   contract: shen-go's recovery becomes one extractor, the `kl` runner's
   identity extractor a second, and the query moves into `analysis.dl`
   and `*shake-rules*` so all three engines run it.
3. `yggdrasil conformance --target P` composing levels 0 to 4 into the
   table above, with `TestConformanceGo` asserting every row for go.
4. Take a second port through the ladder. shen-lua is the candidate: a
   different call style, no SCIP, a parser available (`luac -l`), and a
   working sibling builder. Two ports conforming is the point at which the
   contract has been tested rather than designed.
