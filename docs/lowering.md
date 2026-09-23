# Port-specific lowering: the gate, the measurement, and the parity

**Status**: the gate OPENS on `go` as of shen-go `30ab469`, the pass has been
run for real on three fixtures, and the three-way parity **FAILS** on every one
of them for a reason the gate does not model. See "The gate, which now opens on
go" and "The parity, actually run, and what it found". (Yggdrasil, 2026-09-23)
**Builds on**: [port-contract.md](port-contract.md) (`native_overrides` and its
phase), [analysis-rules.md](analysis-rules.md) (the rule set),
[verification-guide.md](verification-guide.md) section 12
**Commands**: `yggdrasil lower PROG OUTDIR --target T [--report-only]`,
`yggdrasil lower-check PROG OUTDIR --target T`
**Code**: `lower.go`, `lower_test.go`

Every port already rewrites kernel functions into natives, invisibly, at boot.
This note is about making that rewriting an explicit stage that the shake, the
manifest and the checks can see — and about what the evidence for it is, which
is not what an earlier draft of this note claimed.

## What lowering is here

A port declares `native_overrides`: the kernel defuns it replaces with native
code. On shen-go the replacement is a **rebinding under the same name** —
`InstallKernelFast` binds the symbol `reverse` to a Go function called
`nativeReverse`. Every call site already resolves to the native; no call site
mentions `nativeReverse`.

So on such a port the substitution has already happened, and the only thing
left for Yggdrasil to do is stop shipping the KL body that the rebinding
throws away. **Lowering F is deleting F's `defun` from `kernel.kl`.** No form
is rewritten, no call site moves, no name is introduced. That is the entire
pass, which is why its correctness check is one line (`badLowering`, below)
and why the test for it is a byte comparison of the remainder.

That paragraph carries an assumption which turns out to be false on shen-go,
and the rest of this note is largely about it: it assumes the rebinding is
**unconditional**. shen-go's `InstallKernelFast` rebinds F only if the kernel
already bound F, so the `defun` is not text the native replaces — it is the
trigger for the native being installed at all. See "The parity, actually run,
and what it found".

A port that lowered by *renaming* — rewriting `(F a1 .. an)` to `(P a1 .. an)`
for a port primitive `P` with a different name — would need the same table and
the same check, with the substitution in place of the deletion. Nothing here
assumes the rebinding shape except the pass itself; the gate, the table, the
rule and the parity arrangement are the same either way.

## The gate, which now opens on go

A list of overridden defuns says nothing until it says **when** the swap
happens. `builders.json` records that as
`native_overrides_installed_after`, and on `go` it now reads
`"before-initialise"`, sourced to shen-go `30ab469`'s generated `main`:

> the kernel chunk loop at `cmd/yggdrasil-build/main.go:619-622` emits
> `runHelper("InstallKernelFast", InstallKernelFast)` at `main.go:621`, INSIDE
> the loop and so after every kernel chunk, while
> `run(&e, "shen.initialise", ...)` is emitted later, at `main.go:640-642`.

Up to shen-go `da55c5d` it read `"shen.initialise"` and the pass refused: the
initialiser ran while the overridden functions were still their KL bodies, so
those bodies were live code during boot and deleting them would have broken
the boot rather than shrinking it. `trace-check` measured it rather than
arguing it, and it measures the change the same way. On `tests/fib.shen`,
target `go`, host shen-cl:

| | at shen-go `da55c5d` | at shen-go `30ab469` |
|---|---|---|
| `trace-check` sentinel | `OK called=34` | `OK called=11` |
| phase line | `boot=29 program=9` | `boot=2 program=9` |
| of fib's 18 native-overridden kept defuns, how many record a KL entry | 11, every one tagged `b` | **0** |
| what the boot phase contains | 29 kernel defuns the initialiser entered as KL | `shen.initialise`, `shen.initialise-arity-table` |

The `30ab469` column was taken on this branch on 2026-09-23 against
a sibling shen-go checkout at `30ab4692fc5a2750ee61845cb1bdc96d60f4aca8`,
the SHA in `.github/shen-go.ref`; `called.facts` in the run's facts directory
lists eleven names and none of the eighteen is among them.

