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
verdict, which is why each fact carries its provenance alongside it, as
two sibling keys:

| key | meaning |
|---|---|
| `<fact>_source` | the file and function the fact was read off, or `none: <why not>` |
| `<fact>_checked_by` | the test that fails when the fact drifts, or `none` |

There is deliberately no `_verified` boolean. There used to be two, and
they spelled one word over two unrelated predicates:
`port_reads_verified` meant "each entry was read from a named source
line", `native_overrides_verified` meant "the list equals the symbols
`InstallKernelFast` rebinds" -- which says nothing about whether any
native implements its KL twin. A fact whose `_checked_by` is `none` is
**declared**, never verified; `verified` means a named test, and nothing
else. (The flag's emptiness was not hypothetical: `native_overrides`
carried `_verified: true` while being four symbols short of what
`InstallKernelFast` actually rebinds. Nothing read the key, so nothing
could contradict it.)

Facts a target does not state are inherited from the `_default` block,
and a target that states none is a target whose runtime nobody has
measured -- the missing key is how that looks. Of the level-1 keys below,
two exist; four are new.

| key | meaning | consumed by | exists |
|---|---|---|---|
| `port_reads` | globals the runtime reads natively | `liveGlobal` (dead-init) | yes, verified for go only |
| `port_writes` | globals the runtime binds before `shen.initialise` runs | `readBeforeWrite`, `uncoveredRead` | today hardcoded as `*stinput*`/`*stoutput*` in `*global-primitives*`; should move here |
| `special_forms` | KL names the port's compiler lowers without a lookup (`do` on shen-go) | level-3 delta accounting: a kept defun that is never entered | new |
| `native_overrides` | kernel defuns the port replaces with natives (`InstallKernelFast` on shen-go, `overrides.scm` on shen-scheme), plus `native_overrides_installed_after`: the boot phase after which the swap happens | see the obligation below | yes, for `go` |
| `native_deps` | for each override, the kernel names its native implementation calls back into | added as `edge(F, G)` facts | new |
| `call_style` | `direct`, `lookup`, or `mixed`: how a compiled call resolves | decides whether level 3 is static inclusion or graph recovery | new |

**Obligation N (native overrides).** The footprint is computed from the
kernel's KL. If the port swaps a defun for a native, the native's callees
are invisible to the rules. So: a native override may call back into the
kernel only through names listed in `native_deps`, and Yggdrasil adds those
as edges before reachability. A port that cannot enumerate them leaves
`native_deps` undeclared and `native_overrides_checked_by` at `none`, and
the contract report prints `declared`, not `verified`, which is the form
"the footprint is unsound for that port at level 1" takes in the table.
shen-go's list is readable from `kl/kernelfast.go`; shen-scheme's from
`overrides.scm`.

**The phase is half the fact.** A list of overridden defuns says nothing
until it says *when* the swap happens, because the two readings differ on
whether the kernel's KL ever runs. On shen-go it runs: the generated
`main` calls `shen.initialise` before `runHelper("InstallKernelFast",
...)`, so every override's KL body executes during boot and is replaced
only afterwards -- measured, not assumed. `yggdrasil trace-check
tests/fib.shen OUT --target go` (shen-go da55c5d) reports `OK called=34
reach=53`, and of the 18 native-overridden functions in that slice's kept
kernel defuns, **11** recorded a KL entry in `called.facts`: `<-vector`,
`empty?`, `fail`, `hdstr`, `limit`, `map`, `put`, `reverse`,
`shen.+string?`, `vector`, `vector->`. Every one of those entries happened
before `InstallKernelFast` ran. So
`native_overrides_installed_after: "shen.initialise"` is part of the
declaration, and the contract report prints it on the same line. An
override list with no phase would read as "these KL bodies never run",
which is false, and pruning on that reading would delete initialisation
the boot depends on.

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
  opened with `open` reach the file by process exit, without `close`.
  shen-go does this. A port that buffers must either flush at exit or
  declare `trace_flush: close-required`, in which case the weaver emits a
  `close` as the last user toplevel form and the trace is valid only for
  programs that terminate normally. A port declaring neither is reported
  `trace: unsupported`, not passed.
