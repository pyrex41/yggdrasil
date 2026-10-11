# Prompt: Binary-protocol parser workbench (langsec)

> Idea 4 of [demo_ideas.md](../demo_ideas.md).

## Context

This repo is Yggdrasil, a tree-shaker for Shen programs: stage 1
computes the reachable ShenOSKernel-42.2 slice and emits KLambda + a
manifest; per-target stage-2 builders compile it for Common Lisp, LuaJIT,
Go, Rust, and JavaScript. See README.md and DEMO.md for exact invocations.

## Goal

A parser workbench in the langsec tradition: define a binary wire format
*as a grammar*, analyze the grammar, and ship the same verified parser to
multiple production runtimes. One sentence: **grammar in, verified parser
out, on five runtimes.**

The motivating claim: parsers of untrusted input are where the CVEs live,
and the langsec answer — parse with verified grammars, not hand-rolled
pointer arithmetic — rarely meets production runtimes. `defcc` + Yggdrasil
closes that gap.

## What it does

1. **Pick one real format** for v1 — DNS wire format (queries + responses,
   including name compression) is the sweet spot: small, ubiquitous, and
   famously easy to get wrong. (Alternative: a TLV-framed IoT protocol.)
2. **Grammar**: express the format as `defcc` productions over a byte
   stream. `defcc` is designed for token lists, so build a thin byte-lexer
   layer (each byte a token, with helpers for u16/u32, length-prefixed
   runs, and bounded lookahead for DNS compression pointers).
3. **Typed structural judgments**: length fields that must agree with the
   data they describe, label lengths ≤ 63, total name ≤ 255, compression
   pointers that must point backwards — encoded as type-system judgments
   on the AST, not post-hoc asserts.
4. **Grammar analyses** with `defprolog`:
   - ambiguity detection over the productions
   - unreachable productions
   - "can any input make field A disagree with length B?" — adversarial
     queries over the structure rules
   - compression-pointer loop impossibility (the classic DNS DoS).
5. **Artifacts** from the same shaken slice:
   - **Rust** library — the production network-service parser
   - **Lua** module — an OpenResty-side protocol filter
   - **Go** binary — CLI validator usable as a fuzzing oracle
   The parse result is a typed AST (or a structured reject with offset and
   reason); hosts handle all I/O.

## Constraints and gotchas

- No networking in the shaken kernel: artifacts parse byte vectors handed
  in by the host. The Go CLI reads stdin/files via its host shim.
- Keep artifacts eval-strippable (`needs-eval=false`).
- Per-byte tokenization through `defcc` has real overhead — benchmark
  early; consider grammar-driven *generation* of a specialized parser
  (the same emit-then-shake pattern as the other examples) if the direct
  interpretation is too slow.
- Shen's `read-file` is not a data reader; the corpus loader and any
  cache files must be plain text/bytes.
- the stlib is outside the shake universe and not in every port runtime — use the `ygg.*` helpers.

## Deliverables

- `parserw.shen` + the DNS grammar in this directory.
- A fuzz/test corpus: valid packets, truncations, oversize labels,
  forward/looping compression pointers, length-field lies.
- A showboat-style `DEMO.md`:
  1. show the grammar; run the analyses (demonstrate one being caught by
     temporarily introducing an ambiguous production)
  2. build the Rust, Lua, and Go artifacts from one shake
  3. **differential test**: feed the corpus to all three — identical
     accept/reject verdicts and offsets
  4. the kicker: run the same corpus against a popular hand-written DNS
     parser and show where it disagrees with its own RFC
- Tests: each structural judgment (one malformed packet per rule), the
  Prolog analyses, and the cross-target differential harness wired into
  the test suite.
