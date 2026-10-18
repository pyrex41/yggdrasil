# Kernel provenance

The sixteen `.kl` files in this directory are vendored byte for byte from
community **ShenOSKernel 43.1**, the release used by Shen CL 3.2.0.

- Repository: https://github.com/Shen-Language/shen-sources
- Tag: `shen-43.1`, commit `715824a52b55e9c81a9501df8548ff3edd8411f5`
- Asset: `ShenOSKernel-43.1.tar.gz`
- SHA-256: `900eedef44f440af78f9395ed06e689ce1604546fc309f5bc7c8bc84cc651920`
- Source directory: `ShenOSKernel-43.1/klambda/`
- License: the release's unmodified `LICENSE.txt`, copied as `LICENSE`.

The universe is the fifteen core files (including `dict.kl`) and `init.kl`,
in the order declared by `*kernel*` in `yggdrasil.shen`. The files contain
731 defuns. Unfolding `shen.initialise` and its three helpers leaves 727
functions for reachability analysis. The vendored files retain upstream
whitespace, including spaces inside multiline string literals.

The release's `compiler.kl`, `stlib.kl`, and six `extension-*.kl` files remain outside the
shake universe. Compile-time extension expansion occurs on the matching
43.1 host. A program that calls an excluded library at runtime still needs
an explicit module; vendoring the core does not make that library reachable.

Manifests record `kernel-version=43.1`. The derived call graph is stored as
`callgraph-cache-43.1.shen` outside `KLambda/`, and the CLI embeds only the
explicit source patterns. A 42.2 cache cannot be reused for this kernel.

The previous universe was community 42.2 at `73507e06`; this upgrade replaces
it wholesale. The S43.1 distribution maintained separately by Mark Tarver is
a different source lineage and is not substituted for the community archive.
