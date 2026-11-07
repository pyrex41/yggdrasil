# Kernel provenance

The sixteen `.kl` files in this directory are vendored byte for byte from
community **ShenOSKernel 43.4**, published by
[Shen-Language/shen-sources](https://github.com/Shen-Language/shen-sources).
`scripts/sync-kernel.sh` fetches them again and checks the hash.

- Repository: https://github.com/Shen-Language/shen-sources
- Tag: `shen-43.4`, tag object `40fd335893cf7caf4c3a285e122ea0de06696430`, commit
  `f34b307b490880eedcfe1f5e5d3cc24ca3d2602e`
- Asset: `ShenOSKernel-43.4.tar.gz`
- SHA-256: `15e7972e6bf0cd65d37c2eba85750b8839d2dbadda41027eae0c88e5017a2756`
- Source directory: `ShenOSKernel-43.4/klambda/`
- License: the release's unmodified `LICENSE.txt`, copied as `LICENSE`.

The tag's tree holds the Shen sources and not `klambda/`, which the release build
generates from them, so the release asset is the published KLambda for that tag.
On 2026-11-06 the asset's `sources/` directory was compared with the tag's and
was identical, and the asset is the one Shen/JIT 0.4 pins, by the same SHA-256.

The universe is the fifteen core files (including `dict.kl`) and `init.kl`,
in the order declared by `*kernel*` in `yggdrasil.shen`. The files contain
735 defuns. Unfolding `shen.initialise` and its three helpers leaves 731
functions for reachability analysis. The vendored files retain upstream
whitespace, including spaces inside multiline string literals.

The release's `stlib.kl` and six `extension-*.kl` files remain outside the
shake universe. Compile-time extension expansion occurs on the matching
43.4 host. A program that calls an excluded library at runtime still needs
an explicit module; vendoring the core does not make that library reachable.

Manifests record `kernel-version=43.4`. The derived call graph is stored as
`callgraph-cache-43.4.shen` outside `KLambda/`, and the CLI embeds only the
explicit source patterns. A cache from another kernel cannot be reused.

The release's 22 files hold 1127 toplevel defuns (1126 lines that start with one, as
`sys.kl` puts `fail` and `userdefs` on one line); 43.2's held 1118.

The previous universe was community 43.2 at `e6947f50`. Definition by definition,
with generated names set aside, 43.4 differs from it in the sixteen files in the
version string, in `declare`, which now hands its checked signature to the new
`shen.declare-with-signature` (43.3), in `shen.factor-recognisors`, whose
factorised `cond` now raises `condition failure` when no clause matches (43.3),
and in two new functions in `sys.kl`, `shen.update-lambda-table/precompiled` and
`shen.precompiled-function?` (43.4). Within the universe only `declare` calls one of
the new functions; `stlib.kl` and the extensions, outside it, call all three. The symbols the sixteen files call
without defining, which `*primitives*` lists, are the same as on 43.2. The S43.1
distribution maintained separately by Mark Tarver is a different source lineage
and is not substituted for the community archive.
