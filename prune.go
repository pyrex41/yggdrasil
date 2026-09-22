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
	"os"
	"sort"
	"strings"
)

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
//
// The two settings are independent, and the options say so. `o.pruneInit` asks
// for pruning; `o.target` only says WHICH port_reads list to install. A target
// with pruning off is `yggdrasil facts --target T`, which wants T's list in
// portReads.facts and prunes nothing -- yggdrasil.facts never calls
// ygg.prune-init -- so the emitted `(set ygg.*prune-init* ...)` carries
// o.pruneInit rather than an unconditional `true` the caller has to explain
// away.
//
// Pruning against a target whose list is a PLACEHOLDER is refused. "Verified"
// is portReadsVerified's single definition -- the target declares a list of its
// own and builders.json gives it a `port_reads_checked_by` naming a test --
// and only `go` passes today; every other target inherits the `_default` block,
// whose checked_by is "none". Pruning against an unverified list can drop a
// `(set V Lit)` the port's runtime reads natively, and the failure is at run
// time, in the artifact, far from this flag -- so it must be asked for
// explicitly.
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
				"(no port_reads_checked_by in builders.json naming a test), not one read off the %s runtime, so pruning against it may drop a "+
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
