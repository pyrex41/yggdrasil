package main

// Stage 4 of docs/analysis-rules.md: dead-initialisation pruning, and the
// per-builder `port_reads` fact list it is gated on.
//
// The shaker decides which toplevel `(set V Lit)` forms the synthesised
// initialiser can drop (see the "stage 4" section of yggdrasil.shen). What it
// cannot know on its own is which globals a PORT's runtime reads natively,
// without any Shen code mentioning them: shen-go's `fn` reads
// shen.*lambdatable* from Go, its `arity` reads *property-vector*, its `open`
// resolves paths through *home-directory*. Those are facts about a backend,
// not about Shen, so they live next to the backend, in builders.json's
// "port_reads" array, and this file hands them to the shaker.
//
// A shake has no target (the whole point is one slice, many backends), so
// `yggdrasil shake --prune-init` uses the UNION of every builder's list --
// the only choice that is sound for a slice that may be built anywhere.
// `build`/`run --target T --prune-init` uses T's own list, which can be
// smaller. Either way the flag is off by default: pruning changes the bytes
// of kernel.kl, and docs/analysis-rules.md gives the parity gate, not this
// file, the job of deciding whether a target may default it on.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// portReadsFor returns the globals a target's runtime reads natively. With an
// empty target it returns the union over every builder, sorted, which is the
// conservative answer a target-agnostic shake needs.
func portReadsFor(target string) ([]string, error) {
	builders, err := loadBuilders()
	if err != nil {
		return nil, err
	}
	if target != "" {
		b, ok := builders[target]
		if !ok {
			return nil, fmt.Errorf("unknown target %q", target)
		}
		out := append([]string(nil), b.PortReads...)
		sort.Strings(out)
		return out, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range builders {
		for _, v := range b.PortReads {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// wrapShakeExpr prefixes the host expression with the stage-4 settings, as
// plain Shen `set`s on the two globals the shaker reads. Off by default: with
// no --prune-init and no target the expression is returned untouched, which is
// what keeps the default output byte-identical to a pre-stage-4 shake's.
//
// The two settings are independent, and the options say so. `o.pruneInit` asks
// for pruning; `o.target` only says WHICH port_reads list to install. A target
// with pruning off is `yggdrasil facts --target T`, which wants T's list in
// portReads.facts and prunes nothing -- yggdrasil.facts never calls
// ygg.prune-init -- so the emitted `(set ygg.*prune-init* ...)` carries
// o.pruneInit rather than an unconditional `true` the caller has to explain
// away.
//
// Pruning against a target whose list is a PLACEHOLDER is refused. builders.json
// records port_reads_verified per target; only `go`'s list has been read off a
// real runtime. Pruning against an unverified list can drop a `(set V Lit)` the
// port's runtime reads natively, and the failure is at run time, in the
// artifact, far from this flag -- so it must be asked for explicitly.
// A target-agnostic --prune-init (o.target == "") uses the UNION over every
// builder, which is the conservative list by construction, and is not refused.
func wrapShakeExpr(expr string, o shakeOpts) (string, error) {
	if !o.pruneInit && o.target == "" {
		return expr, nil
	}
	reads, err := portReadsFor(o.target) // also validates the target name
	if err != nil {
		return "", err
	}
	if o.pruneInit && o.target != "" && !portReadsVerified(o.target) {
		if !o.allowUnverifiedPortReads {
			return "", fmt.Errorf("--prune-init on target %s: its port_reads list in builders.json is a placeholder "+
				"(port_reads_verified: false), not one read off the %s runtime, so pruning against it may drop a "+
				"(set V Lit) that runtime reads natively.\n"+
				"  Shake without --target (the union over every builder is sound for any of them), drop --prune-init, "+
				"or pass --prune-init-unverified to prune against the placeholder anyway",
				o.target, o.target)
		}
		fmt.Fprintf(os.Stderr, "yggdrasil: WARN --prune-init on target %s uses an unverified port_reads list; "+
			"the artifact may read a global the initialiser no longer writes\n", o.target)
	}
	if len(reads) == 0 {
		return "", fmt.Errorf("--prune-init: builders.json lists no port_reads for %s",
			targetLabel(o.target))
	}
	// Shen symbols self-evaluate, so [a b c] is a literal symbol list.
	set := fmt.Sprintf(`(set ygg.*port-reads* [%s])`, strings.Join(reads, " "))
	return fmt.Sprintf(`(do (set ygg.*prune-init* %t) (do %s %s))`, o.pruneInit, set, expr), nil
}

func targetLabel(target string) string {
	if target == "" {
		return "any target"
	}
	return "target " + target
}

// portReadsVerified reports whether a target's port_reads list was written by
// reading that port's runtime (true) or is the conservative placeholder
// (false). Only `go` is verified today; see docs/analysis-rules.md.
func portReadsVerified(target string) bool {
	builders, err := loadBuilders()
	if err != nil {
		return false
	}
	b, ok := builders[target]
	return ok && b.PortReadsVerified
}
