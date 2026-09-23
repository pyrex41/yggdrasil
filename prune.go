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
// ONE LIST, AND THIRTEEN UNKNOWNS. Only `go` has had its list read off a
// runtime. Every other target declares no port_reads and inherits the
// `_default` block, whose value is now the literal string "unknown" -- not a
// list. It used to be a 35-name conservative guess, and that is the shape this
// file is about: a target nobody had measured resolved to a list of globals
// indistinguishable, to every reader downstream, from a list somebody had
// checked. Absence of a fact looked like a fact.
//
// What unknown does, in the two places it can be reached:
//
//   - `--prune-init --target T` where T resolves to unknown is REFUSED, by
//     name, with the reason. --prune-init-unverified overrides it and prunes
//     against the union below, loudly.
//   - a target-agnostic `--prune-init` (no --target) is refused too, while
//     any target's reads are unknown. The union it would prune against is
//     over the targets that HAVE declared a list -- today `go` alone, five
//     globals -- and a shake with no --target is by definition a slice that
//     may be built for one of the others. The older claim, "the union over
//     every builder is sound for any of them", was true only of the guess it
//     was a union of, and the union of the measurements is SMALLER, so it
//     prunes more rather than less. The refusal names both halves: the
//     targets the union is over, and the targets it says nothing about.
//     --prune-init-unverified proceeds, with the same two halves as a WARN.
//
// Either way the flag is off by default: pruning changes the bytes of
// kernel.kl, and docs/analysis-rules.md gives the parity gate, not this file,
// the job of deciding whether a target may default it on.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// portReadsFor returns the globals a target's runtime reads natively, and
// whether that answer is KNOWN.
//
// For a named target: its own declared list, or NOTHING with known=false. It
// used to return the union over the declared lists in the unknown case, on the
// reasoning that it was the most that was known -- and `yggdrasil facts
// --target lua` then wrote portReads.facts containing go's five globals, as
// lua's EDB, silently. A relation attributed to a port that nobody read it off
// is the same defect as the 35-name default, one layer down. Unknown resolves
// to the empty list, wrapShakeExpr says so on stderr, and the only caller that
// substitutes the union is the --prune-init-unverified path, which announces
// it in the same breath.
//
// For the empty target: the union over every builder's declared list, sorted,
// with known reporting whether every target contributed one. A target-agnostic
// shake has no backend, so this is the conservative answer available -- over
// the ports that have been measured, and no further.
func portReadsFor(target string) ([]string, bool, error) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		return nil, false, err
	}
	union := func() []string {
		seen := map[string]bool{}
		var out []string
		for _, b := range builders {
			reads, known := effectivePortReads(b, defaults)
			if !known {
				continue
			}
			for _, v := range reads {
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	if target != "" {
		b, ok := builders[target]
		if !ok {
			return nil, false, fmt.Errorf("unknown target %q", target)
		}
		reads, known := effectivePortReads(b, defaults)
		if !known {
			return nil, false, nil
		}
		out := append([]string(nil), reads...)
		sort.Strings(out)
		return out, true, nil
	}
	_, unknown, err := portReadsCoverage()
	if err != nil {
		return nil, false, err
	}
	return union(), len(unknown) == 0, nil
}

// portReadsCoverage is which targets have declared a port_reads list and which
// have not, sorted. Both halves are printed by the messages that use it: one
// naming only the targets in the union would leave the reader to work out
// which ports it was therefore silent about, and that silence is the finding.
func portReadsCoverage() (declared, unknown []string, err error) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		return nil, nil, err
	}
	for name, b := range builders {
		if _, known := effectivePortReads(b, defaults); known {
			declared = append(declared, name)
		} else {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(declared)
	sort.Strings(unknown)
	return declared, unknown, nil
}

// effectivePortReads is the one place the inheritance rule lives: a declared
// list wins, an absent one falls back to `_default` -- which today holds
// "unknown" and so resolves to (nil, false). The boolean is what the function
// is for now: `nil, false` is "nobody measured this port", and that is a
// different claim from `[], true` ("this port reads nothing natively"), which
// would be a licence to prune every init form. An empty list is therefore read
// as no declaration, on both sides, exactly as before.
//
// Callers must go through it (or through portReadsFor) rather than reading
// b.PortReads, which is empty for thirteen of the fourteen targets and means
// "not declared", not "none".
//
// "One place" is load-bearing and was briefly untrue. There are two readers of
// port_reads -- the shaker, through portReadsFor, and `yggdrasil contract`,
// through contractFactRow -- and contract.go first resolved the key itself, by
// key-presence. A report is worth having only if it says what the shaker will
// do, so contractFactRow calls this function instead of deciding again.
// TestDeclaredEmptyPortReadsInheritsInBothReaders pins it.
func effectivePortReads(b, defaults builder) ([]string, bool) {
	if len(b.PortReads) > 0 {
		return b.PortReads, true
	}
	if len(defaults.PortReads) > 0 {
		return defaults.PortReads, true
	}
	return nil, false
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
// PRUNING AGAINST A LIST NOBODY HAS IS REFUSED, in two degrees:
//
//   - UNKNOWN. The target declares no port_reads and `_default` says
//     "unknown", so this repository does not know what that runtime reads
//     natively. There is no list to prune against; the refusal says so.
//   - DECLARED BUT UNVERIFIED. The target has a list with no
//     `port_reads_checked_by` naming a test. portReadsVerified is the single
//     definition of that word, and only `go` passes today.
//
// Either way pruning can drop a `(set V Lit)` the port's runtime reads
// natively, and the failure is at run time, in the artifact, far from this
// flag -- so it must be asked for explicitly, with --prune-init-unverified.
//
// A target-agnostic --prune-init (o.target == "") is refused on the same
// grounds while any target's reads are unknown, and this is the case that used
// to be the recommended way out. The union it would use is over the targets
// that have declared a list; a shake with no --target is a slice that may be
// built for one of the others, so the union is sound for exactly the ports it
// is over and for no one else. The refusal and the --prune-init-unverified
// WARN both name the two halves. The sentence this replaces -- "the union over
// every builder is sound for any of them" -- was true of a union of guesses;
// the union of the measurements is smaller, so it prunes MORE.
func wrapShakeExpr(expr string, o shakeOpts) (string, error) {
	if !o.pruneInit && o.target == "" {
		return expr, nil
	}
	reads, known, err := portReadsFor(o.target) // also validates the target name
	if err != nil {
		return "", err
	}
	if o.pruneInit && o.target != "" && !portReadsVerified(o.target) {
		why := fmt.Sprintf("its port_reads list in builders.json has no port_reads_checked_by naming a test, "+
			"so it is a declaration rather than a measurement of the %s runtime", o.target)
		if !known {
			why = fmt.Sprintf("builders.json states no port_reads for it and the %s block says %q, "+
				"so nobody has read what the %s runtime reads natively",
				builderDefaultsKey, portReadsUnknown, o.target)
		}
		if !o.allowUnverifiedPortReads {
			return "", fmt.Errorf("--prune-init on target %s: %s, and pruning against it may drop a "+
				"(set V Lit) that runtime reads natively.\n"+
				"  Name a target whose list is checked (%s), drop --prune-init, or pass "+
				"--prune-init-unverified to prune against %s anyway.\n"+
				"  Shaking without --target is not the way out: that union is over the same "+
				"declared lists and is refused for the same reason",
				o.target, why, strings.Join(verifiedPortReadsTargets(), ", "), describeReadsSource(known, o.target))
		}
		fmt.Fprintf(os.Stderr, "yggdrasil: WARN --prune-init on target %s prunes against %s; "+
			"%s, so the artifact may read a global the initialiser no longer writes\n",
			o.target, describeReadsSource(known, o.target), why)
		if !known {
			// There is no list for this target. The hatch prunes against
			// the union over the declared lists -- the most conservative
			// thing available -- and the WARN above has just said that it
			// is not a measurement of this target.
			union, _, err := portReadsFor("")
			if err != nil {
				return "", err
			}
			reads = union
		}
	}
	if o.pruneInit && o.target == "" && !known {
		declared, unknown, err := portReadsCoverage()
		if err != nil {
			return "", err
		}
		if !o.allowUnverifiedPortReads {
			return "", fmt.Errorf("--prune-init with no --target: the union it would prune against is "+
				"over the targets that HAVE declared a port_reads list (%s, %d globals), and it says "+
				"nothing about %s, which declare none.\n"+
				"  A shake with no --target is by definition a slice that may be built for one of "+
				"those, where pruning against this union can drop a (set V Lit) that runtime reads "+
				"natively.\n"+
				"  Name a verified target (--prune-init --target go), drop --prune-init, or pass "+
				"--prune-init-unverified to prune against the union anyway.\n"+
				"  `yggdrasil contract --target T` prints which T is which",
				strings.Join(declared, ", "), len(reads), strings.Join(unknown, ", "))
		}
		fmt.Fprintf(os.Stderr, "yggdrasil: WARN --prune-init with no --target prunes against the UNION "+
			"of the port_reads declared by: %s (%d globals).\n"+
			"  That union says nothing about %s, which declare none: building this slice for one of "+
			"them may read a global the initialiser no longer writes.\n"+
			"  `yggdrasil contract --target T` prints which T is which.\n",
			strings.Join(declared, ", "), len(reads), strings.Join(unknown, ", "))
	}
	if !o.pruneInit && o.target != "" && !known {
		// `facts --target T` and `build --target T` with pruning off: the
		// list is only an EDB, and the honest EDB for a port nobody has
		// measured is EMPTY. Filling it with another port's globals is how
		// portReads.facts came to say that lua reads shen.*lambdatable*.
		// One line, on stderr, because a silently empty relation is the
		// other half of the same problem.
		fmt.Fprintf(os.Stderr, "yggdrasil: WARN portReads is %s for target %s (builders.json: it "+
			"declares no port_reads and %s says %q), so the portReads relation is EMPTY -- it is "+
			"not this port's reads and it is not another port's either. `yggdrasil contract "+
			"--target %s` says the same.\n",
			portReadsUnknown, o.target, builderDefaultsKey, portReadsUnknown, o.target)
		reads = nil
	}
	if o.pruneInit && len(reads) == 0 {
		return "", fmt.Errorf("--prune-init: builders.json lists no port_reads for %s, and no other "+
			"target declares one either, so there is nothing to prune against",
			targetLabel(o.target))
	}
	// Shen symbols self-evaluate, so [a b c] is a literal symbol list.
	set := fmt.Sprintf(`(set ygg.*port-reads* [%s])`, strings.Join(reads, " "))
	return fmt.Sprintf(`(do (set ygg.*prune-init* %t) (do %s %s))`, o.pruneInit, set, expr), nil
}

// describeReadsSource names the list a refusal or a WARN is talking about, so
// that the two cases -- "this target's own declared list" and "the union over
// the targets that have one, because this target has none" -- are never the
// same sentence.
func describeReadsSource(known bool, target string) string {
	if known {
		return "target " + target + "'s own declared port_reads"
	}
	return "the union over the targets that HAVE declared a port_reads list (" +
		strings.Join(declaredPortReadsTargets(), ", ") + "), which is not a measurement of " + target
}

func declaredPortReadsTargets() []string {
	declared, _, err := portReadsCoverage()
	if err != nil || len(declared) == 0 {
		return []string{"none"}
	}
	return declared
}

// verifiedPortReadsTargets are the targets --prune-init accepts without the
// escape hatch: a declared list with a checked_by naming a test. It is the
// only suggestion a refusal can make that does not lead to another refusal.
func verifiedPortReadsTargets() []string {
	var out []string
	for _, t := range declaredPortReadsTargets() {
		if portReadsVerified(t) {
			out = append(out, "--target "+t)
		}
	}
	if len(out) == 0 {
		return []string{"none today"}
	}
	return out
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
// its own, the default's value is "unknown", and its `_checked_by` is "none".
// Only `go` passes.
func portReadsVerified(target string) bool {
	builders, err := loadBuilders()
	if err != nil {
		return false
	}
	b, ok := builders[target]
	return ok && len(b.PortReads) > 0 && factChecked(b.PortReadsCheckedBy)
}
