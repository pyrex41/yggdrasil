# Prompt: Typed configuration generator (Dhall/CUE competitor)

> Idea 5 of [demo_ideas.md](../demo_ideas.md). Smallest scope; the purest
> showcase of Shen's type system, the lightest use of Yggdrasil.

## Context

This repo is Yggdrasil, a tree-shaker for Shen programs: stage 1
computes the reachable ShenOSKernel-42.2 slice and emits KLambda + a
manifest; per-target stage-2 builders compile it. The Go builder produces
a static single-file binary — the same distribution story as `kustomize`,
which is the point here. See README.md and DEMO.md for invocations.

## Goal

A configuration generator where validity is a *type theory*, not a schema
check. One sentence: **sequent-calculus types for your Kubernetes
manifests.** Dhall/CUE validate documents; this relates *multiple*
documents under user-defined inference rules.

## What it does

1. **Input DSL** (parsed with `defcc`): a concise source format describing
   services — name, image, ports, probes, labels, exposure, RBAC needs —
   from which Deployments, Services, NetworkPolicies, and RBAC objects are
   generated.
2. **Domain type theories** as sequent rules — violations are type errors
   naming the rule and the offending objects:
   - a Deployment's probes must target declared container ports
   - every Service selector matches some Deployment's labels (and the
     match is unique)
   - a "valid NetworkPolicy": every exposed service has a policy covering
     exactly its declared ingress
   - cross-document rules are the differentiator — judgments span the
     whole config set, which schema validators cannot express.
3. **Prolog analyses** (`defprolog`):
   - orphaned selectors / dangling references across the set
   - RBAC privilege-escalation paths (role → binding → subject chains
     that reach cluster-admin-equivalent verbs)
   - impact queries: "which generated objects change if base value X
     changes?"
4. **Output**: deterministic, diff-stable YAML. The generator only emits
   from a config set that typechecks.
5. **Ship** as a shaken Go static binary for CI (`confgen check` /
   `confgen build` / `confgen why <object>`).

## Constraints and gotchas

- Emit YAML via your own writer (a small, deterministic subset — maps,
  lists, scalars, block style). No YAML lib exists in the kernel; don't
  try to parse YAML, only emit it.
- Shen's `read-file` is not a data reader; parse the input DSL from plain
  text with a hand-rolled lexer + `defcc`, keeping `needs-eval=false`.
- the stlib is outside the shake universe and not in every port runtime — use the `ygg.*` helpers
  from `yggdrasil.shen`.
- Scope discipline: do NOT model the full Kubernetes API. Pick the four
  object kinds above with a minimal field set sufficient for the rules.

## Deliverables

- `confgen.shen` (+ supporting files) in this directory.
- `example.conf` — a 3–4 service system with one internal and one exposed
  service, a probe, and an RBAC need.
- A showboat-style `DEMO.md`:
  1. generate: source → typecheck → YAML for all objects
  2. break it: point a Service selector at labels nobody declares → type
     error naming the rule and both documents
  3. ask it: `confgen why` shows the RBAC escalation query finding a
     planted role→binding chain
  4. build the static Go binary and run the same check from a clean dir
- Tests: one fixture per type rule (passing and failing), the Prolog
  analyses, deterministic-output assertion (two runs, identical bytes),
  and an end-to-end shake+build through the Go builder.

## Honest positioning

This idea ranks last for *demo* purposes — the output is dead files, so
Yggdrasil's multi-target story barely appears. Its role in the set is
blog-post material: the clearest illustration that Shen types are
inference rules over your domain, not annotations over your code.
