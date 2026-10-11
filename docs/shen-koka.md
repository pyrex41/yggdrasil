# Experimental Shen-Koka integration

The runtime lives on `pyrex41/shen-koka`, branch `implement/klambda-runtime`.
Build it with `nix develop --command bash scripts/build.sh`. Its own `#toolchain`
pin supplies Koka and C dependencies; this integration does not duplicate them.

Bifrost uses `BIFROST_SHEN_KOKA=/absolute/shen-koka/bin/shen-koka`.
Yggdrasil uses `YGGDRASIL_SHEN_KOKA_DIR=/absolute/shen-koka` and target `koka`.
The native artifact embeds only the manifest's KL files and invokes the
manifest initializer once. It uses the Koka interpreter, not direct KL code
generation. `needs-eval=true` exits 3 (UNSUPPORTED). Runtime read metadata
remains unknown and cannot qualify initializer pruning.

The port is not fully qualified. Consult its `evidence/` and implementation
status. Presence in a registry, a buildable artifact, and selected green cases
do not establish full language, IO, platform, or deployment parity.
