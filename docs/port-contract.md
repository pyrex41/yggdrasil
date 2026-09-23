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
| `<fact>_checked_by` | the test that fails when the fact drifts, or `none`, or `none: <why nothing checks it>` -- which reads as `none` (`factChecked`), so explaining yourself cannot promote a row to `verified` |

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
measured -- the missing key is how that looks.

### `unknown` is a value

For `port_reads` the missing key was not enough, because `_default`
carried a list: a 35-name conservative guess that every unmeasured target
inherited, so "nobody looked" and "somebody measured this" arrived at
every consumer in the same shape. `_default.port_reads` is now the literal
string

```json
"port_reads": "unknown"
```

with a `port_reads_source` that says why, and that value is first-class
everywhere downstream (hickey-14):

* **`prune.go`** resolves it to `(nil, false)`. `--prune-init --target T`
  on such a target is **refused**, naming the target and saying that
  nobody has read what its runtime reads natively. `--prune-init-unverified`
  overrides it and prunes against the union below, with a `WARN`.
* **a target-agnostic `--prune-init`** (no `--target`) is **refused** on
  the same grounds while any target is unknown. The union it would prune
  against is over the targets that **have** declared a list -- today `go`
  alone, five globals -- and a shake with no `--target` is by definition a
  slice that may be built for one of the others, where a global the
  initialiser no longer writes is a run-time failure far from this flag.
  The older claim, "the union over every builder is sound for any of
  them", was true of a union of guesses; the union of the measurements is
  *smaller*, so it prunes more. The refusal names both halves -- the
  targets the union is over, and the targets it says nothing about -- and
  `--prune-init-unverified` proceeds with the same two halves as a `WARN`.
* **`yggdrasil contract --target T`** prints
  `level1  port_reads  unknown  unknown: nobody has read this port's native
  global reads off its runtime`, with the `_default` source under it and a
  line saying what unknown costs. `unknown` in that report now means "not
  declared, **or** declared as the literal `unknown`".
* **`yggdrasil.shen`'s `(set ygg.*port-reads* ...)`** -- the copy a direct
  host invocation uses when there is no Go driver to push a list in -- is
  the same union of declared lists, pinned element for element by
  `TestPortReadsDefaultMatchesShen`. It is no longer a conservative
  superset for an unmeasured port, and its comment says so.

Any string other than `unknown` in a `port_reads` value is a parse error
(`TestPortReadsRejectsAnyOtherString`): a typo must not be able to turn a
declaration into a silence, which is the failure this value exists to make
visible.

### Level 1, as it exists: three keys

Each has at least one consumer in the code and appears in
`builders.json` (checked against Yggdrasil `3c499a1` with `grep -rn` over
`*.go`, `*.shen` and `*.json`):

| key | meaning | consumed by | status on `go` today |
|---|---|---|---|
| `port_reads` | globals the runtime reads natively | `liveGlobal` (dead-init) in `prune.go`, `yggdrasil.shen` and `main.go` | **verified** (5 entries, `TestPruneInitGoArtifactStillRuns`); every other target inherits `_default`'s `"unknown"` and reads **unknown** -- see above |
| `special_forms` | KL names the port's compiler lowers without a lookup (`do` on shen-go) | level-3 delta accounting in `scip.go`: a kept defun that is never looked up | **declared** (7 entries, each cited to a line of shen-go; `special_forms_checked_by` is `none`). Unknown on every other target |
| `native_overrides` | kernel defuns the port replaces with natives (`InstallKernelFast` on shen-go, `overrides.scm` on shen-scheme), plus `native_overrides_installed_after`: the boot phase after which the swap happens | `prune.go`, and the obligations below | **verified** (58 entries, `installed_after=shen.initialise`, `TestNativeOverridesMatchKernelFast`). Unknown on every other target |

### Level 1, proposed: four keys with a row and no data

The four keys below are **proposed**, and appear here in the present
tense nowhere else. None appears in `builders.json` for
any target; the only code that mentions them is `contract.go`, which
prints a row saying `unknown`, and `prune_test.go`, which asserts that it
does. Nothing consumes them, and the "consumed by" column is what they
would feed if they existed, not what reads them now.

