# `yggdrasil why`: footprint attribution

**Status**: shipped (Yggdrasil, September 2026)
**Code**: `yggdrasil.shen` — `yggdrasil.why`, `yggdrasil.why-trace`; `main.go` — `cmdWhy`
**Prompted by**: Mark Tarver, *The Future of Shen* (Shen group, September 2026):
"code creep ... the tendency to drag in large amounts of kernel from trivial
lines of code ... You need some kind of device that scans the code and
highlights parts of your program that do that."

## What it prints

```
yggdrasil why PROG [--trace FN]
```

```
yggdrasil-why: mode=eval-free floor=48 total=53 kernel=686
top toplevel adds=5 exclusive=5
seed pr adds=4 exclusive=4
seed stoutput adds=1 exclusive=1
defun fib adds=0 exclusive=0
seed shen.app adds=0 exclusive=0
trace pr: pr
```

All numbers are kernel defuns. The **floor** is what the kernel's own
toplevel init forms reach with no user code at all; every program pays it.
**total** is the footprint the shake would write to `kernel.kl`; **kernel**
is the whole S42 kernel.

Then one row per unit of the program, sorted by cost:

| row | unit |
|---|---|
| `defun F` | a user function; its seeds are the kernel functions its body mentions |
| `top toplevel` | the file's non-defun forms, as one unit (they run in source order) |
| `seed F` | a kernel function the user KL mentions directly |

with two numbers each:

- `adds` = |reach(floor seeds + this)| − |floor|: what this costs on top of
  the floor, on its own.
- `exclusive` = |total| − |reach(everything except this)|: what would leave
  `kernel.kl` if this unit went away.

`adds > exclusive` means the cost is shared with other rows. Rows that add
nothing are still listed, so the report is a complete map.

`--trace FN` appends the shortest call chain from the program to kernel
function `FN`, breadth-first over the same call graph the shake uses. User
seeds are searched first; the init forms only if user code cannot reach
`FN` on its own.

## The point: creep is a floor, not a slope

The report runs in the same mode the shake would (eval-free stripping on or
off, decided by the same `eval-free?` test), so it describes the `kernel.kl`
that would actually be written. Run it on the two partial-function fixtures,
which are the same program with and without one stray mention of `eval`:

```
$ yggdrasil why tests/partial.shen --trace read
yggdrasil-why: mode=eval-free floor=48 total=53 kernel=686
top toplevel adds=5 exclusive=5
seed pr adds=4 exclusive=4
seed stoutput adds=1 exclusive=1
defun f adds=0 exclusive=0
seed shen.app adds=0 exclusive=0
trace read: not in footprint

$ yggdrasil why tests/partial-eval.shen --trace read
yggdrasil-why: mode=eval-capable floor=548 total=548 kernel=686
eval-capable because the program mentions: eval
defun f adds=0 exclusive=0
top toplevel adds=0 exclusive=0
seed eval adds=0 exclusive=0
...
trace read: eval -> shen.shen->kl -> shen.shen->kl-h -> shen.shendef->kldef
  -> shen.<define> -> shen.shendef->kldef-h -> shen.compile-to-kl
  -> shen.scan-body -> shen.f-error -> y-or-n? -> read
```

Two things this shows that the shake alone does not:

1. **The partial function costs nothing either way.** Tarver's example was
   `(define f {number --> number} 0 -> 1)`, whose `pps` shows a
   `(shen.f-error f)` fallthrough. But the S42 kernel's own `bootstrap`
   (load.kl) runs `shen.partial` over the compiled KL, rewriting that call
   to `(simple-error "partial function f")`, so a bootstrapped user program
   never seeds `shen.f-error` at all. What is left are the kernel's
   *internal* `shen.f-error` callers, which the shake handles with the
   `rewrite-f-error` / `strip-f-error-row` pair when the program is
   eval-free.
2. **When creep does happen it is a step function.** One reachable eval
   entry point moves the floor from 48 to 548 of 686 defuns, and every
   per-row number collapses to 0 because everything is already in the
   floor. The `eval-capable because ...` line names the symbols that flipped
   it, and `--trace` shows the chain through `shen.f-error` and `y-or-n?`
   to `read` that Tarver described. The useful per-expression footprint
   measure only exists on the eval-free side of that step, where the
   numbers are small and legible (`pr` costs 4 defuns).

So the "device" has two halves: tell the programmer which side of the step
they are on and why, then attribute the remainder per defun.

Two further things from the same message, and where they stand:

- **Colour banding in an editor** (green for light through red for heavy).
  The `adds` column is that signal; an editor integration would band on it.
  On the eval-free side the bands are meaningful; on the eval-capable side
  every row is red for the same reason, which is why the report names the
  cause instead of colouring every line.
- **Lightweight substitutes**, e.g. `(simple-error "partial application
  wrt f")` in place of `shen.f-error`. S42 already ships this substitute
  for user code (`shen.partial` in `bootstrap`), and the shake applies the
  same one to the kernel's internal callers (`*static-f-error*` in
  `yggdrasil.shen`) when the program is eval-free. The report is how you
  find the next candidate: `--trace` a heavy kernel function and the chain
  names the edge a substitute would cut.

## Cost

Each row is two worklist traversals (`footprint`), O(V+E) each over a
686-node, ~2,600-edge graph, so a report is a few dozen shakes' worth of
graph work: under a second on the shen-go host after the call-graph cache
is warm. `--trace` is one breadth-first search.

## Output contract

The Go driver prints host output from the first `yggdrasil-why:` line on
and drops the host's trailing `done` echo. Lines:

```
yggdrasil-why: mode=M floor=N total=N kernel=N
eval-capable because the program mentions: F G     (eval-capable only)
defun NAME adds=N exclusive=N
top NAME adds=N exclusive=N
seed NAME adds=N exclusive=N
trace F: A -> B -> F                                (or "not in footprint")
```