- **Obligation E (entry).** Every kept defun is entered through its KL
  body, so the woven `(ygg.traced F)` runs -- *while* that body is the
  binding. A native override (Obligation N) changes the binding, and
  therefore Obligation E is a claim about a phase, not about a function:
  before the port installs its natives the KL body is what runs and the
  name records itself; after, the native runs and it does not. Which half
  a given entry falls in is what `native_overrides_installed_after`
  states, and on shen-go it is `shen.initialise`, i.e. the whole boot is
  in the first half. Measured on `tests/fib.shen` (shen-go da55c5d): of
  the 18 native-overridden functions among that slice's kept kernel
  defuns, 11 appear in `called.facts` -- so the reading that a native
  override "never records itself" is false for that port, for every entry
  the boot made, and an absence is not evidence either way.

  What this means for the query below: a name missing from `called` may be
  an override entered only after installation, and a name present may be
  an override entered before it. Neither is a finding on its own. The
  honest report is per-port and phase-aware, and Yggdrasil does not write
  it yet: nothing today excludes override names from `called`, and no
  report counts them. A port whose overrides are installed *before* its
  user program runs (the reading the old text assumed) can subtract them
  and say how many; shen-go cannot, because for shen-go the set is neither
  all of them nor none.

The check is `uncoveredCall(F) :- called(F), kernel(F), !reach(F)` empty,
per run, per port. It is the one piece of evidence that is identical in
form on every target, which is why it is the floor of the ladder above
level 0, not SCIP.

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

## The contract report

`yggdrasil contract --target P` prints one line per level-1 obligation,
with where the fact came from and what checks it. Levels 2 to 4 are
commands that run things rather than declarations to read, so the report
names them and says it did not run them, rather than printing a row that
could only ever say `ok`:

```
yggdrasil-contract: target=go
  verified = a named test fails when the fact drifts; declared = stated, nothing checks it; unknown = not declared
level0  builder            ok         runs on shen-go, 3 build steps, needs go
level1  port_reads         verified   5 entries
                                      source:     shen-go da55c5d kl/primitives.go (*stinput*, *stoutput*), ...
                                      checked_by: TestPruneInitGoArtifactStillRuns (prune_test.go) and the parity gate: ...
level1  port_writes        unknown    not declared in builders.json
level1  special_forms      unknown    not declared in builders.json
level1  native_overrides   verified   58 entries, installed_after=shen.initialise
                                      source:     shen-go da55c5d kl/kernelfast.go InstallKernelFast: ...
                                      checked_by: TestNativeOverridesMatchKernelFast (prune_test.go): ...
                                      phase:      the natives replace these KL bodies only after shen.initialise, so whatever the boot reached before that point ran as KL
level1  native_deps        unknown    not declared in builders.json
level1  call_style         unknown    not declared in builders.json
level1  dispatch           unknown    not declared in builders.json
level2  trace              not-run    yggdrasil trace-check
level3  extractor          not-run    yggdrasil scip-check (go only)
level4  parity             not-run    yggdrasil parity
```

Three statuses, and `unknown` is the important one: an undeclared key gets
a row saying so rather than being omitted, because "nobody measured this"
and "this port has none" are different claims and only the first is true.
Four of the six level-1 keys are `unknown` on every target today, and the
report is where that is visible. A target with no `port_reads` of its own
reports `declared` with an `inherited: builders.json _default` line -- the
conservative placeholder, named as one.

This table, per port, is the honest form of "the guarantees survive
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
3. Extend `yggdrasil contract --target P` from reading the level-1
   declarations (which it does today) to composing levels 0 to 4 by
   running them, with a test asserting every row for go.
4. Take a second port through the ladder. shen-lua is the candidate: a
   different call style, no SCIP, a parser available (`luac -l`), and a
   working sibling builder. Two ports conforming is the point at which the
   contract has been tested rather than designed.