| key | meaning | would be consumed by |
|---|---|---|
| `port_writes` | globals the runtime binds before `shen.initialise` runs | `readBeforeWrite`, `uncoveredRead`. Today these read `*global-primitives*`, hardcoded in `yggdrasil.shen` as `*stinput*`/`*stoutput*`; moving it here is step 1 of the staging plan below |
| `native_deps` | for each override, the kernel names its native implementation calls back into | `edge(F, G)` facts added before reachability |
| `call_style` | `direct`, `lookup`, or `mixed`: how a compiled call resolves | deciding whether level 3 is static inclusion or graph recovery. `scip.go` today hardcodes graph recovery and `--target` refuses anything but `go` |
| `dispatch` | `slice` or `full-kernel`: whether the artifact is the slice or links the whole kernel behind it | making every check above level 0 report vacuity. Today `trace.go` names it only in a comment |

An earlier draft of this note also proposed `trace_flush`. It is gone: the
weaver now appends `(ygg.trace-end)` unconditionally (Obligation F below),
so there is nothing for a port to declare. No file in the repository
mentions the key.

### Level 1, the run facts: `stdin` and `stdout`

These are facts about how a target's artifact is RUN rather than about its
runtime's bindings, and unlike the four above they have consumers today.
`_default` states the ordinary contract, so every target declares them by
inheritance and none is silent:

| key | values | consumed by |
|---|---|---|
| `stdin` | `delivered` (default): the artifact receives the caller's bytes. `appended-to-program`: the runtime reads its PROGRAM from stdin, so the caller's bytes land after it, as further toplevel forms | `evidencePossible` in `trace.go` (a fixture stdin on such a target is the named skip `stdin-appended-to-program`, never a pass) and `cmdParity` (the target is skipped, by fact, when `--stdin` is given) |
| `stdout` | `program` (default): stdout is the program's output. `repl-transcript`: the program's output is embedded in the runtime's own prompts, echoes and diagnostics | `checkGolden` in `trace.go` and `compareParity` in `main.go`: the golden is compared by **containment** rather than equality, on every leg, because such a transcript is not byte-stable even across two boots of the same program |
| `transcript_error_markers` | substrings that, appearing anywhere in a transcript, mean the run went wrong (`kl`: `Recovered in Eval`, `Panic:`, `goroutine `) | the same two, **before** they compare anything: a transcript carrying one FAILS, naming the marker and the first line that matched. Required of any target declaring `stdout: repl-transcript` -- containment alone is satisfied by a run whose boot panicked and carried on, which is what `kl` does today |

An empty golden is a FAIL of its own (`golden-empty`) on the containment
path, for the same reason: `strings.Contains(anything, "")` is true, so an
empty `tests/<fixture>.expected` would report a target as checked that was
never checked at all.

`kl` is the only target declaring the non-default value of either, and
those two facts are the whole of what used to be a runner special-cased by
name inside `trace.go` (hickey-13). A companion recipe key,
`program_file`, names the file such a run feeds to stdin first; it is a
recipe key rather than a fact (no `_source`), so `yggdrasil contract` does
not list it as a declaration.

**Obligation N (native overrides).** The footprint is computed from the
kernel's KL. If the port swaps a defun for a native, the native's callees
are invisible to the rules. The *proposed* discharge is: a native override
may call back into the kernel only through names listed in `native_deps`,
and Yggdrasil adds those as edges before reachability.

None of that exists. `native_deps` is undeclared on every target and
nothing would read it if it were declared, so the obligation is open for
every port including `go`, whose 58 overrides are enumerated and checked
(`native_overrides` is `verified`) but whose callees are not. What the
report can say today is `native_deps: unknown`, which is the honest form
of "nobody has looked". shen-go's list would be readable from
`kl/kernelfast.go`; shen-scheme's from `overrides.scm`.

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
its own kernel first) is not running the shaken program and *would* say
so: `dispatch: full-kernel`. Every check above level 0 is vacuous for such
a build.

