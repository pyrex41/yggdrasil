# Kernel provenance

The sixteen `.kl` files in this directory are vendored byte for byte from
community **ShenOSKernel 43.7**, published by
[Shen-Language/shen-sources](https://github.com/Shen-Language/shen-sources).
`scripts/sync-kernel.sh` fetches and verifies the release archive.

- Tag: `shen-43.7`, tag object `4112f674b3d22a962324564b512bfcc2f9ee90ae`, commit `7b1d1305a4acab575cffda344c6048b14b0e37c7`
- Asset: `ShenOSKernel-43.7.tar.gz`
- SHA-256: `55911dd95b719aa0eaeff8fbd926fcbbb9cfafab04dc01f6e283750b9461e487`
- Source: `ShenOSKernel-43.7/klambda/`; license: the release's `LICENSE.txt`.

This is the archive pinned by Shen/JIT 0.11 and 0.12. The universe is the fifteen core
files and `init.kl`; the standard library and extensions remain outside it.
Manifests record `kernel-version=43.7`. Call graph caches are kernel-specific.
The S43.1 distribution maintained separately by Mark Tarver is a different lineage.
