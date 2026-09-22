// Stage 5 of docs/analysis-rules.md: the shake's KL-level oracle.
//
// The shake's claim is about the KL it writes. Stages 1-3 check that claim
// against itself (three engines, one footprint). Nothing checks the step
// AFTER it: that the stage-2 builder compiled the shaken KL into a program
// whose OWN call graph still reaches everything the shake decided to keep.
//
// `yggdrasil scip-check PROG OUTDIR --target go` builds both:
//
//	A*  the shaken slice           (yggdrasil.shake)
//	A   the full program K + user  (yggdrasil.shake-full, --no-shake)
//
// with the SAME builder into two module directories, recovers the KL-level
// call graph from each generated module (see klGraph below), and asserts BY
// NAME that (1) every defun the shake kept in kernel.kl is reachable in the
// graph the backend actually emitted, and (2) every name reachable in A* is
// reachable in A.
//
// The only subtraction on (1) is the names the target is DECLARED to lower
// without a symbol lookup (builders.json `special_forms`, with a
// `special_forms_source` citing the line of the port for each), plus the
// user's own defuns and the synthesised shen.initialise. There is no integer
// "delta" to eyeball; residue on any side is printed name by name and fails,
// and the declaration itself is audited (see specialFormsAudit) so a stale
// entry cannot silence a finding.
//
// Why the KL level and not the Go level: see the klGraph comment below. An
// earlier version of this file indexed both modules with scip-go and decoded
// the index with a hand-written protobuf reader, but the graph it got that
// way has four nodes however big the program is, so the inclusion it checked
// was the trivial direction and could not fail. It is gone.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ------------------------- the KL-level graph --------------------------
//
// shen-go's yggdrasil-build does NOT emit one Go function per KL defun. It
// emits one 0-arity module thunk per chunk --
//
//	var KernelChunk0 = MakeNative(func(__e *ControlFlow) { ... })
//
// -- inside which every defun is an anonymous closure bound at run time
// (`Call(__e, ns2_1set, symF, MakeNative(...))`, ns2_1set being `defun`) and
// every call is a run-time symbol lookup (`Call(__e, PrimFunc(symF), ...)`).
// So the Go-level node graph of the generated module has a handful of nodes
// however big the program is, and comparing it proves only that both builds
// produced the same shape of module.
//
// The node graph Stage 5 wants is still in there, one level down: the defun
// bindings are the nodes and the PrimFunc lookups are the edges. klGraph
// recovers it from the generated Go with go/ast, which makes the check a
// real statement about the backend's output rather than about its packaging.
type klGraph struct {
	Defuns map[string]map[string]bool // KL name -> KL names its body looks up
	Seeds  map[string]bool            // looked up outside any defun body: the initialiser and the user's toplevel forms
}

func newKLGraph() *klGraph {
	return &klGraph{Defuns: map[string]map[string]bool{}, Seeds: map[string]bool{}}
}

// klSymbols reads `var symX = MakeSymbol("name")` out of the generated
// symbols.go, which is how every KL name reaches the generated code.
func klSymbols(moduleDir string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(moduleDir, "symbols.go"), nil, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	ast.Inspect(f, func(x ast.Node) bool {
		vs, ok := x.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
			return true
		}
		call, ok := vs.Values[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "MakeSymbol" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out[vs.Names[0].Name] = name
		return true
	})
	return out, nil
}

// buildKLGraph walks the generated module and records, for every KL defun
// the backend emitted, the KL names its body looks up.
func buildKLGraph(moduleDir string) (*klGraph, error) {
	syms, err := klSymbols(moduleDir)
	if err != nil {
		return nil, err
	}
	g := newKLGraph()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, moduleDir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && fi.Name() != "symbols.go"
	}, 0)
	if err != nil {
		return nil, err
	}
	// lookups collects every PrimFunc(symG) in a subtree, not descending
	// into a nested defun binding (whose lookups belong to that defun).
	var lookups func(n ast.Node, skip map[ast.Node]bool) map[string]bool
	lookups = func(n ast.Node, skip map[ast.Node]bool) map[string]bool {
		out := map[string]bool{}
		ast.Inspect(n, func(x ast.Node) bool {
			if x == nil || skip[x] {
				return false
			}
			call, ok := x.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "PrimFunc" || len(call.Args) != 1 {
				return true
			}
			if arg, ok := call.Args[0].(*ast.Ident); ok {
				if name, ok := syms[arg.Name]; ok {
					out[name] = true
				}
			}
			return true
		})
		return out
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			// First pass: find every defun binding and its body.
			bodies := map[ast.Node]string{}
			ast.Inspect(f, func(x ast.Node) bool {
				call, ok := x.(*ast.CallExpr)
				if !ok || len(call.Args) < 4 {
					return true
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok || fn.Name != "Call" {
					return true
				}
				if id, ok := call.Args[1].(*ast.Ident); !ok || id.Name != "ns2_1set" {
					return true
				}
				nameID, ok := call.Args[2].(*ast.Ident)
				if !ok {
					return true
				}
				name, ok := syms[nameID.Name]
				if !ok {
					return true
				}
				bodies[call.Args[3]] = name
				return true
			})
			skip := map[ast.Node]bool{}
			for node := range bodies {
				skip[node] = true
			}
			for node, name := range bodies {
				if _, seen := g.Defuns[name]; !seen {
					g.Defuns[name] = map[string]bool{}
				}
				inner := map[ast.Node]bool{}
				for other := range bodies {
					if other != node {
						inner[other] = true
					}
				}
				for ref := range lookups(node, inner) {
					g.Defuns[name][ref] = true
				}
			}
			for ref := range lookups(f, skip) {
				g.Seeds[ref] = true
			}
		}
	}
	return g, nil
}

