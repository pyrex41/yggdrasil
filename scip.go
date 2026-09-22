// Stage 5 of docs/analysis-rules.md: the shake's KL-level oracle. Stages 1-3
// check the shake's claim against itself; nothing checked the step after it,
// that the stage-2 builder compiled the shaken KL into a program whose OWN
// call graph still reaches everything the shake kept.
//
// `yggdrasil scip-check PROG OUTDIR --target go` builds A* (shaken) and A
// (--no-shake) with the SAME builder, recovers the KL-level call graph from
// each module (klGraph), and asserts BY NAME that (1) every defun kept in
// kernel.kl is reachable in the graph the backend emitted and (2) A*'s
// reachable set is inside A's. The only subtraction on (1) is declared:
// builders.json `special_forms` plus what only those reach (inlinedClosure),
// the user's defuns, and shen.initialise. Residue is printed name by name
// and fails; the declaration is itself audited.
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

// klInitialiser is manufactured by the shake at write time (trim-top), not
// read out of the kernel: it has no row in the call graph the rules run on.
const klInitialiser = "shen.initialise"

// shen-go's yggdrasil-build emits one 0-arity thunk per chunk, not one Go
// function per KL defun; inside it each defun is a closure bound at run time
// and each name is a generated `symX` variable:
//
//	tmpN := MakeNative(func(__e *ControlFlow) { ...body... }, 2)
//	tmpM := Call(__e, ns2_1set, symdo, tmpN)     // ns2_1set is `defun`
//
// An earlier version compared the Go-level graph, which has a handful of
// nodes however big the program is. Stage 5's graph is one level down: the
// bindings are the nodes, the symbols in their bodies the edges.
type klGraph struct {
	Defuns map[string]map[string]bool // KL name -> KL names its body mentions
	Seeds  map[string]bool            // mentioned outside every defun body, plus the driver's entry points
}

// klVisit attributes each symbol occurrence to the defun whose body it is in,
// or to the seeds when it is in none; copying the visitor down the tree is
// what carries the owner to the children.
type klVisit struct {
	g       *klGraph
	syms    map[string]string
	bodies  map[ast.Node]string // the closure that is this defun's body
	bound   map[ast.Node]bool   // the name node of a binding: a definition, not a reference
	owner   string              // "" while outside every defun body
	initial bool                // the owner is klInitialiser (see buildKLGraph)
}

func (v klVisit) Visit(n ast.Node) ast.Visitor {
	if n == nil || v.bound[n] {
		return nil
	}
	if name, ok := v.bodies[n]; ok {
		v.owner, v.initial = name, name == klInitialiser
		return v
	}
	dst := v.g.Seeds
	if v.owner != "" {
		dst = v.g.Defuns[v.owner]
	}
	switch x := n.(type) {
	case *ast.Ident:
		// A MENTION, in any position, because the shake's edge relation is
		// (analysis/analysis.dl D1: "argpos yields an edge unconditionally,
		// exactly like callpos") -- the kernel constructs KL it may later
		// evaluate, `(cons shen.f-error (cons V761 ()))` in shen.scan-body.
		// Call-position-only makes a residue out of the two sides
		// disagreeing about what an edge is, not out of a dropped name.
		if !v.initial {
			if name, ok := v.syms[x.Name]; ok {
				dst[name] = true
			}
		}
	case *ast.CallExpr:
		// MakeSymbol("f") is how the driver in main.go names an entry point
		// it calls directly; PrimFunc(symF) is an ordinary lookup.
		if name, ok := makeSymbolArg(x); ok {
			dst[name] = true
			return v
		}
		id, ok := x.Fun.(*ast.Ident)
		if !ok || id.Name != "PrimFunc" || len(x.Args) != 1 {
			return v
		}
		if a, ok := x.Args[0].(*ast.Ident); ok {
			if name, ok := v.syms[a.Name]; ok {
				dst[name] = true
			}
		}
	}
	return v
}

