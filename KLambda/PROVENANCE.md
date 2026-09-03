# Kernel provenance

The `.kl` files in this directory are Mark Tarver's **S42.0 (2026-08-25)**
kernel, vendored byte-for-byte from the canonical mirror of his uploads:

- **Canonical source:** `pyrex41/shen-upstream`, tag
  `s42-pristine-20260825`, files `KLambda/*.kl`.
- **Upstream origin:** <https://www.shenlanguage.org/Download/S42.zip>,
  re-uploaded 2026-08-25. Zip SHA-256:
  `30abdc7e5a1e27b7a20109c1ed141e4712885e31f24d9710d16415fbbd4dfb23`

This is a **lineage switch**: earlier Yggdrasil vendored the community
ShenOSKernel-41.2 packaging (Shen-Language/shen-sources, tag `shen-41.2`).
The S-series kernel differs structurally (and has had the 15-file
backend.kl layout since 41.1 — see the mirror's PROVENANCE.md for the
lineage note):

- `backend.kl` — the `cl.*` KL→Lisp compiler, inside the kernel. It is
  vendored for the Lisp builder's eval path but is **not** on the
  runtime boot list (`*kernel*`), matching upstream `install.lsp`.
- `compiler.kl` (shen-cl build artifact), `dict.kl`, `init.kl`,
  `stlib.kl` and the community `extension-*.kl` files are gone. Dicts
  are replaced by a property vector (`*property-vector*`); the stlib
  ships as lazily-loaded Shen sources (`S41/Lib/StLib`); there is **no
  `shen.initialise`** — initialisation is toplevel forms in
  `declarations.kl` and `types.kl`, which the shake wraps into a
  synthetic `(defun shen.initialise () ...)`.
- Against the community kernel: 672 shared defuns, 156 modified
  (including `put`, `get`, `arity`, `bootstrap`, `read`, `macroexpand`),
  21 new, ~26 core removals besides the dropped files.
- Boot order is `install.lsp`'s: sys writer core reader declarations
  toplevel macros load prolog sequent track t-star yacc types.

No `callgraph-*.shen` belongs in this directory. The derived call-graph cache
lives at the root of the shaker tree as `callgraph-cache.shen`, because
`main.go` embeds this directory into the binary and a bare `go:embed KLambda`
would sweep a generated cache in with the kernel sources — which is how a
stale graph once reached extracted roots and silently under-shook. The embed
patterns now name `KLambda/*.kl` explicitly, so only kernel sources, this
file and `LICENSE` are embedded.
