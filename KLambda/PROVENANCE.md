# Kernel provenance

The `.kl` files in this directory are the community **ShenOSKernel 42.2**
KLambda, vendored byte-for-byte from the official release. It is the kernel
upstream [shen-scheme 0.50](https://github.com/tizoc/shen-scheme/tree/v0.50)
ships, and the one every Yggdrasil port moved to on its `kernel-42.2` line.

- **Repository:** [Shen-Language/shen-sources](https://github.com/Shen-Language/shen-sources)
- **Tag:** `shen-42.2`, commit `73507e069fe147aefb728d632c4ce40bc6a7b314`
- **Release asset:** `ShenOSKernel-42.2.tar.gz`
  (<https://github.com/Shen-Language/shen-sources/releases/download/shen-42.2/ShenOSKernel-42.2.tar.gz>),
  SHA-256 `1d02273003654d34ec020ae67cb214ecec410dee3350419ff807d82b652678d8`
  (the checksum pinned in shen-scheme 0.50's Makefile)
- **`(version)`:** `"42.2"`, set by `init.kl`
- **Files:** `ShenOSKernel-42.2/klambda/{toplevel,core,sys,dict,sequent,yacc,reader,prolog,track,load,writer,macros,declarations,types,t-star,init}.kl`,
  each checked identical with `cmp` against a fresh extraction of the
  tarball above. `LICENSE` is the tarball's `LICENSE.txt`.

Manifests written against this kernel record `kernel-version=42.2`.

## Lineage decision

Option (a) of pyrex41/yggdrasil#32: **adopt ShenOSKernel 42.2 wholesale.**
Earlier Yggdrasil vendored Mark Tarver's S42.0 (2026-08-25; mirror
`pyrex41/shen-upstream`, tag `s42-pristine-20260825`, manifests
`kernel-version=42-s42.20260825`), and before that the community 41.2 and
Tarver's refreshed S41.2. The S-series reuses the community's version
numbers but is a different lineage; this move goes back to the community
line, matching shen-scheme 0.50 byte for byte. No file here is patched.

What changed for the shaker, against S42.0:

- **Initialisation is a function again.** Every 42.2 `.kl` file holds only
  defuns, and `init.kl` defines `shen.initialise` (a do-chain of
  `shen.initialise-environment`, `-lambda-forms` and `-signedfuncs`). S42
  had no `shen.initialise`; its initialisation was toplevel forms
  interleaved through `declarations.kl` and `types.kl`. The shake now
  *unfolds* init.kl's initialiser into a flat list of init forms
  (`ygg.unfold-init` in `yggdrasil.shen`), so trimming, the init-order
  check, stage-4 pruning and the trace weaver keep working on forms, and
  still writes a synthesised `shen.initialise` into `kernel.kl`. The
  builder contract is unchanged.
- **Lambda forms are literals.** 42.2 registers one
  `(shen.set-lambda-form-entry (cons F (lambda ...)))` per kernel function
  in the property vector; S42 built `shen.*lambdatable*` with `eval-kl` at
  boot, which the shake had to replace with a literal of its own. That
  machinery is gone.
- **Type signatures** are 42.2's `(set shen.*sigf* (shen.assoc-> F Rule ...))`
  forms rather than S42's toplevel `(declare F Type)` forms; stripped
  programs drop them the same way.
- **`dict.kl` is back**: `*property-vector*` is a `shen.dict`.
- **`backend.kl` is gone.** It was S42's in-kernel KL-to-Lisp compiler and
  was never on the boot list; the Lisp builder uses shen-cl's own
  precompiled compiler.

## The shake universe

Seventeen files of the release's 23 are vendored, and those seventeen are
the universe the shake draws from (`*kernel*` in `yggdrasil.shen`, in
shen-cl's `boot.lsp` order): the fifteen core files, `dict.kl` and
`init.kl`. 726 defuns once the initialiser is unfolded (730 in the files,
less `shen.initialise` and the three helpers it inlines); 2,770 call-graph
edges.

Not vendored, and outside the universe:

- `stlib.kl`, the precompiled standard library. It must be loaded after the
  kernel and initialised with `stlib.initialise` (macros, synonyms,
  datatypes, types), and port runtimes do not uniformly carry it. A shaken
  program that calls a stlib function at run time names a function the
  slice does not define.
- The six `extension-*.kl` files (`features`, `expand-dynamic`, `launcher`,
  `programmable-pattern-matching`, `namespaces`, `type-annotations`). They
  act on the HOST at compile time (source-form handlers, macros, the
  command line); their effect is already in the user KL that the host's
  `bootstrap` emits, and a shaken artifact does not call them.

No `callgraph-*.shen` belongs in this directory. The derived call-graph cache
lives at the root of the shaker tree as `callgraph-cache-42.2.shen`, because
`main.go` embeds this directory into the binary and a bare `go:embed KLambda`
would sweep a generated cache in with the kernel sources — which is how a
stale graph once reached extracted roots and silently under-shook. The embed
patterns now name `KLambda/*.kl` explicitly, so only kernel sources, this
file and `LICENSE` are embedded.