func makeSymbolArg(x ast.Node) (string, bool) {
	call, ok := x.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	fn, _ := call.Fun.(*ast.Ident)
	lit, _ := call.Args[0].(*ast.BasicLit)
	if fn == nil || fn.Name != "MakeSymbol" || lit == nil || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

// buildKLGraph recovers the KL graph from the generated module. symbols.go's
// `var symX = MakeSymbol("name")` is the dictionary, so it is parsed but not
// walked. The generated code names every intermediate, a defun's body
// closure included, so a binding's fourth argument is an identifier and the
// body one hop away; following that hop is the difference between a graph
// with edges and one whose every lookup seeds itself.
//
// klInitialiser is the one node whose bare symbols are NOT edges: it is the
// shake's own output, and trim-top rewrites the arity table, the external-
// symbols list and the lambda table inside it AGAINST THE FOOTPRINT
// (analysis.dl D7), so every kept name is quoted there by construction.
// Following those quotations saturates the reach set and makes the check
// vacuous -- the circularity the rules avoid at the same sites (D2).
func buildKLGraph(moduleDir string) (*klGraph, error) {
	fset := token.NewFileSet()
	dict, err := parser.ParseFile(fset, filepath.Join(moduleDir, "symbols.go"), nil, 0)
	if err != nil {
		return nil, err
	}
	syms := map[string]string{}
	ast.Inspect(dict, func(x ast.Node) bool {
		if vs, ok := x.(*ast.ValueSpec); ok && len(vs.Names) == 1 && len(vs.Values) == 1 {
			if name, ok := makeSymbolArg(vs.Values[0]); ok {
				syms[vs.Names[0].Name] = name
			}
		}
		return true
	})
	pkgs, err := parser.ParseDir(fset, moduleDir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && fi.Name() != "symbols.go"
	}, 0)
	if err != nil {
		return nil, err
	}
	g := &klGraph{Defuns: map[string]map[string]bool{}, Seeds: map[string]bool{}}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			// One pass: Go declares before it uses, so the assignment that
			// holds a body is always recorded before the binding names it.
			assigns := map[string]ast.Expr{}
			v := klVisit{g: g, syms: syms, bodies: map[ast.Node]string{}, bound: map[ast.Node]bool{}}
			ast.Inspect(f, func(x ast.Node) bool {
				if as, ok := x.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
					if id, ok := as.Lhs[0].(*ast.Ident); ok {
						assigns[id.Name] = as.Rhs[0]
					}
					return true
				}
				call, ok := x.(*ast.CallExpr)
				if !ok || len(call.Args) < 4 {
					return true
				}
				fn, _ := call.Fun.(*ast.Ident)
				ns, _ := call.Args[1].(*ast.Ident)
				nameID, _ := call.Args[2].(*ast.Ident)
				if fn == nil || fn.Name != "Call" || ns == nil || ns.Name != "ns2_1set" || nameID == nil {
					return true
				}
				name, ok := syms[nameID.Name]
				if !ok {
					return true
				}
				body := call.Args[3]
				if id, ok := body.(*ast.Ident); ok {
					if rhs, ok := assigns[id.Name]; ok {
						body = rhs
					}
				}
				v.bodies[body] = name
				v.bound[nameID] = true
				g.Defuns[name] = map[string]bool{}
				return true
			})
			ast.Walk(v, f)
		}
	}
	return g, nil
}

func klReach(g *klGraph) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if _, ok := g.Defuns[n]; !ok || seen[n] {
			return
		}
		seen[n] = true
		for ref := range g.Defuns[n] {
			walk(ref)
		}
	}
	for s := range g.Seeds {
		walk(s)
	}
	return seen
}