// klReach is the shake's own reach rule, evaluated over the graph the
// BACKEND emitted rather than over the KL the shaker read.
func klReach(g *klGraph) map[string]bool {
	seen := map[string]bool{}
	var work []string
	for s := range g.Seeds {
		if _, ok := g.Defuns[s]; ok && !seen[s] {
			seen[s] = true
			work = append(work, s)
		}
	}
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		for ref := range g.Defuns[n] {
			if _, ok := g.Defuns[ref]; ok && !seen[ref] {
				seen[ref] = true
				work = append(work, ref)
			}
		}
	}
	return seen
}

// userDefuns reads the manifest's fn= lines: the user program's own defuns,
// so the KL node count can be split into kernel and user.
func userDefuns(shakenDir string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(shakenDir, "yggdrasil.manifest.txt"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "fn=") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "fn="))
		if len(f) > 0 {
			out[f[0]] = true
		}
	}
	return out
}

// ------------------------------ the check ------------------------------

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kernelDefunNames reads the defun NAMES out of a shaken kernel.kl. This is
// the shake's own footprint, the set of names it decided to keep, and it is
// what the artifact's emitted call graph is held against. Counting them was
// not enough: a count can only produce a delta, and a delta has to be
// explained by a person.
//
// The scan skips string literals, because kernel.kl contains its own writer's
// source text: line 889 of the full kernel is `"(defun fail () shen.fail!)"`
// inside shen.write-kl-h. A naive substring scan reports `fail` as a kept
// defun whether the shake kept it or not, and since the footprint now gates
// the exit code that is a phantom `kl-delta-missing fail` in any slice that
// keeps shen.write-kl-h without fail. KL string literals have no backslash
// escape -- Shen spells control characters `c#N;` -- so a quote toggle is the
// entire lexer this needs.
func kernelDefunNames(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src := string(b)
	out := map[string]bool{}
	const marker = "(defun "
	inString := false
	for i := 0; i < len(src); i++ {
		if src[i] == '"' {
			inString = !inString
			continue
		}
		if inString || !strings.HasPrefix(src[i:], marker) {
			continue
		}
		k := i + len(marker)
		e := k
		for e < len(src) && !strings.ContainsRune(" \t\r\n()\"", rune(src[e])) {
			e++
		}
		if e > k {
			out[src[k:e]] = true
		}
		i = e - 1
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no defuns found in %s", path)
	}
	return out, nil
}

// klHeads returns the names that occur in HEAD position -- written `(name `
// -- anywhere in a kernel.kl, outside string literals. It is not part of the
// verdict; it is what turns a bare residue name into a diagnosis.
//
// The rules' edge relation is "the name occurs in this defun's body". That
// counts a quoted symbol in code the kernel *constructs* -- `(cons shen.f-error
// (cons V761 ()))` in shen.scan-body, `(= input Select5848)` in shen.macros --
// as an edge, deliberately and soundly, because the kernel may later evaluate
// what it built. A backend's edge relation cannot: there is no call site to
// compile, so no `PrimFunc` lookup is emitted and the recovered graph has no
// edge. A residue name with no head-position occurrence anywhere in the KL is
// that disagreement; a residue name that IS called somewhere is a different
// animal, and the two should not read alike in the output.
//
// Being text, this over-reports rather than under-reports: a `(name ` inside
// a string literal is skipped, but one inside quoted data that happens to be
// written out literally still counts as a call site, which can only move a
// name from the first description to the second.
func klHeads(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src := string(b)
	out := map[string]bool{}
	inString := false
	for i := 0; i < len(src); i++ {
		if src[i] == '"' {
			inString = !inString
			continue
		}
		if inString || src[i] != '(' {
			continue
		}
		e := i + 1
		for e < len(src) && !strings.ContainsRune(" \t\r\n()\"", rune(src[e])) {
			e++
		}
		if e > i+1 {
			out[src[i+1:e]] = true
		}
	}
	return out, nil
}