This too is proposed. No target declares `dispatch`, nothing branches on
it, and no check reports vacuity; `trace.go` names the value in a comment
explaining where the containment query would have teeth. The report prints
`unknown`, which is accurate and is all it can be.

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
  body, so the woven `(ygg.traced F)` runs -- *while* that body is the
  binding. A native override (Obligation N) changes the binding, and
  therefore Obligation E is a claim about a phase, not about a function:
  before the port installs its natives the KL body is what runs and the
  name records itself; after, the native runs and it does not. Which half
  a given entry falls in is what `native_overrides_installed_after`
  states, and on shen-go it is `shen.initialise`, i.e. the whole boot is
  in the first half. Measured at Yggdrasil `3c499a1`, shen-go `da55c5d`,
  host shen-cl, on `tests/fib.shen --target go`: of the 18
  native-overridden functions among that slice's 54 kept kernel defuns, 11
  appear in `called.facts` -- so the reading that a native override "never
  records itself" is false for that port, for every entry the boot made,
  and an absence is not evidence either way.

  The trace now carries the phase itself, so this is readable off the
  artifact rather than inferred from shen-go's source: every record's
  third column is `b` while `shen.initialise` runs and `p` afterwards,
  and `trace-check` prints `phase: boot=N program=M`. On `fib --target go`
  that line reads `boot=29 program=9`, and **all 11** of the override
  entries above are tagged `b`. Not one override is entered in the program
  phase, which is exactly what `installed_after: shen.initialise`
  predicts. On `--target kl` the same run reports
  `phase: DEGENERATE ... every record is tagged boot` -- shen-go's
  `cmd/kl` never executes the woven flip -- and `trace-check` says so
  rather than reporting `called-program=0` as a finding.

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
| interpreter, artifact is the KL slice | identity: `node`/`edge` from `kernel.kl` itself | shen-swift, the `kl` target |
| whole-program optimising compiler | not compositional; `body` is meaningless, only `node`/`edge` are compared, and the report says so | SBCL, Chez |

SCIP is therefore the *first row*, not the contract. What is fixed is the
three relations and the query.