// inlinedClosure is the declared subtraction plus its consequences: a name
// the port lowers inline is never looked up, so neither is whatever only it
// calls -- shen-go emits PrimIsSymbol at every call site of `symbol?`, so
// the kept defun `symbol?` is unreachable and so is shen.analyse-symbol?.
// Not a second declaration: derived from the SAME one over the emitted
// graph, and every name it adds is printed.
func inlinedClosure(g *klGraph, special map[string]bool) map[string]bool {
	return klReach(&klGraph{Defuns: g.Defuns, Seeds: special})
}

// userDefuns reads the manifest's fn= lines: the user program's own defuns,
// which live outside kernel.kl and so are never in the footprint.
func userDefuns(shakenDir string) map[string]bool {
	out := map[string]bool{}
	b, _ := os.ReadFile(filepath.Join(shakenDir, "yggdrasil.manifest.txt"))
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimPrefix(line, "fn="))
		if strings.HasPrefix(line, "fn=") && len(f) > 0 {
			out[f[0]] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ",")
}

// kernelDefunNames reads the defun NAMES out of a kernel.kl: on the shaken
// kernel that is the footprint the emitted graph is held against, and a
// count could only produce a delta for a person to explain. Odd segments of
// the split are inside a string literal and are skipped, because kernel.kl
// carries its own writer's source text -- line 889 of the full kernel is
// `"(defun fail () shen.fail!)"` inside shen.write-kl-h. KL strings have no
// backslash escape, so splitting on the quote is the whole lexer needed.
func kernelDefunNames(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for i, seg := range strings.Split(string(b), `"`) {
		if i%2 == 1 {
			continue
		}
		for _, tail := range strings.Split(seg, "(defun ")[1:] {
			if n := strings.IndexAny(tail, " \t\r\n()"); n > 0 {
				out[tail[:n]] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no defuns found in %s", path)
	}
	return out, nil
}

// specialFormsAudit holds the DECLARATION itself to account: a subtraction
// that is never checked is not reviewable, and a stale entry would silence a
// real residue with no output at all.
//
//	unknown  not a defun in the FULL kernel, so not a name this port lowers
//	         specially: stale or misspelled. A finding.
//	applied  in the footprint and NOT reachable: the subtraction did work.
//	unused   a real kernel defun not subtracted this run -- a property of the
//	         program, not of the port, so reported rather than failed.
//
// viaClosure is what inlinedClosure added on top of the declaration and the
// residue actually needed, so the verdict can name it separately.
func specialFormsAudit(foot, rs, special, accounted, fullFoot map[string]bool) (applied, viaClosure, unused, unknown []string) {
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
	for _, name := range sortedKeys(accounted) {
		if !special[name] && foot[name] && !rs[name] {
			viaClosure = append(viaClosure, name)
		}
	}
	return applied, viaClosure, unused, unknown
}

// klDelta is the whole accounting, as a pure function so it can be tested
// without a toolchain: `foot` is what the shake kept, `rs` what the emitted
// graph reaches, `special` the declared special forms with their
// inlinedClosure, `users` the user's own defuns. missing is a kept name the
// artifact cannot reach and nothing accounts for -- dead, or lowered
// specially and not declared; extra is a reached name the shake did not
// keep, so the slice is not closed.
func klDelta(foot, rs, special, users map[string]bool) (missing, extra []string) {
	for _, name := range sortedKeys(foot) {
		if !rs[name] && !special[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range sortedKeys(rs) {
		if !foot[name] && !users[name] && name != klInitialiser {
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
	say := func(format string, a ...any) { fmt.Printf("yggdrasil-scip-check: "+format+"\n", a...) }
	fail := func(what string, err error) int {
		fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: FAIL %s: %v\n", what, err)
		return 1
	}
	// The subtraction is DECLARED, not decided here: a target with no
	// `special_forms` key subtracts nothing, so its residue is a finding.
	var bd builder
	if builders, err := loadBuilders(); err == nil {
		bd = builders[*target]
	}
	specialNames := append([]string(nil), bd.SpecialForms...)
	sort.Strings(specialNames)
	special := map[string]bool{}
	for _, n := range specialNames {
		special[n] = true
	}
	start := time.Now()
	abs, _ := filepath.Abs(outdir)
	shakenDir := filepath.Join(abs, "shaken")
	fullDir := filepath.Join(abs, "full")

	// A* and A, same builder, same flags, different stage 1.
	for i, dir := range []string{shakenDir, fullDir} {
		leg := [2]string{"shaken", "full"}[i]
		if _, err := shakeMode(prog, dir, host, *evalStyle, true, shakeOpts{full: i == 1}); err != nil {
			return fail(leg+" stage 1", err)
		}
		runArgv, err := build(*target, dir, false)
		if err != nil {
			return fail(leg+" stage 2", err)
		}
		if runArgv == nil {
			fmt.Fprintf(os.Stderr, "yggdrasil-scip-check: SKIP target %s (a required tool is not on PATH)\n", *target)
			return 3
		}
	}

	klShaken, err := buildKLGraph(filepath.Join(shakenDir, "app-go"))
	if err != nil {
		return fail("reading the shaken module", err)
	}
	klFull, err := buildKLGraph(filepath.Join(fullDir, "app-go"))
	if err != nil {
		return fail("reading the full module", err)
	}
	foot, err := kernelDefunNames(filepath.Join(shakenDir, "kernel.kl"))
	if err != nil {
		return fail("reading the shaken footprint", err)
	}
	delete(foot, klInitialiser) // synthesised; the rules never put it in reach
	// Every defun the port has: what a declared special form must be one of.
	fullFoot, err := kernelDefunNames(filepath.Join(fullDir, "kernel.kl"))
	if err != nil {
		return fail("reading the full kernel", err)
	}
	rs := klReach(klShaken)
	rf := klReach(klFull)
	users := userDefuns(shakenDir)
	accounted := inlinedClosure(klShaken, special)
	missing, extra := klDelta(foot, rs, accounted, users)
	applied, viaClosure, unusedSpecial, unknownSpecial := specialFormsAudit(foot, rs, special, accounted, fullFoot)
	var klMissing []string // (2): A* included in A, by name
	for _, name := range sortedKeys(rs) {
		if !rf[name] {
			klMissing = append(klMissing, name)
		}
	}

	say("target=%s wall=%.1fs kl-level footprint=%d reachable=%d; full defuns=%d reachable=%d",
		*target, time.Since(start).Seconds(), len(foot), len(rs), len(klFull.Defuns), len(rf))
	// What was actually subtracted is `applied` plus `viaClosure`, not the
	// declaration: printing the declaration would claim more than was done.
	say("kl-level delta accounted: special_forms=%s called-only-from-them=%s user-defuns=%s initialise=1",
		joinOrNone(applied), joinOrNone(viaClosure), joinOrNone(sortedKeys(users)))
	say("special_forms declared=%d applied=%s unused=%s unknown=%s source=%s", len(specialNames),
		joinOrNone(applied), joinOrNone(unusedSpecial), joinOrNone(unknownSpecial), bd.SpecialFormsSource)
	if len(missing) == 0 && len(extra) == 0 && len(klMissing) == 0 && len(unknownSpecial) == 0 {
		say("OK footprint=%d reachable=%d accounted=%d", len(foot), len(rs), len(applied)+len(viaClosure))
		return 0
	}
	list := func(tag string, names []string) {
		for _, n := range names {
			say("  %s %s", tag, n)
		}
	}
	list("kl-delta-missing", missing)
	list("kl-delta-extra", extra)
	list("kl-missing-in-full", klMissing)
	list("special-forms-unknown", unknownSpecial)
	say("FAIL footprint=%d reachable=%d delta-missing=%d delta-extra=%d kl-missing-in-full=%d special-forms-unknown=%d",
		len(foot), len(rs), len(missing), len(extra), len(klMissing), len(unknownSpecial))
	return 1
}
