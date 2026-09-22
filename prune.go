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
	"sort"
	"strings"
)

// pruneOpts is the stage-4 request for the current process: set once from the
// command line, read by shake()/facts() when they build the host expression.
// A package-level value rather than a parameter on shake() because every other
// caller of shake() -- parity, the tests, the oracle -- wants the default, and
// threading an always-zero argument through them all would be noise.
var pruneOpts struct {
	on     bool   // --prune-init
	target string // "" means "no target: use the union over all builders"
}

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
func wrapShakeExpr(expr string) (string, error) {
	if !pruneOpts.on {
		return expr, nil
	}
	reads, err := portReadsFor(pruneOpts.target)
	if err != nil {
		return "", err
	}
	if len(reads) == 0 {
		return "", fmt.Errorf("--prune-init: builders.json lists no port_reads for %s",
			targetLabel(pruneOpts.target))
	}
	// Shen symbols self-evaluate, so [a b c] is a literal symbol list.
	set := fmt.Sprintf(`(set ygg.*port-reads* [%s])`, strings.Join(reads, " "))
	return fmt.Sprintf(`(do (set ygg.*prune-init* true) (do %s %s))`, set, expr), nil
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