**Delta accounting.** The extractor's reachable set will not equal `reach`
exactly on any real port, and the contract requires the difference to be
explained by declared facts, never waved through. `scip.go` subtracts
exactly three causes and there is no fourth: `shen.initialise` (the
driver's generated `main` calls it), the user's own defuns (they live
outside `kernel.kl`), and the target's declared `special_forms` together
with whatever only they reach. Anything else in the delta is printed by
name and fails.

`native_overrides` is deliberately *not* a subtraction here, and the
earlier draft that listed it was wrong on the reason as well as the fact:
a native override's KL body is still emitted by the backend and is still a
node in the recovered graph. Whether it is ever *entered* is Obligation E
below, which is a level-2 question about a phase, not a level-3 question
about a graph.

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

Run against a sibling shen-go at `da55c5d`, with the long `source:` and
`checked_by:` strings elided at `...`:

```
yggdrasil-contract: target=go
  verified = checked_by names a test; read it for what the test catches and in which direction
  declared = stated, nothing checks it
  unknown = not declared, or declared as the literal "unknown" -- nobody measured it; read the source for why
level0  builder            ok         runs on shen-go, 3 build steps, needs go
level1  port_reads         verified   5 entries
                                      source:     shen-go da55c5d kl/primitives.go (*stinput*, *stoutput*), ...
                                      checked_by: TestPruneInitGoArtifactStillRuns (prune_test.go) and the parity gate: ...
level1  port_writes        unknown    not declared in builders.json
level1  special_forms      declared   7 entries
                                      source:     shen-go da55c5d, derived as a class and not from a fixture's residue. ...
                                      checked_by: none
level1  native_overrides   verified   58 entries, installed_after=shen.initialise
                                      source:     shen-go da55c5d kl/kernelfast.go InstallKernelFast: ...
                                      checked_by: TestNativeOverridesMatchKernelFast (prune_test.go): ...
                                      phase:      the natives replace these KL bodies only after shen.initialise, so whatever the boot reached before that point ran as KL
                                      phase src:  shen-go da55c5d cmd/yggdrasil-build/main.go genMain: ...
level1  native_deps        unknown    not declared in builders.json
level1  call_style         unknown    not declared in builders.json
level1  dispatch           unknown    not declared in builders.json
level1  stdin              declared   delivered
                                      inherited: builders.json _default (this target declares none of its own)
                                      source:     the default run contract: ...
                                      checked_by: none: scripts/parity-gate.sh would catch a port that broke it, but CI gates three targets and skips any whose toolchain is absent
level1  stdout             declared   program
                                      inherited: builders.json _default (this target declares none of its own)
                                      source:     the default run contract: ...
                                      checked_by: none: ... (as above)
level2  trace              not-run    yggdrasil trace-check
level3  extractor          not-run    yggdrasil scip-check (go only)
level4  parity             not-run    yggdrasil parity
```

The same report on a target nobody has measured -- every target but `go`
-- differs in one row, and that row is the point of `unknown` being a
value:

```
yggdrasil-contract: target=lua
level1  port_reads         unknown    unknown: nobody has read this port's native global reads off its runtime
                                      inherited: builders.json _default (this target declares none of its own)
                                      source:     none: nobody has read this port's native global reads off its runtime. The previous value here was a conservative guess ...
                                      checked_by: none
                                      consequence: --prune-init --target <this target> is refused (prune.go); so is a target-agnostic --prune-init, whose union is over the targets that HAVE declared a list
```

and on `kl`, whose two run facts are the whole of what used to be a
special case in `trace.go`:

```
yggdrasil-contract: target=kl
level0  builder            ok         runs on shen-go cmd/kl, 2 build steps, needs go
level1  stdin              verified   appended-to-program
                                      source:     shen-go cmd/kl/main.go: the VM reads its program from os.Stdin and takes no file argument ...
                                      checked_by: TestStdinFactDrivesTheSkip and TestEvidencePossible (trace_test.go): ...
level1  stdout             verified   repl-transcript
                                      source:     shen-go cmd/kl/main.go: the VM prints a numbered prompt and echoes each toplevel form's value ...
                                      checked_by: TestCheckGolden (trace_test.go, the kl-containment subtest) and TestTranscriptTargetIsComparedByContainment (parity_test.go): ...
level1  transcript_error_markers verified   3 entries (Recovered in Eval, Panic:, goroutine )
                                      source:     shen-go cmd/kl/main.go and kl/eval.go Eval.func1: the VM recovers a panic, prints it, and CARRIES ON ...
                                      checked_by: TestTranscriptErrorMarkersFailBeforeContainment (trace_test.go) and TestTranscriptErrorMarkerFailsParity (parity_test.go): ...
```

Three statuses, and `unknown` is the important one: an undeclared key gets
a row saying so rather than being omitted, because "nobody measured this"
and "this port has none" are different claims and only the first is true.
Four of the seven level-1 keys are `unknown` on **every** target today --
the four marked proposed above -- and the report is where that is visible.
`special_forms` shows the other axis: it is declared, sourced line by line
to shen-go, and `checked_by: none`, so the word `verified` is withheld --
even though `TestGoBuilderDeclaresSpecialForms` pins all seven names and
requires the source to cite a line of the port for each, and `scip-check`
fails loudly if the declaration is wrong for a fixture it runs. Naming
either in `special_forms_checked_by` would make the row `verified`
honestly; until someone does, the report says what `builders.json` says. A target with no `port_reads` of its own reports
`unknown` with an `inherited: builders.json _default` line, the source
saying why nobody measured it, and a `consequence:` line saying what
unknown costs. `TestContractReportNamesSourceAndPhase`
and `TestContractLegendPromisesNoMoreThanCheckedBySays` in `prune_test.go`
are what fail if any of that drifts.

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
   contract: shen-go's recovery becomes one extractor, the `kl` target's
   identity extractor a second, and the query moves into `analysis.dl`
   and `*shake-rules*` so all three rule evaluators run it (two
   transcriptions and one independent engine -- `verification-guide.md`
   section 6).
3. Extend `yggdrasil contract --target P` from reading the level-1
   declarations (which it does today) to composing levels 0 to 4 by
   running them, with a test asserting every row for go.
4. Take a second port through the ladder. shen-lua is the candidate: a
   different call style, no SCIP, a parser available (`luac -l`), and a
   working sibling builder. Two ports conforming is the point at which the
   contract has been tested rather than designed.