So the gate — permitted only when the phase is `none` or `before-initialise` —
opens, and `yggdrasil lower --target go` runs:

```
$ yggdrasil lower tests/fib.shen out --target go
yggdrasil-lower: OK target=go dropped=18 kept-kernel-defuns=36
  canonical (artifact of record, untouched): out
  lowered:                                   out/lowered
  report:                                    out/lowered/yggdrasil.lowering.txt
```

`go` is still the only target that declares `native_overrides` at all, so
every other target refuses for the other reason — `no-native-overrides`, which
the message is careful to call an undeclared key rather than a claim that the
port overrides nothing. A refused gate exits 1 and writes no `lowered/`
directory: a partial one is a directory the next command would read.

The gate has four refusals and they are different facts. Each says what to do
about itself, and only the first one is about moving an install:

| reason | what it means |
|---|---|
| `natives-installed-after-initialise` | the phase is declared and is not early enough; `go` up to shen-go `da55c5d`, no shipped target today |
| `native-install-phase-undeclared` | a list with no phase. Refused rather than read optimistically, because the optimistic reading is the one that deletes boot code |
| `native-overrides-unchecked` | `native_overrides_checked_by` is `none`. The list is declared and nothing re-derives it from the port's source |
| `no-native-overrides` | the target declares no list. Nobody measured it; that is not "there are none" |

The third is there because of what happened to `go`'s own list. It went four
symbols short of what `InstallKernelFast` rebinds — `<-vector`, `==`, `@p`,
`shen.hds=?` — while carrying a `native_overrides_verified: true` that nothing
read and so nothing could contradict. A list nothing checks is a list that can
drift without being told, and lowering deletes code on its say-so. The two
accepted phase spellings are the two that appear in `builders.json` and
`port-contract.md`, and no others: a gate that accepts a spelling no
declaration uses accepts a typo as a permission.

shen-go moved `InstallKernelFast` ahead of `shen.initialise` in PR #48
(`30ab469`) — the port's change, not Yggdrasil's — `builders.json`'s phase
changed to match, and the same code ran with no edit in `lower.go`.
`TestLowerGateRefusesGoToday` was the test that failed on the day it did,
which is how the change got noticed rather than silently taken; it is now
`TestLowerGateOpensForGo` and asserts the other direction, while the refusal
branches stay covered by `TestLowerGateRefusesUndeclaredPhase`,
`TestLowerGateRefusesAnUncheckedOverrideList`,
`TestLowerSliceRefusesAndWritesNothing` and
`TestRefusalLastLineIsReasonSpecific` on hand-built declarations rather than on
whatever a shipped target happens to say today.

## The measurement, which is never refused

The number this note quotes has to be computable on the port as it is today,
so `--report-only` skips the gate entirely: it reports a fact about the slice
and the declaration, true whether or not the pass is permitted.

```
$ yggdrasil lower tests/fib.shen out --target go --report-only
yggdrasil-lower: report-only target=go
  kept-kernel-defuns=54 native-overridden=18 declared=58
  names: <-vector,arity,assoc,concat,empty?,fail,fn,get,hdstr,limit,map,not,put,reverse,shen.+string?,vector,vector->,vector?
  installed-after=before-initialise lowering=permitted
```

**18 of 54** — a third of an eval-free slice's kernel defuns are functions
shen-go replaces natively. Re-taken on this branch 2026-09-23, shen-go
`30ab469`, host shen-cl; the count is unchanged from the Yggdrasil `4c791e3` /
shen-go `da55c5d` reading, and only the last line moved
(`refused(natives-installed-after-initialise)` to `permitted`). `TestLowerReportOnlyCountsAgainstBuildersJSON` re-derives that
number from `builders.json` and the shaken `kernel.kl` on every run with a
host, so it is not a figure in a document that nothing re-takes; a literal in
the test would have been exactly that.

