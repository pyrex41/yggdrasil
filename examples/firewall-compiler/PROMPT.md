# Prompt: Verified firewall/routing policy compiler for OpenResty

> Idea 1 of [demo_ideas.md](../demo_ideas.md) — the recommended first build.

## Context

This repo is Yggdrasil, a tree-shaker for Shen programs. Stage 1
(`yggdrasil.shen`, run on any certified Shen — developed against a sibling
`../shen-cl` checkout) computes the reachable slice of the ShenOSKernel-42.2
and writes KLambda + a manifest to an output dir. Stage 2 builders compile
that slice per target; the LuaJIT builder
(`../shen-lua/bin/yggdrasil-build.lua <dir> <out.lua>`) produces a
self-contained ~640 KB .lua file with ~25 ms startup, and the Go builder
produces a static binary. When the program never touches an eval-capable
entry point, eval-stripping leaves only ~100 kernel defuns. See README.md
and DEMO.md for the exact invocations.

## Goal

Build a firewall/routing policy compiler in Shen, and a deployment story
where each compiled policy becomes its own tiny Lua artifact running inside
OpenResty. One sentence: **your firewall policy, as a verified 640 KB Lua
file.**

## Architecture (two stages, mirroring Yggdrasil's own)

1. **Authoring tool** (`policyc.shen`) — full Shen, runs at CI time:
   - Parses a policy DSL with `defcc`. The DSL covers: HTTP method, path
     patterns (literal segments + wildcards), client CIDR ranges, header
     equality tests, and actions `allow | deny | rate-limit <class>`.
     Rules are ordered; first match wins.
   - Runs analyses and refuses to compile on failure, printing concrete
     counterexamples:
     - **Totality**: every request matches some rule. Require an explicit
       final catch-all rather than inventing a default.
     - **No shadowing**: for each rule, prove some request reaches it.
       Implement as `defprolog` queries over the match predicates, using
       prefix/interval reasoning for CIDRs and segment unification for
       paths — never enumeration.
     - **No contradiction**: consistent actions for the rate-class table.
     - **Diff mode**: `policyc diff old.policy new.policy` reports every
       request class newly allowed (witnesses, not booleans).
   - On success, **emits a specialized Shen program**: the ruleset compiled
     to straight pattern-matching dispatch (`decide Method Path IP Headers
     -> action`), plus a tiny request-decoding shim. No interpreter in the
     artifact.

2. **Decision artifact** — shake the emitted program with Yggdrasil
   (must report `needs-eval=false` in the manifest), build with the Lua
   builder, and load from nginx via `access_by_lua_file`. The Lua side
   needs a small adapter mapping ngx request vars to the artifact's entry
   function and the returned action to `ngx.exit`/rate-limit handling.

## Constraints and gotchas

- The shaken kernel has **no networking** — nginx does all I/O; the
  artifact is a pure decision function.
- Keep the artifact eval-strippable: tokenize the DSL with a hand-rolled
  lexer feeding `defcc` token lists in the *authoring tool only*; the
  emitted program must not mention `eval`, `read`, `load`, `tc`, etc.
- Shen's `read-file` is not a data reader (it curries and cons-ifies);
  read policy files as plain text/lines.
- the stlib is outside the shake universe and not in every port runtime — reuse the `ygg.*`
  helpers in `yggdrasil.shen` rather than `mapc`/`filter`/etc.
- Keep the v1 predicate language small enough that shadowing stays
  decidable by construction.

## Deliverables

- `policyc.shen` (+ supporting files) in this directory.
- `example.policy` — a realistic ~15-rule policy (API routes, an admin
  subnet, a rate-limited public endpoint, a catch-all deny).
- `nginx/` — minimal OpenResty config wiring the artifact in.
- A showboat-style `DEMO.md` with the money-shot narrative:
  1. compile `example.policy` → proofs reported → `policy.lua`
  2. `curl` against OpenResty: allowed route passes, blocked route 403s
  3. edit the policy so one rule shadows another → compiler refuses with a
     counterexample request
  4. fix, rebuild, reload, `curl` shows the new behavior
- Tests covering each analysis (a shadowed ruleset, a non-total ruleset, a
  broadening diff) and one end-to-end shake+build verified through the Lua
  builder (fastest), with a Go-binary build of the same policy as the
  "same policy, second runtime" beat.
