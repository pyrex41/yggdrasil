# Design note: the invariant for a KL-to-KL pass around the shake

**Status**: design note; no such pass is implemented (Yggdrasil, September 2026)
**Builds on**: [reachability.md](reachability.md) (the footprint),
[analysis-rules.md](analysis-rules.md) (the rule set),
[verification-guide.md](verification-guide.md) section 12 ("Is this the
smallest possible artifact?")
**Question**: if Yggdrasil ever runs a KL-to-KL transformation — inlining,
specialisation, a rewrite of a kernel idiom — what has to be true of it for the
shake's checks to still mean anything, and how many times does the shake have
to run?

The shake removes; it never rewrites. Everything the verification guide
certifies is about removal. A pass that rewrites carries its own obligations,
and this note writes down the one that concerns the shake. It is not the pass's
correctness obligation — that is separate, and larger — it is the structural
property that decides whether the pass and the shake compose.

## The invariant

For a KL-to-KL pass `T` applied to a program `P`:

```
reach(T(P)) ∩ kernel ⊆ reach(P) ∩ kernel
```

**The kernel functions reachable after the pass are a subset of those
reachable before.** Over kernel defuns only. The pass is free to add, remove
and rewrite user-level defuns as it likes — specialisation does exactly that,
and a pass that could not would be useless.

That restriction is the whole content of the note, and it is what an earlier
framing got wrong. "Footprint-monotone" — the footprint never grows — is the
wrong invariant for an optimise-then-shake arrangement, because a pass that
specialises `(shen.app X Y Z)` into `shen.app-string` **adds a function**. The
footprint of the transformed program is larger by that defun, and a check that
counted functions would refuse a pass that is doing exactly what it is for.
What must not grow is the part of the *kernel* that the program depends on: a
pass that introduces a call to a kernel function the original program never
reached has enlarged the trusted surface of the artifact, and every
reachability argument about the original no longer covers it.

The asymmetry is deliberate. A user-level defun the pass invented is code the
pass is responsible for and the pass's own equivalence obligation covers. A
kernel defun newly pulled in is code nobody audited for this program, arriving
through a rewrite rather than through the program text.

## How it is checked

Run the shake twice and compare footprints:

```
yggdrasil shake P     OUT_before
yggdrasil shake T(P)  OUT_after
```

and require the kernel defun names of `OUT_after/kernel.kl` to be a subset of
those of `OUT_before/kernel.kl`. That is a set comparison over two files
(`kernelDefunNames` in `scip.go` already reads them), and it costs two shakes.

Reachability is cheap — about 0.1 s for the closure on a slice of this size —
so the measurement is not the expensive part; the host boot is, and two boots
is the price of the check. An equality check rather than a subset check would
be wrong in the other direction: a pass whose point is to make a kernel
function unreachable (which is what lowering does, by deletion) shrinks the
set, and shrinking is the good case.

The check says nothing about whether `T` is correct. It says that if `T` is
correct, the shake's argument about `T(P)` is covered by the same kernel
audit as the argument about `P`. A pass that violates it is not necessarily
wrong; it is a pass whose artifact needs a fresh look at a part of the kernel
nothing looked at, and that has to be a visible event rather than a silent one.

## No fixpoint loop

An early sketch of this had optimise and shake alternating to a fixpoint. That
is unnecessary, and the invariant is why.

If the invariant holds, one pass followed by one shake suffices. The shake
computes the least fixpoint of reachability over the *final* program text; it
does not care how that text was produced, and running it again on its own
output changes nothing, because the footprint of a shaken program is already
closed under the rules. The only reason to shake twice would be that the pass
had made something newly unreachable *after* the shake ran — which cannot
happen if the pass runs first — or that the pass wanted to see the shaken
footprint in order to decide what to do, which is a different design (a pass
parameterised by the footprint) and still terminates in one round: shake,
pass, shake, where the second shake is the one whose output ships and the
first is an input to the pass.

So the orderings that make sense are `pass; shake` and `shake; pass; shake`,
and both are finite by construction. A loop would buy iterations whose only
justification would be that nobody had established the invariant.

Lowering (see [lowering.md](lowering.md)) is the degenerate case: it only
deletes, and it runs after the shake, on a slice it does not re-shake. Its
`reach` can only shrink, so the invariant holds trivially and there is nothing
to loop over.

## Correctness is port-independent; profitability is not

Two things about a KL-to-KL pass get confused because they arrive together,
and they belong to different owners.

**Correctness is a property of KL.** If `T` rewrites `(f (g x))` into `(fg x)`
and `fg` computes what the composition computes, that is true under every
port, because KL's semantics are what both sides are stated in. The
verification guide's section 2 argument — the backend sees the same KL for the
full and the transformed program, so the transfer step through `B_P` is the
same one — carries over unchanged. A pass does not need to be re-verified per
port, and a pass that does need to be is a pass that has quietly assumed
something about a backend.

This is also where the honest warning goes. Yggdrasil's checks certify the
*removal* of code; they say nothing about a rewrite. This is precisely the gap
Tarver identified for the kernel's own factorisation and triple-stack
rewrites: elegant, working, and without a proof of extensional equivalence. A
KL-level optimiser would need its own rule set, its own oracle and its own
trace check. The machinery in the verification guide is a template for
building them, not a substitute for them.

**Profitability is a property of a port.** Whether `(fg x)` is *faster* than
`(f (g x))` depends on the backend: a port that already fuses the composition
gains nothing and pays for an extra defun; a port that boxes every
intermediate gains a lot; a port with a whole-program optimising compiler
(SBCL, Chez) may do better with the untransformed form because its own
analysis recognises the idiom. Size is the same story — one more defun is one
more defun on every port, but what it saves differs.

Two consequences:

- A pass may be **always applied** (correct everywhere) while being
  **selectively worth applying**. If Yggdrasil ever gates a pass on the
  target, the gate is about profitability and must be declared as such — a
  `builders.json` fact with a source, like every other port fact — never
  smuggled in as a correctness condition. A correctness condition that is
  really a performance preference is the kind of claim the port contract
  exists to keep out.
- Measuring profitability needs the port, and therefore needs the parity and
  timing machinery, not the rule engine. `yggdrasil parity --time` already
  reports per-target wall clock and already says it is advisory and never
  fails the gate; that is the right shape for it.

## What would have to be built

Nothing here is implemented, and this note is not a proposal to implement it
tomorrow. In order:

1. The subset check as a command or a test helper: two shakes, two defun-name
   sets, a subset assertion, and a report naming any kernel defun the pass
   newly pulled in.
2. One pass, small enough that its equivalence obligation is dischargeable by
   hand, to have something to check.
3. The pass's own oracle and trace check, on the template of sections 5 to 8
   of the verification guide.

Until at least (2) exists, the honest status of KL-level optimisation in this
repository is "not attempted", and the value of this note is that the
invariant is written down before a pass is written against a wrong one.