Two readings of the 18 were both wrong and the phase is what separated them:
in the **boot** phase none of the 18 was native, in the **program** phase all
18 were. At shen-go `30ab469` the distinction is gone on `go` — all 18 are
native from the first kernel chunk on, and none of them records a KL entry in
either phase — but it is the general shape of the question a port's phase
declaration answers, and it comes straight back on any port that installs
later. For the eval-capable fixture
`tests/partial-eval.shen` an earlier run of this note recorded 49 of 549 kept
defuns; that figure is carried forward from Yggdrasil `3c499a1` and nothing
re-takes it, and the phase split there has never been measured. Re-take it with
`--report-only` before quoting it.

The gap this note exists to close is the program phase: the shake keeps a KL
body nothing will enter, the weave instruments it, and the level-3 graph
recovery sees a binding that is dead on arrival.

## The table, the rule, and what checks the pass

Two relations, both syntactic:

```
equiv(F)     kernel function F is implemented natively by this port
             (builders.json: <target>.native_overrides)
lowered(F)   the pass deleted F's defun from this slice
```

and one rule, which is the whole correctness check of the pass:

```
badLowering(F) :- lowered(F), !equiv(F).
```

`badLowering` must be empty. It is evaluated in Go (`badLowering` in
`lower.go`) **before anything is written** — a pass that emits the artifact
and then reports on it has a log line, not a check — and
`TestBadLoweringIsTheRule` feeds it a drop list the pass would not produce,
because a rule that only ever sees conforming input is a rule nothing has
evaluated. There is no new engine for one negated join.

