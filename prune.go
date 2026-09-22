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
// One list, not thirteen. Only `go` has had its list read off a runtime. The
// other targets declare no port_reads at all and inherit builders.json's
// `_default` block, which holds the conservative union exactly once. A target
// with no port_reads key is a target whose native reads nobody has measured,
// and the missing key is how that looks -- rather than the same placeholder
// pasted into every entry, which is a heuristic wearing the shape of data.
//
// A shake has no target (the whole point is one slice, many backends), so
// `yggdrasil shake --prune-init` uses the UNION of every builder's effective
// list -- the only choice that is sound for a slice that may be built
// anywhere. `build`/`run --target T --prune-init` uses T's own list, which can
// be smaller. Either way the flag is off by default: pruning changes the bytes
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

// portReadsFor returns the globals a target's runtime reads natively: the
// target's own declared list when it has one, otherwise builders.json's
// `_default` list. With an empty target it returns the union over every
// builder's EFFECTIVE list, sorted, which is the conservative answer a
// target-agnostic shake needs.
func portReadsFor(target string) ([]string, error) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		return nil, err
	}
	if target != "" {
		b, ok := builders[target]
		if !ok {
			return nil, fmt.Errorf("unknown target %q", target)
		}
		out := append([]string(nil), effectivePortReads(b, defaults)...)
		sort.Strings(out)
		return out, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range builders {
		for _, v := range effectivePortReads(b, defaults) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// effectivePortReads is the one place the inheritance rule lives: a declared
// list wins, an absent one falls back to `_default`. Callers must go through
// it (or through portReadsFor) rather than reading b.PortReads, which is empty
// for twelve of the thirteen targets and means "not declared", not "none".
//
// "One place" is load-bearing and was briefly untrue. There are two readers of
// port_reads -- the shaker, through portReadsFor, and `yggdrasil contract`,
// through contractFactRow -- and contract.go first resolved the key itself, by
// key-presence. The two predicates agree on every target in the file today and
// disagree on `"port_reads": []`: presence says "declared, none", this says
// "declares nothing, inherit". A report is worth having only if it says what
// the shaker will do, so contractFactRow calls this function instead of
// deciding again. TestDeclaredEmptyPortReadsInheritsInBothReaders pins it.
func effectivePortReads(b, defaults builder) []string {
	if len(b.PortReads) > 0 {
		return b.PortReads
	}
	return defaults.PortReads
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

// portReadsVerified reports whether a target's port_reads list is backed by a
// named test. That is the only thing "verified" is allowed to mean here: the
// two booleans this replaces (`port_reads_verified`, `native_overrides_
// verified`) spelled one word and asserted two unrelated predicates -- "each
// entry was read from a named source line" and "the list equals the symbols
// InstallKernelFast rebinds" -- so the word now has a single definition, in
// one place, and `yggdrasil contract` prints it.
//
// A target inheriting `_default` is never verified: it has declared nothing of
// its own, and the default's own `_checked_by` is "none". Only `go` passes.
func portReadsVerified(target string) bool {
	builders, err := loadBuilders()
	if err != nil {
		return false
	}
	b, ok := builders[target]
	return ok && len(b.PortReads) > 0 && factChecked(b.PortReadsCheckedBy)
}
