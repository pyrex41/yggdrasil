# Kernel provenance

The sixteen `.kl` files in this directory are vendored byte for byte from
community **ShenOSKernel 43.6**, published by
[Shen-Language/shen-sources](https://github.com/Shen-Language/shen-sources).
`scripts/sync-kernel.sh` fetches and verifies the release archive.

- Tag: `shen-43.6`, tag object `ff21a974d5d8c1b2125c28dbf8b3fa7fb643a940`, commit `956d05303a784bfa413396515afc1b0d3aa98b69`
- Asset: `ShenOSKernel-43.6.tar.gz`
- SHA-256: `61d1795755a57da252ba8108cdfccc680f95a04bfd395a54f533ab6724647ef5`
- Source: `ShenOSKernel-43.6/klambda/`; license: the release's `LICENSE.txt`.

This is the archive pinned by Shen/JIT 0.8. The universe is the fifteen core
files and `init.kl`; the standard library and extensions remain outside it.
Manifests record `kernel-version=43.6`. Call graph caches are kernel-specific.
The S43.1 distribution maintained separately by Mark Tarver is a different lineage.