The pass is also held to "deletion, never rewriting" at the byte level.
`TestLowerSliceDropsExactlyTheNamedDefuns` asserts that the lowered
`kernel.kl` is the original with whole lines removed, in order, and that every
removed run of non-blank lines opens a defun that was named for deletion. Every
surviving byte is compared. The splitter is string-aware, because `kernel.kl`
carries string literals containing newlines and unbalanced parens (the vector
accessors' error messages), and a line-based pass would cut one of those forms
in half.

## The evidence, which is parity and not a lemma

An earlier draft of this note argued the composed artifact equivalent by
**transitivity**: shake preserves the program, each substitution preserves it
because its row has a differential test, therefore the composition preserves
it. Both words are gone from the design, and it is worth saying why rather
than quietly dropping them.

The middle step was not a lemma. It was a differential test on a sample: run
F's KL body and the native over some inputs and diff. That establishes
agreement on the inputs tried, which is worth having and is not a proof. Worse
for the composition, the test runs F's body on a **full kernel VM**, while the
pass applies inside a **shaken** program whose property vector, lambda table
and arity table have been trimmed to the footprint and whose `shen.f-error` has
been replaced. A row can hold on the first and not on the second. Transitivity
through a link like that is only as strong as the link, so chaining it added
confidence the links did not have.

What actually supports the lowered slice is narrower and checkable: **the
lowered slice, on the target port, answers what the canonical slice answers on
the same port.** That is a property of the artifact that ships, measured in the
environment it ships into, and it is what `yggdrasil lower-check` runs:

```
canonical slice on the reference host   vs tests/PROG.expected
canonical slice on target T             vs the above
lowered   slice on target T             vs the above
```

Three runs, because the middle comparison localises a failure. The first two
are the existing parity arrangement (`yggdrasil parity`, section 10 of the
verification guide) and are unchanged; lowering adds only the third. The
third differing from the second is a wrong `equiv` row or a wrong deletion,
and the sentinel names the pair:

```
yggdrasil-lower-check: OK target=T reference=lisp dropped=N golden=fib.expected
yggdrasil-lower-check: FAIL pair=canonical-target-vs-lowered-target target=T dropped=N
```

When the gate refuses, the check does not pretend to have run:

```
$ yggdrasil lower-check tests/fib.shen out --target <a target with no table>
yggdrasil-lower-check: SKIP reason=no-native-overrides target=...
  ... the refusal, in full ...
```

and exits **0**. The skip is a declared fact about the port, not an error, and
a gate script has to be able to tell it from a disagreement.

## The parity, actually run, and what it found

This section used to say the three-way parity "has never run on a lowering any
port actually declares". It has now. At shen-go `30ab469` the gate opens on
`go`, and the three fixtures below were lowered and checked for real on this
branch on 2026-09-23, host shen-cl, against
a sibling shen-go checkout at `30ab4692fc5a2750ee61845cb1bdc96d60f4aca8`.

**All three FAIL, and the FAIL is left standing.** The reference leg is the
default, `--reference lisp` (shen-cl); it never gets compared, because the
run that fails is the third one.

| fixture | `yggdrasil lower --target go` | `yggdrasil lower-check --target go` |
|---|---|---|
| `tests/fib.shen` | `yggdrasil-lower: OK target=go dropped=18 kept-kernel-defuns=36` | `yggdrasil-lower-check: FAIL pair=none run=lowered@go` |
| `tests/hello.shen` | `yggdrasil-lower: OK target=go dropped=18 kept-kernel-defuns=36` | `yggdrasil-lower-check: FAIL pair=none run=lowered@go` |
| `tests/prolog.shen` | `yggdrasil-lower: OK target=go dropped=20 kept-kernel-defuns=47` | `yggdrasil-lower-check: FAIL pair=none run=lowered@go` |

The lowered artifact compiles. It dies in its own initialiser, identically on
all three:

```
$ ./out/lowered/app-go/prog
yggdrasil: shen.initialise failed: variable vector not bound
```

`pair=none` is the sentinel's way of saying this is not a disagreement between
two outputs — there is no second output. The `lowered@go` leg never produced
one.

### Why: the phase is not the whole precondition

The declared fact is true. shen-go `30ab469` really does call
`InstallKernelFast` inside the kernel chunk loop, before `shen.initialise`, and
`trace-check`'s `boot=2` is that fact showing up in a measurement. What the
fact does not say — and what `lower.go`'s gate therefore does not model — is
that shen-go's overrides are **conditional on the kernel having defined the
name**. `kl/kernelfast.go:211` and `:218`:

```go
func overridePrimitive(name string, arity int, fn interface{}) {
	if kernelBound(name) == nil {
		return
	}
	BindSymbolFunc(MakeSymbol(name), canonicalOrMake(name, arity, fn))
}
```

`restoreCanonicalPrimitive` (`:185`) has the same guard. It is there for a good
reason — an eval-stripped kernel that never defined `empty?` must not acquire a
binding for it — but it means the KL `defun` is not dead text the native
replaces. It is the **trigger** for the native being installed at all.

So deleting `vector`'s defun from `kernel.kl` does not leave `vector` bound to
`primShenVector`. It leaves `vector` unbound, `overridePrimitive("vector", …)`
returns early, and `shen.initialise`'s
`(set *property-vector* (vector 20000))` compiles to
`Call(__e, PrimFunc(symvector), MakeNumber(20000))`, whose `PrimFunc` on an
unbound symbol panics with exactly the message above
(`kl/primitives.go:293-298`).

The design note's premise — "on such a port the substitution has already
happened, and the only thing left for Yggdrasil to do is stop shipping the KL
body" — holds only when the rebinding is **unconditional**. On shen-go it is
not, and no fact in `builders.json` records that, so the gate cannot see it.

### What was deliberately NOT done about it

- The gate was not widened, narrowed or bypassed. `lowerPhaseOK` still accepts
  exactly `none` and `before-initialise`.
- `lower-check` was not weakened. The lowered leg failing to run is still a
  `FAIL`, not a `SKIP` and not a warning.
- `native_overrides_installed_after` was not falsified back to
  `"shen.initialise"` to make the symptom go away. The phase fact is true; a
  false fact to buy a true verdict is the failure mode this whole file is
  about.

`TestLowerCheckOnGoFailsOnShenGoConditionalInstall` (`lower_test.go`) runs the
real, unfaked path and records the FAIL, two-sided: if `lower-check --target go`
ever passes, that test fails and demands this section be rewritten.

### What would fix it

One of, in the port or in the contract:

1. **shen-go installs unconditionally** for the names it lists, or exposes a
   second entry point that does. Then the KL body really is dead text and
   dropping it is safe.
2. **`builders.json` gains a fact for it** — something like
   `native_overrides_install_requires_kernel_binding: true` with its own
   `_source` and `_checked_by` — and `lower.go`'s gate reads it as a refusal
   reason of its own (`natives-installed-only-for-bound-names`). That is the
   honest encoding of what is true today: shen-go's table is a list of
   *accelerations*, not of *replacements*, and only a replacement can be
   lowered.

Option 2 is the smaller change and the more truthful one, but it is a new
declared fact about a port and should be written by whoever reads it off the
port, not inferred here.

### What the mechanism is still known to do

`TestLowerCheckThreeWayParityPassesAndFails` drives the same subcommand under a
**faked** `before-initialise` declaration whose table names no defun the slice
contains (so the lowered artifact is a byte copy) and gets an `OK`; then it
redefines `shen.app` in the lowered `kernel.kl` — an artifact that still builds
and boots and answers differently, which is the shape a wrong `equiv` row has —
and requires the verdict

```
yggdrasil-lower-check: FAIL pair=canonical-target-vs-lowered-target target=go dropped=0
  canonical@go: "fib 20 = 6765"
  lowered@go: "fib 20 = WRONG-LOWERING"
```

So the comparison is known to pass on an agreeing lowering and known to fail on
a disagreeing one, and — new as of `30ab469` — known to fail on a real lowering
that breaks the artifact. What has never been seen is a real lowering that
*works*.

The per-row differential tests are still worth writing, and the next section
says what they are. Their role is **localisation**: when parity fails, a row
whose test also fails says which substitution to look at. They are not the
argument; parity is.

The other half of the evidence is the port's own kernel test suite. A native
that disagrees with its KL twin is a bug in the port whether or not Yggdrasil
deletes the twin, and the port is where it is found. Lowering does not add a
correctness obligation to a port that rebinds under the same name — the
rebinding already made the native authoritative. It removes dead text.

## What a port has to do

This was previously a separate file, `lowering-shen-go-prompt.md`, written as
a brief for a session in the shen-go repository. It was a prompt committed as
documentation, which is not a thing this repository should carry; what a
reader needs from it is here, as prose, and the file is deleted.

A port that wants its lowering to be `verified` rather than `declared` exports
its table as data and tests it row by row. Concretely, for shen-go:

**The table, as data.** A `kl/equiv.json` generated by `go generate` or
written by a test that fails when it drifts from `InstallKernelFast`, with one
row per rebound symbol: the kernel function, the native's name, the arity
(which must match the kernel's arity table), whether the row is verified, the
number of differential cases run, the source line, and — for the stateful ones
— the globals the native reads or writes. Yggdrasil's `builders.json` then
imports it instead of carrying a hand-kept list. The hand-kept list is exactly
how the current one went four symbols short (`<-vector`, `==`, `@p`,
`shen.hds=?`) while carrying `native_overrides_verified: true`;
`TestNativeOverridesMatchKernelFast` now re-parses `kl/kernelfast.go` on every
run rather than trusting a transcription, and the `_verified` flag is gone.

**A differential test per row.** Evaluate the KL definition and the native on
the same inputs and require identical results, including identical error
behaviour. The KL side has to come from a VM in which `InstallKernelFast` has
not run for that symbol — load the kernel's `defun` under a fresh name — or the
test compares the native with itself. Inputs from three sources: boundary cases
per accepted type (empty and singleton lists, the empty string, multi-byte
strings, zero and negative and large integers, symbols with dots and asterisks,
size-0 and size-1 vectors, and for the higher-order ones both a KL lambda and a
native); every call site of the function in `kernel/klambda/*.kl` that has
literal arguments, extracted mechanically; and seeded random draws from the
same generators so a failure reproduces.

The cases that decide whether the exercise was worth doing are the ones where
a native can plausibly differ: `(reverse ...)` on a 10,000-element list, where
a recursive native overflows and a tail loop does not; `(hdstr "")`, where the
KL body raises and a native might return a sentinel; `(nth 3 [a b])` for index
base and out-of-range; `(map F [])` and a side-effecting `F` for call count and
order; `put`/`get`/`unput` against separate fresh property vectors, comparing
the vectors as well as the return values; `(thaw (freeze E))` running `E`
exactly once; `(shen.string->bytes "λ")` for UTF-8 byte order.

**A harness the table can be re-checked with**, so the cases travel with the
table and a consumer can re-run them: `cmd/kl equiv-check kl/equiv.json`,
printing `equiv NAME ok cases=N` or naming the failing case, non-zero on any
failure.

**No native changes in that work.** A row that fails is recorded
`verified: false` with the failing case, and fixing the native is a separate
change, so that the table's first version is an honest audit rather than a
list of things that were made true while nobody was reading.

And, separately from the table: **the install moved, and it was not enough**.
shen-go PR #48 (`30ab469`) made `cmd/yggdrasil-build` call
`kl.InstallKernelFast()` after every kernel chunk rather than after
`shen.initialise` — that was shen-go issue #46, it is faster, it fixes an
interpreted `put`'s tail call escaping the interpreter's recover inside
`trap-error`, and it is what makes `native_overrides_installed_after` read
`before-initialise`. What it did not change is the `kernelBound` guard inside
`InstallKernelFast`, so the natives are still installed **only for names the
kernel defined**, and a lowered slice therefore loses the native along with the
body. See "The parity, actually run, and what it found". The remaining port-side
change is an unconditional install for the listed names, or a declared fact
saying the install is conditional.

## Keeping the existing guarantees

A lowered slice is not portable: it is valid only for the one port whose table
produced it, so it cannot go through the eight-host byte-identity check or the
cross-target parity gate. Lowering is therefore a stage **after** the canonical
shake, writing to `OUTDIR/lowered/`, and `OUTDIR` stays the artifact of record.
`yggdrasil lower` never modifies a file in `OUTDIR` itself;
`TestLowerLeavesTheCanonicalSliceAlone` shakes twice and compares
`kernel.kl` byte for byte to hold it to that.

The lowered directory is a byte copy of the canonical one except for
`kernel.kl`, plus one file that is not in the canonical slice:
`yggdrasil.lowering.txt`. It records the target, the declared phase, the count,
and one line per dropped name carrying the `builders.json` citation the drop
rests on, so a grepped line carries its own authority. The record is a sidecar
rather than extra manifest lines deliberately: it keeps "the lowered slice is
the canonical slice minus exactly these defuns, and every other file is a byte
copy" a property anything can re-check with `diff`.

## What is per port and what is shared

Per port: the `native_overrides` table, its install phase, the differential
tests, and the port's own kernel suite. Shared: the pass, the gate, the
`badLowering` rule, the measurement, and the three-way parity arrangement. A
new port opts in by declaring a table and a phase, not by writing a compiler
pass.

## What is left

- **Two-level reachability.** `primDef(P, G)` (the port's native definition
  references runtime function `G`) and `targetEdge(G, H)` from an extractor
  over the port's own source, joined to KL-level `reach` through the table, to
  give `runtimeReach` — the port's runtime footprint as a computed relation,
  checkable against a target-level coverage trace exactly as the KL-level trace
  checks `reach`. Nothing of this exists. An earlier draft quoted a "314
  functions of which 94 execute" figure here; it was taken with the Go-level
  SCIP path that has since been deleted, on a shen-go commit the artifact no
  longer builds against, and nothing re-takes it, so it is gone rather than
  carried forward.
- **A second port.** One port conforming is a design; two is a contract that
  has been tested.
- **A real lowering that works.** The parity now runs on `go` and reports
  `FAIL pair=none run=lowered@go` on every fixture tried, because shen-go
  installs a native only for a name the kernel already bound. Until that is
  fixed in the port or declared as a fact the gate can read, no lowered slice
  this repository produces is usable, and `lower` writes one anyway so the
  failure is visible rather than hypothetical.

Related: [optimisation.md](optimisation.md) states the invariant for a KL-to-KL
pass that runs before or after the shake. Lowering is not such a pass — it only
deletes, and it runs after — but the distinction is the point of that note.
