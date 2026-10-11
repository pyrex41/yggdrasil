# Prompt: Authorization engine — one verified policy source, five runtimes

> Idea 3 of [demo_ideas.md](../demo_ideas.md). The ambitious flagship —
> build after the firewall compiler proves the policy-as-binary machinery.

## Context

This repo is Yggdrasil, a tree-shaker for Shen programs: stage 1
computes the reachable ShenOSKernel-42.2 slice and emits KLambda + a
manifest; per-target stage-2 builders compile it for Common Lisp, LuaJIT
(self-contained ~640 KB .lua), Go (static ~4.5 MB binary), Rust, and
JavaScript (self-contained ~120 KB ES module via ShenScript). See
README.md and DEMO.md for exact invocations.

## Goal

A Cedar/OPA-class authorization engine where the policy language, the
analyses, and the production decision engine are **one Shen source**.
The hook: AWS Cedar maintains Dafny proofs *alongside* a separate Rust
implementation and must keep them in sync; OPA/Rego has no proofs at all.
Here there is nothing to keep in sync — and Yggdrasil's multi-target story
is the moat: the **same shaken slice** becomes the Lua module in the API
gateway, the Go middleware in the services, the JS module in the Node
services, and the Rust library at the edge. Identical decision semantics by construction, not by discipline.

## What it does

1. **Policy language** (parsed with `defcc`), deliberately restrained for
   v1 — no full Cedar entity hierarchies:
   - `permit`/`forbid` statements over (principal, action, resource)
   - principals/resources with flat attributes; simple condition
     expressions (equality, set membership, boolean combinators)
   - **forbid overrides permit**; default deny
   - decisions return the action *and a reason string* naming the
     deciding policy — the demo depends on this being deterministic.
2. **Typed well-formedness**: a custom type theory where ill-scoped
   attributes and unsatisfiable conditions are *type errors*, caught at
   compile time with the offending policy named.
3. **Analyses as Prolog queries** (`defprolog`):
   - "Who can access resource R?" / "What can principal P do?" —
     enumeration over the policy set with condition reasoning.
   - **Policy diff** — the killer CI feature: compare two policy sets and
     report every newly-granted (principal, action, resource) triple as a
     concrete witness. "Did this PR broaden anyone's access?"
   - Forbid-coverage: "is there any path to resource R not guarded by
     condition C (e.g. MFA)?"
4. **Compilation**: emit a specialized Shen decision program per policy
   set (straight pattern matching, no interpreter — the policy-as-binary
   pattern), shake it (`needs-eval=false`), and build **all five targets**
   from the same output dir.
5. **Host adapters** (thin, per target): nginx `access_by_lua` glue for
   the Lua artifact; a `net/http` middleware example calling the Go
   artifact; an Express/Fastify middleware importing the JS artifact; a
   Rust CLI wrapping the Rust artifact; the Lisp image as the
   interactive/analysis REPL.

## Constraints and gotchas

- The engine is a pure function: (principal, action, resource, context)
  → (decision, reason). All I/O and entity fetching belongs to the hosts.
- Keep the artifact eval-strippable; the authoring/analysis tool may use
  the full reader and eval machinery.
- Shen's `read-file` is not a data reader; parse policy files as text.
- the stlib is outside the shake universe and not in every port runtime — use the `ygg.*` helpers.
- Benchmark Lua-path decision latency early; specialization should keep
  it to straight dispatch, but verify under wrk before widening scope.

## Deliverables

- `authzc.shen` (+ supporting files) in this directory.
- `example.authz` — a policy set with enough texture for the analyses:
  roles, an MFA-conditioned admin action, at least one forbid override.
- Host adapter examples for at least Lua/nginx and Go.
- A showboat-style `DEMO.md`:
  1. compile → proofs → five artifacts from one source
  2. the same request denied by nginx, the Go middleware, and the Rust
     CLI — **byte-identical reason string** from all three
  3. a policy PR that quietly broadens access → the diff query prints the
     exact new (principal, action, resource) triples it would grant
- Tests: well-formedness rejections, forbid-override semantics, diff
  witnesses, and a cross-target differential test asserting identical
  decisions over a generated request corpus.