// specialFormsAudit holds the DECLARATION itself to account. A subtraction
// that is never checked is not reviewable: a stale or over-broad
// `special_forms` entry would silence a real residue with no output at all,
// and nothing would ever say the declaration had rotted. So every declared
// name is classified against this run and every class is printed.
//
//	unknown  the name is not a defun in the FULL kernel at all, so it cannot
//	         be a kernel name this port lowers specially: the declaration is
//	         stale or misspelled. A finding; it fails the check.
//	applied  the name is in the footprint and NOT reachable, so the
//	         subtraction did work here: it is the reason a residue is absent.
//	unused   a real kernel defun that was not subtracted in this run, either
//	         because this shake did not keep it or because the artifact
//	         reaches it by an ordinary lookup anyway. Reported, not a
//	         finding: which special forms a slice exercises is a property of
//	         the program, while the declaration is a property of the port.
func specialFormsAudit(foot, rs, special, fullFoot map[string]bool) (applied, unused, unknown []string) {
	for _, name := range sortedKeys(special) {
		switch {
		case len(fullFoot) > 0 && !fullFoot[name]:
			unknown = append(unknown, name)
		case foot[name] && !rs[name]:
			applied = append(applied, name)
		default:
			unused = append(unused, name)
		}
	}
	return applied, unused, unknown
}

