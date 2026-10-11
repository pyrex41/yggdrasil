# Prompt: Mesh-network control plane (WireGuard config prover/generator)

> Idea 2 of [demo_ideas.md](../demo_ideas.md). Best built after the
> firewall compiler — it reuses the same analysis style in a new domain.

## Context

This repo is Yggdrasil, a tree-shaker for Shen programs: stage 1
computes the reachable ShenOSKernel-42.2 slice of a program and emits
KLambda + a manifest; per-target stage-2 builders compile it. The Go
builder (`../shen-go/cmd/yggdrasil-build <dir> <outdir>` then `go build`)
produces a ~4.5 MB static binary with ≤10 ms startup that cross-compiles
to linux/windows — the natural shape for an ops tool. See README.md and
DEMO.md for exact invocations.

## Goal

A mesh-network **control plane** in Shen: ingest a WireGuard mesh
topology, *prove* its invariants, and generate the per-peer configs.
One sentence: **Shen proves the trust graph; the kernel does the crypto.**

Explicitly out of scope: any cryptography. No ChaCha20, no key generation
(shell out to `wg genkey` or take keys as input). The data plane stays in
WireGuard; this tool is the brain that today is YAML and hope.

## What it does

1. **Ingest** a topology file (design a small DSL or s-expression format,
   parsed with `defcc`): peers with public keys and endpoints, AllowedIPs
   per peer, trust edges (who may talk to whom), zone tags
   (`prod`/`dev`/`quarantine`), and pending revocations.
2. **Prove invariants** — failures print concrete counterexamples:
   - **AllowedIPs disjointness**: no two peers claim overlapping CIDR
     ranges. A Prolog query that must fail; on overlap, report the exact
     overlapping prefix. Use prefix/interval reasoning, not enumeration.
   - **Trust-path reachability**: `(reaches? A B)` as a `defprolog`
     transitive-closure query, and its dual — prove peer X is *not*
     reachable from the quarantine zone.
   - **Revocation impact**: "does revoking key K partition the network?"
     — answered before the revocation, naming the orphaned nodes.
   - **Zone conformance** as typed judgments: e.g. `prod peers never
     trust dev peers directly` expressed as sequent rules, so a violating
     topology is a type error.
3. **Generate** per-peer `wg-quick` config files (or `wg set` scripts)
   from the proven topology — only from a proven topology.

## Constraints and gotchas

- The shaken kernel has no networking or shell access; the tool reads the
  topology file and writes config files. A thin wrapper script applies
  them (`wg-quick up` etc.).
- Read input as plain text — Shen's `read-file` is not a data reader.
- the stlib is outside the shake universe and not in every port runtime; reuse the `ygg.*`
  helpers from `yggdrasil.shen`.
- Aim for `needs-eval=false`: hand-rolled lexer feeding `defcc` token
  lists, no `eval`/`read`/`load` in the shipped tool.

## Deliverables

- `meshc.shen` (+ supporting files) in this directory.
- `example.mesh` — a ~6-peer topology with zones, including one peer
  whose role is to be revoked in the demo.
- A showboat-style `DEMO.md`:
  1. compile the topology → proofs reported → per-peer configs emitted
  2. bring up a real 3-node mesh in network namespaces (`ip netns`) and
     ping across it — netns keeps the demo self-contained (needs root)
  3. add a peer whose AllowedIPs overlaps an existing subnet → tool
     refuses, printing the overlapping range
  4. run the partition query on a proposed revocation → it names the
     nodes that would be orphaned
- Tests for each analysis (overlap, unreachable quarantine violation,
  partitioning revocation) plus one end-to-end shake+build through the Go
  builder, confirming `needs-eval=false` and a single-file static binary.
