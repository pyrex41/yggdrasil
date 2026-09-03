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

## The way out: read bytes, not forms

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

## What is still missing

There is no way to keep a `read`-based driver *and* an eval-free shake, and
arguably there should be: `(read (stinput))` whose result never reaches
`eval`/`eval-kl` is reading data, not code, and the analysis cannot currently
tell those apart. Tracked in
[#22](https://github.com/pyrex41/yggdrasil/issues/22). Until then, a program
that must consume S-expressions from stdin has to either accept
`needs-eval=true` or bring its own reader.
