# Kernel provenance

The sixteen `.kl` files in this directory are vendored byte for byte from
community **ShenOSKernel 43.2**, published by
[Shen-Language/shen-sources](https://github.com/Shen-Language/shen-sources).
`scripts/sync-kernel.sh` fetches them again and checks the hash.

- Repository: https://github.com/Shen-Language/shen-sources
- Tag: `shen-43.2`, tag object `7b7563e5bb7fb38573ee579f8cab6ea323c39ec8`, commit
  `e6947f507ebc03f7ade5d2a8d971bd05d84a5bae`
- Asset: `ShenOSKernel-43.2.tar.gz`
- SHA-256: `23c71508f0de24785e1f127ea4fab440081904d81c463090bc642ee840417f32`
- Source directory: `ShenOSKernel-43.2/klambda/`
- License: the release's unmodified `LICENSE.txt`, copied as `LICENSE`.

The tag's tree holds the Shen sources and not `klambda/`, which the release build
generates from them, so the release asset is the published KLambda for that tag.
On 2026-11-05 the asset's `sources/` directory was compared with the tag's and
was identical, and its `klambda/` was identical to the kernel Shen/JIT 0.2 pins.

The universe is the fifteen core files (including `dict.kl`) and `init.kl`,
in the order declared by `*kernel*` in `yggdrasil.shen`. The files contain
732 defuns. Unfolding `shen.initialise` and its three helpers leaves 728
functions for reachability analysis. The vendored files retain upstream
whitespace, including spaces inside multiline string literals.

The release's `compiler.kl`, `stlib.kl`, and six `extension-*.kl` files remain outside the
shake universe. Compile-time extension expansion occurs on the matching
43.2 host. A program that calls an excluded library at runtime still needs
an explicit module; vendoring the core does not make that library reachable.

Manifests record `kernel-version=43.2`. The derived call graph is stored as
`callgraph-cache-43.2.shen` outside `KLambda/`, and the CLI embeds only the
explicit source patterns. A cache from another kernel cannot be reused.

The previous universe was community 43.1 at `715824a5`. Definition by definition,
with generated names set aside, 43.2 differs from it in the version string and in
`shen.by-hypothesis` and `shen.search-user-datatypes`, which now fail at once on an
empty list through two new helpers, `shen.by-hypothesis-h` and
`shen.search-user-datatypes-h`. The S43.1 distribution maintained separately by
Mark Tarver is a different source lineage and is not substituted for the community
archive.