// klDelta is the whole accounting, as a pure function so it can be tested
// without a toolchain.
//
//	foot    the defun names the shake kept (kernel.kl, less shen.initialise)
//	rs      the names reachable in the graph the backend actually emitted
//	special the names this target is DECLARED to lower without a symbol
//	        lookup, from builders.json `special_forms`
//	users   the user program's own defuns, which live outside kernel.kl
//
// missing is a name the shake kept that the artifact's own graph cannot
// reach and nothing accounts for: either the shake kept something dead or
// the port lowers it specially and has not said so. extra is a name the
// artifact reaches that the shake did not keep, which would mean the slice
// is not closed. Both are findings; neither is subtracted silently.
func klDelta(foot, rs, special, users map[string]bool) (missing, extra []string) {
	for _, name := range sortedKeys(foot) {
		if !rs[name] && !special[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range sortedKeys(rs) {
		if !foot[name] && !users[name] && name != "shen.initialise" {
			extra = append(extra, name)
		}
	}
	return missing, extra
}

func cmdScipCheck(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil scip-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	target := fs.String("target", "go", "stage-2 target to build both programs with (only compositional backends are meaningful)")
	verbose := fs.Bool("v", false, "also list the KL-level reachable name set of the shaken build")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target")); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil scip-check PROG OUTDIR [--target go]")
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}
	if *target != "go" {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: SKIP target %s is not known to compile calls compositionally\n", *target)
		return 3
	}

	// The accounted-for subtraction is DECLARED, not decided here. A target
	// with no `special_forms` key subtracts nothing, which is the safe
	// default: its residue shows up as a finding rather than as silence.
	special := map[string]bool{}
	var specialNames []string
	var specialSource string
	if builders, err := loadBuilders(); err == nil {
		if bd, ok := builders[*target]; ok {
			specialNames = append(specialNames, bd.SpecialForms...)
			sort.Strings(specialNames)
			for _, n := range specialNames {
				special[n] = true
			}
			specialSource = bd.SpecialFormsSource
		}
	}

	start := time.Now()
	abs, _ := filepath.Abs(outdir)
	shakenDir := filepath.Join(abs, "shaken")
	fullDir := filepath.Join(abs, "full")

	// A* and A, same builder, same flags, different stage 1.
	for _, leg := range []struct {
		dir  string
		full bool
		what string
	}{{shakenDir, false, "shaken"}, {fullDir, true, "full"}} {
		if _, err := shakeMode(prog, leg.dir, host, *evalStyle, true, leg.full); err != nil {
			fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL %s stage 1: %v\n", leg.what, err)
			return 1
		}
		runArgv, err := build(*target, leg.dir, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL %s stage 2: %v\n", leg.what, err)
			return 1
		}
		if runArgv == nil {
			fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: SKIP target %s (a required tool is not on PATH)\n", *target)
			return 3
		}
	}
	shakenMod := filepath.Join(shakenDir, "app-go")
	fullMod := filepath.Join(fullDir, "app-go")

	klShaken, err := buildKLGraph(shakenMod)
	if err != nil {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL reading the shaken module's KL graph: %v\n", err)
		return 1
	}
	klFull, err := buildKLGraph(fullMod)
	if err != nil {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL reading the full module's KL graph: %v\n", err)
		return 1
	}
	foot, err := kernelDefunNames(filepath.Join(shakenDir, "kernel.kl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL reading the shaken footprint: %v\n", err)
		return 1
	}
	delete(foot, "shen.initialise") // synthesised; the rules never put it in reach

	// The full build's kernel.kl is every defun the port has, and is what a
	// declared special form has to be one of.
	fullFoot, err := kernelDefunNames(filepath.Join(fullDir, "kernel.kl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL reading the full kernel's defun names: %v\n", err)
		return 1
	}

	heads, err := klHeads(filepath.Join(shakenDir, "kernel.kl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL reading the shaken kernel's call sites: %v\n", err)
		return 1
	}

	rs := klReach(klShaken)
	rf := klReach(klFull)
	users := userDefuns(shakenDir)

	// (2) A* included in A, by name.
	var klMissing []string
	for _, name := range sortedKeys(rs) {
		if !rf[name] {
			klMissing = append(klMissing, name)
		}
	}
	// (1) the footprint accounted for, by name.
	missing, extra := klDelta(foot, rs, special, users)
	// (3) the declaration itself accounted for, by name.
	applied, unusedSpecial, unknownSpecial := specialFormsAudit(foot, rs, special, fullFoot)

	kernelSide := 0
	for name := range klShaken.Defuns {
		if !users[name] {
			kernelSide++
		}
	}
	fmt.Printf("yggdrasil-scip-check: target=%s wall=%.1fs\n", *target, time.Since(start).Seconds())
	fmt.Printf("yggdrasil-scip-check: kl-level shaken defuns=%d (kernel=%d user=%d) reachable=%d; full defuns=%d reachable=%d\n",
		len(klShaken.Defuns), kernelSide, len(klShaken.Defuns)-kernelSide, len(rs), len(klFull.Defuns), len(rf))
	fmt.Printf("yggdrasil-scip-check: kl-level footprint=%d reachable=%d\n", len(foot), len(rs))
	// The subtraction the residue below actually used is `applied`, not the
	// whole declaration: naming the declaration here would credit this run
	// with subtractions it never made.
	fmt.Printf("yggdrasil-scip-check: kl-level delta accounted: special_forms=%s user-defuns=%s initialise=1\n",
		joinOrNone(applied), joinOrNone(sortedKeys(users)))
	fmt.Printf("yggdrasil-scip-check: special_forms declared=%d applied=%s unused=%s unknown=%s\n",
		len(specialNames), joinOrNone(applied), joinOrNone(unusedSpecial), joinOrNone(unknownSpecial))
	if specialSource != "" {
		fmt.Printf("yggdrasil-scip-check: special_forms source: %s\n", specialSource)
	}
	if *verbose {
		for _, name := range sortedKeys(rs) {
			fmt.Printf("yggdrasil-scip-check:   kl-reachable %s\n", name)
		}
	}
	if len(missing) == 0 && len(extra) == 0 && len(klMissing) == 0 && len(unknownSpecial) == 0 {
		fmt.Printf("yggdrasil-scip-check: OK footprint=%d reachable=%d accounted=%d\n",
			len(foot), len(rs), len(applied))
		return 0
	}
	// Each residue name is annotated with whether the KL it came from calls
	// it at all. This is a DIAGNOSIS, not a subtraction: no name leaves the
	// residue because of it, and the exit code above is already decided.
	for _, m := range missing {
		why := "called in kernel.kl but unreachable in the emitted graph"
		if !heads[m] {
			why = "no call site in kernel.kl: kept for a symbol occurrence in constructed code"
		}
		fmt.Printf("yggdrasil-scip-check:   kl-delta-missing %s (%s)\n", m, why)
	}
	for _, e := range extra {
		fmt.Printf("yggdrasil-scip-check:   kl-delta-extra %s\n", e)
	}
	for _, k := range klMissing {
		fmt.Printf("yggdrasil-scip-check:   kl-missing-in-full %s\n", k)
	}
	for _, u := range unknownSpecial {
		fmt.Printf("yggdrasil-scip-check:   special-forms-unknown %s (declared for %s but not a defun in the full kernel)\n", u, *target)
	}
	fmt.Printf("yggdrasil-scip-check: FAIL footprint=%d reachable=%d delta-missing=%d delta-extra=%d kl-missing-in-full=%d special-forms-unknown=%d\n",
		len(foot), len(rs), len(missing), len(extra), len(klMissing), len(unknownSpecial))
	return 1
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ",")
}
