# Writing an eval-free CLI

A shake is either `needs-eval=false` — the kernel keeps only what your code
reaches — or `needs-eval=true`, in which case the reader, macroexpander,
typechecker and eval machinery all stay, because a program that can evaluate
code at run time needs the machinery that evaluates it.

Which one you get is decided by a single test (`yggdrasil.shen`):

```shen
(set *eval-entry-points*
     [eval eval-kl load tc spy track step it
      read read-from-string lineread input input+ bootstrap])

(define eval-free?
  UserFs -> (not (intersect? UserFs (value *eval-entry-points*))))
```

If any name in that list appears in your reachable code, the shake is
eval-capable. Note what is on it: **`read`**, `read-from-string`, `lineread`,
`input`, `input+`. Every ordinary way of reading an S-expression is an eval
entry point.

## The trap

The usual shape of a Shen CLI is:

```shen
(define run-checker
  -> (let Input (read (stinput))          \\ <- eval entry point
          (output "~A~%" (check Input))))
```

Your rule logic can be entirely free of `eval`, `declare` and `(tc +)` and it
will not matter: `read` alone puts the whole program in the eval-capable regime.

This is not hypothetical. `xpc`'s 47-file rule kernel has zero `declare` forms,
no `(tc +)`, and no `eval` in any rule body. Removing its 46 runtime
`(load "rN-....shen")` calls — by concatenating the files at build time, which
is semantically identical since the list is static — got it to
`needs-eval=false`. Then the *driver* re-flipped it, because `run-checker`
called `(read (stinput))`.

Dropping the driver keeps the shake eval-free and produces an artifact that
binds 310 functions and exits, never reading stdin. That is easy to mistake for
success: it builds, links, exits 0, and is much smaller. It just does not do
anything.

There are two ways out: declare that what the program reads is data (see
[Reading S-expressions as data](#reading-s-expressions-as-data) below), or
read bytes rather than forms.

## Reading bytes, not forms

`read-byte` is a primitive. It needs no reader, so it is not an eval entry
point. Slurp bytes and parse them with your own grammar:

```shen
(define slurp
  S Acc -> (let B (read-byte S)
                (if (= B -1) (reverse Acc) (slurp S [B | Acc]))))

(let Bs (slurp (stinput) []) (do (report Bs) ...))
```

`tests/stdin-sum.shen` is the committed fixture for this. It consumes stdin,
reports a position-sensitive digest, and shakes to `needs-eval=false`. It is
gated on every stage-2 target like any other fixture, with its input in
`tests/stdin-sum.stdin` — a fixture may ship a `.stdin` file, and
`scripts/parity-gate.sh` feeds it to both boots (identical bytes, so `two-boot`
and `two-pass` still mean what they say).

So the pattern is tested, not merely asserted:

```
$ printf 'hello yggdrasil' | ./app-go-bin
bytes: 15
digest: 12410
```

## Two things that bite

**Not every kernel function survives the shake.** An earlier draft of that
fixture used `(floor (/ X N))` for a modulus. `floor` is not in the eval-free
footprint, and the artifact failed at **run time** with
`variable floor not bound` — the shake reported success. Keep an eval-free
driver's arithmetic to `+`, `-`, `*` and comparisons, or check the manifest's
`fn=` lines for what you are relying on.

**Stay inside exact integer range.** Hosts differ: some have a bignum/rational
tower, some are float64-only. A digest that overflows 2^53 will disagree across
targets and the parity gate will (correctly) fail it. The fixture uses
`sum of byte * 1-based index`, which stays small for any realistic input.

## Reading S-expressions as data

`read` is an eval entry point on the S42 kernel for a real reason, not an
artefact of the analysis. The reader evaluates code by itself:

```
read -> shen.read-loop -> shen.try-parse -> shen.process-sexprs
     -> shen.unpackage&macroexpand -> shen.unpackage -> eval
```

`shen.unpackage` evaluates the exceptions expression of every
`(package Name Exceptions ...)` form the reader returns, and there are four
more paths like it: macroexpansion compiles a `(defmacro ...)`, a
`(synonyms ...)` or a `(datatype ...)` form it reads, and reading a
`(define F ...)` for a new `F` registers `F`'s arity and evaluates an eta
wrapper for it. So a program that calls `read` on arbitrary input can
evaluate code, and the shake keeps the compiler.

A program whose reads are *data* (a config, a query, a serialised term) can
say so, with one toplevel form in any of its files:

```shen
(set yggdrasil.*read-data* true)

(define read-all
  S Acc -> (let F (read S)
             (if (= F end) (reverse Acc) (read-all S [F | Acc]))))
```

On a Shen host the declaration just sets a global. To the shake it means:
when the program mentions no eval entry point other than `read`,
`read-from-string` and `lineread`, shake it in **read-data** mode:

- everything the eval-free mode strips is stripped, except the macro table:
  macroexpansion is part of what `read` returns, so it stays;
- the five kernel functions above are replaced by versions that raise a
  named error instead of evaluating (`shen.unpackage` keeps its first clause,
  which unpacks a `(package null ...)` form without evaluating anything);
- the arity table and the `shen` package's external symbols are kept whole
  instead of trimmed to the footprint, because the reader's
  `shen.process-applications` reads both to decide how to curry what it
  parsed. The one name dropped from them is `eval-kl`, so that
  `cannot-reach=eval` stays a syntactic fact about the emitted KL.

The manifest says `needs-eval=false`, `cannot-reach=eval` and
`read-data=true`. `tests/read-data.shen` reads its stdin this way and shakes
to 355 kernel defuns, against 551 for the same program without the
declaration, and `--web` builds it.

What the declaration promises, and what holds you to it:

- **Where the full kernel's reader evaluates nothing, the slice reads the
  same value.** `TestReadDataSliceReadsWhatTheFullKernelReads` builds the
  fixture twice, shaken and with `--no-shake` (every kernel defun, nothing
  stripped), runs both on the same stdin and requires identical output,
  which is also the committed golden. Macroexpansion (`(+ 1 2 3)` reads as
  `[+ 1 [+ 2 3]]`) and currying (`(port 80)` reads as `[[fn port] 80]`) come
  out the same.
- **Where it would evaluate, the slice refuses by name.** Reading
  `(defmacro ...)` in the fixture's artifact stops the program with
  `yggdrasil read-data: read a (defmacro ...) form; defining a macro
  evaluates it, and this slice cannot evaluate`, and likewise for the other
  four. The refusal is by form, not by path: a `(datatype ...)` with no
  rules is refused although the kernel would happen to evaluate nothing
  for it. `TestReadDataSliceRefusesFormsThatEvaluate` pins each one.
- **A read value that reaches `eval` is still eval-capable.** The
  declaration only takes the reader family out of the eval test. A program
  that also mentions `eval`, `load`, `input` or any other entry point is
  eval-capable whatever it declares. `tests/read-data-eval.shen` declares
  and then evaluates what it reads, and must shake `needs-eval=true`.

One deviation from the full kernel is left, and it is deliberate: because
`eval-kl` is dropped from the kept tables, a form that applies `eval-kl`
reads curried as an unknown function would, where the full kernel leaves it
as a call.

Without the declaration nothing changes: `read` is an eval entry point, and
a program that must consume S-expressions either declares them data,
accepts `needs-eval=true`, or brings its own reader over `read-byte` as
above. The clean fix is still in the kernel, a reader that returns package
forms unevaluated. Tracked in
[#27](https://github.com/pyrex41/yggdrasil/issues/27).
