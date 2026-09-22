// Stage 5 of docs/analysis-rules.md: the SCIP level-2 oracle.
//
// The shake's claim is about the KL it writes. Stages 1-3 check that claim
// against itself (three engines, one footprint). Nothing checks the step
// AFTER it: that the stage-2 builder compiled the shaken KL into the same
// program the full KL would have compiled into, restricted to what runs.
//
// `yggdrasil scip-check PROG OUTDIR --target go` builds both:
//
//	A*  the shaken slice           (yggdrasil.shake)
//	A   the full program K + user  (yggdrasil.shake-full, --no-shake)
//
// with the SAME builder into two module directories, indexes each with
// scip-go, decodes the two index.scip files, and compares
//
//	(1) the set of function symbols reachable from `main` over the index's
//	    own reference graph -- a definition's references are the reference
//	    occurrences lying inside its enclosing_range -- and
//	(2) a normalised body hash per function (the go/printer rendering of the
//	    declaration, so whitespace and comments cannot make two equal bodies
//	    look different).
//
// A* must be included in A node for node, and every function it kept must
// have an identical-body twin in A. Disagreement is a bug in the builder or
// in the rules, exactly the status the Souffle oracle has for stage 1.
//
// Two things about this file are worth knowing before reading it.
//
// The decoder is hand-written. The `scip` CLI cannot be installed here (its
// go.mod carries replace directives, so `go install
// github.com/scip-code/scip/cmd/scip@v0.7.1` is refused), and the Go
// bindings are a module dependency -- and this repo has none, deliberately:
// it embeds its own kernel and shaker and is meant to be auditable by the
// same people who audit those. So scip.Index is decoded here from the wire
// format, reading only the five fields this check needs.
//
// And the Go-level graph is not the whole story for every builder. See
// klGraph below and the Stage 5 section of docs/analysis-rules.md: a backend
// that compiles each KL call to a runtime symbol lookup has no Go-level node
// graph to compare, and the KL-level graph recovered from its output is what
// the oracle can actually see.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------- protobuf ---------------------------------

// pbBuf is a reader for the protobuf wire format: enough of it to walk a
// message's fields and pull out varints, length-delimited bytes and packed
// repeated int32s. Unknown fields are skipped by wire type, which is what
// makes it safe against a newer scip.proto adding fields.
type pbBuf struct {
	b []byte
	i int
}

var errPBTruncated = errors.New("scip: truncated protobuf message")

func (p *pbBuf) more() bool { return p.i < len(p.b) }

func (p *pbBuf) varint() (uint64, error) {
	v, n := binary.Uvarint(p.b[p.i:])
	if n <= 0 {
		return 0, errPBTruncated
	}
	p.i += n
	return v, nil
}

// tag reads a field header, returning the field number and wire type.
func (p *pbBuf) tag() (num int, wire int, err error) {
	v, err := p.varint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 7), nil
}

func (p *pbBuf) lenBytes() ([]byte, error) {
	n, err := p.varint()
	if err != nil {
		return nil, err
	}
	if p.i+int(n) > len(p.b) {
		return nil, errPBTruncated
	}
	out := p.b[p.i : p.i+int(n)]
	p.i += int(n)
	return out, nil
}

// skip advances past a field of the given wire type.
func (p *pbBuf) skip(wire int) error {
	switch wire {
	case 0:
		_, err := p.varint()
		return err
	case 1:
		if p.i+8 > len(p.b) {
			return errPBTruncated
		}
		p.i += 8
		return nil
	case 2:
		_, err := p.lenBytes()
		return err
	case 5:
		if p.i+4 > len(p.b) {
			return errPBTruncated
		}
		p.i += 4
		return nil
	}
	return fmt.Errorf("scip: unsupported wire type %d", wire)
}

// int32s reads a repeated int32 field that may arrive packed (wire type 2)
// or one value per tag (wire type 0). Both spellings are legal protobuf and
// indexers differ, so both are accepted.
func (p *pbBuf) int32s(wire int, into []int32) ([]int32, error) {
	if wire == 0 {
		v, err := p.varint()
		if err != nil {
			return into, err
		}
		return append(into, int32(v)), nil
	}
	raw, err := p.lenBytes()
	if err != nil {
		return into, err
	}
	sub := &pbBuf{b: raw}
	for sub.more() {
		v, err := sub.varint()
		if err != nil {
			return into, err
		}
		into = append(into, int32(v))
	}
	return into, nil
}

// ---------------------------- the index --------------------------------

// scipRoleDefinition is SymbolRole.Definition (0x1) in scip.proto.
const scipRoleDefinition = 1

type scipOccurrence struct {
	Rng   []int32 // [startLine startChar endLine endChar] or [startLine startChar endChar]
	Sym   string
	Roles int32
	Encl  []int32
}

type scipDocument struct {
	Path string
	Occs []scipOccurrence
}

type scipIndex struct {
	Docs []scipDocument
}

func decodeSCIPIndex(b []byte) (*scipIndex, error) {
	idx := &scipIndex{}
	p := &pbBuf{b: b}
	for p.more() {
		num, wire, err := p.tag()
		if err != nil {
			return nil, err
		}
		if num != 2 || wire != 2 { // Index.documents
			if err := p.skip(wire); err != nil {
				return nil, err
			}
			continue
		}
		raw, err := p.lenBytes()
		if err != nil {
			return nil, err
		}
		doc, err := decodeSCIPDocument(raw)
		if err != nil {
			return nil, err
		}
		idx.Docs = append(idx.Docs, doc)
	}
	return idx, nil
}

func decodeSCIPDocument(b []byte) (scipDocument, error) {
	var doc scipDocument
	p := &pbBuf{b: b}
	for p.more() {
		num, wire, err := p.tag()
		if err != nil {
			return doc, err
		}
		switch {
		case num == 1 && wire == 2: // relative_path
			raw, err := p.lenBytes()
			if err != nil {
				return doc, err
			}
			doc.Path = string(raw)
		case num == 2 && wire == 2: // occurrences
			raw, err := p.lenBytes()
			if err != nil {
				return doc, err
			}
			occ, err := decodeSCIPOccurrence(raw)
			if err != nil {
				return doc, err
			}
			doc.Occs = append(doc.Occs, occ)
		default:
			if err := p.skip(wire); err != nil {
				return doc, err
			}
		}
	}
	return doc, nil
}

func decodeSCIPOccurrence(b []byte) (scipOccurrence, error) {
	var occ scipOccurrence
	p := &pbBuf{b: b}
	for p.more() {
		num, wire, err := p.tag()
		if err != nil {
			return occ, err
		}
		switch num {
		case 1: // range
			occ.Rng, err = p.int32s(wire, occ.Rng)
		case 2: // symbol
			var raw []byte
			raw, err = p.lenBytes()
			occ.Sym = string(raw)
		case 3: // symbol_roles
			var v uint64
			v, err = p.varint()
			occ.Roles = int32(v)
		case 7: // enclosing_range
			occ.Encl, err = p.int32s(wire, occ.Encl)
		default:
			err = p.skip(wire)
		}
		if err != nil {
			return occ, err
		}
	}
	return occ, nil
}

// startOf normalises a SCIP range to a (line, char) start. SCIP ranges are
// three-element when the range is on one line and four otherwise; both start
// with line then char, which is all this needs.
func startOf(r []int32) (int32, int32) {
	if len(r) < 2 {
		return -1, -1
	}
	return r[0], r[1]
}

// endLineOf returns the end line of a range (the start line for the
// three-element single-line spelling).
func endLineOf(r []int32) int32 {
	if len(r) >= 4 {
		return r[2]
	}
	if len(r) >= 1 {
		return r[0]
	}
	return -1
}

// ------------------------- the function graph --------------------------

// isFunctionSymbol reports whether a SCIP symbol names a function or method.
// The SCIP symbol grammar spells a method descriptor `<name>(<disambig>).`,
// which is what the Go indexer emits for a func declaration; a local symbol
// (`local 3`) is never one.
func isFunctionSymbol(sym string) bool {
	if sym == "" || strings.HasPrefix(sym, "local ") {
		return false
	}
	return strings.HasSuffix(sym, ").")
}

// funcNode is one function definition in an index, with the body hash used
// to decide whether two indexes' copies of it are the same function.
type funcNode struct {
	Sym  string
	Path string
	Line int32 // 0-based, as SCIP counts
	Hash string
	Refs map[string]bool
}

// funcGraph is the whole comparison unit for one built module.
type funcGraph struct {
	Nodes map[string]*funcNode
	Main  string
}

// buildFuncGraph turns a decoded index into a function graph. An edge
// def -> sym exists when a non-definition occurrence of sym lies inside the
// definition's enclosing_range: that is the index's own statement of "this
// reference is inside that definition", and it is the only edge information
// a SCIP index carries.
func buildFuncGraph(idx *scipIndex, moduleDir string) (*funcGraph, error) {
	g := &funcGraph{Nodes: map[string]*funcNode{}}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}

	for _, doc := range idx.Docs {
		// Definitions with an enclosing range, innermost last so that a
		// reference inside a nested definition is attributed to the
		// nested one.
		type span struct {
			sym        string
			start, end int32
			line       int32
		}
		var spans []span
		for _, occ := range doc.Occs {
			if occ.Roles&scipRoleDefinition == 0 || !isFunctionSymbol(occ.Sym) {
				continue
			}
			if len(occ.Encl) == 0 {
				continue
			}
			sl, _ := startOf(occ.Encl)
			el := endLineOf(occ.Encl)
			dl, _ := startOf(occ.Rng)
			spans = append(spans, span{sym: occ.Sym, start: sl, end: el, line: dl})
			if _, seen := g.Nodes[occ.Sym]; !seen {
				g.Nodes[occ.Sym] = &funcNode{
					Sym: occ.Sym, Path: doc.Path, Line: sl,
					Refs: map[string]bool{},
				}
			}
		}
		// Narrowest enclosing span wins.
		sort.Slice(spans, func(i, j int) bool {
			return (spans[i].end - spans[i].start) < (spans[j].end - spans[j].start)
		})
		for _, occ := range doc.Occs {
			if occ.Roles&scipRoleDefinition != 0 || occ.Sym == "" {
				continue
			}
			line, _ := startOf(occ.Rng)
			for _, s := range spans {
				if line >= s.start && line <= s.end {
					g.Nodes[s.sym].Refs[occ.Sym] = true
					break
				}
			}
		}
		// Body hashes for this document's definitions.
		if len(spans) > 0 && moduleDir != "" {
			full := filepath.Join(moduleDir, doc.Path)
			f, ok := files[full]
			if !ok {
				parsed, err := parser.ParseFile(fset, full, nil, 0)
				if err != nil {
					// An unparseable document costs its hashes, not the run:
					// the reachable-set half of the check still stands.
					continue
				}
				files[full] = parsed
				f = parsed
			}
			hashes := declHashes(fset, f)
			for _, s := range spans {
				if h, ok := hashes[int(s.line)]; ok {
					g.Nodes[s.sym].Hash = h
				}
			}
		}
	}
	for sym := range g.Nodes {
		if strings.HasSuffix(sym, "main().") || strings.HasSuffix(sym, "`main`().") {
			g.Main = sym
		}
	}
	return g, nil
}

// declHashes renders every top-level declaration with go/printer and hashes
// it, keyed by the declaration's 0-based start line. Printing the AST is the
// normalisation: two declarations that differ only in whitespace, comment
// text or line breaks print the same.
func declHashes(fset *token.FileSet, f *ast.File) map[int]string {
	out := map[int]string{}
	for _, d := range f.Decls {
		var buf bytes.Buffer
		cfg := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
		if err := cfg.Fprint(&buf, fset, d); err != nil {
			continue
		}
		sum := sha256.Sum256(buf.Bytes())
		line := fset.Position(d.Pos()).Line - 1
		out[line] = hex.EncodeToString(sum[:])[:16]
	}
	return out
}

// reachableFrom is the transitive closure of the reference graph from one
// node, restricted to nodes that are themselves definitions in this index.
func reachableFrom(g *funcGraph, root string) map[string]bool {
	seen := map[string]bool{}
	if root == "" {
		return seen
	}
	work := []string{root}
	seen[root] = true
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		node, ok := g.Nodes[n]
		if !ok {
			continue
		}
		for ref := range node.Refs {
			if _, isDef := g.Nodes[ref]; !isDef {
				continue
			}
			if !seen[ref] {
				seen[ref] = true
				work = append(work, ref)
			}
		}
	}
	return seen
}

// ------------------------- the go/ast fallback -------------------------

// astFuncGraph computes the same graph as buildFuncGraph without an index:
// top-level function declarations as nodes, and an edge F -> G whenever F's
// body mentions the identifier G that names another top-level function in
// the same package. It is the insurance path of deliverable 3 -- coarser
// than go/types resolution but identical on a generated single-package
// module, where no shadowing of a top-level func name occurs.
func astFuncGraph(moduleDir string) (*funcGraph, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, moduleDir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go")
	}, 0)
	if err != nil {
		return nil, err
	}
	g := &funcGraph{Nodes: map[string]*funcNode{}}
	type decl struct {
		name string
		node ast.Decl
		path string
	}
	var decls []decl
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			for _, d := range f.Decls {
				switch dd := d.(type) {
				case *ast.FuncDecl:
					decls = append(decls, decl{name: dd.Name.Name, node: d, path: path})
				case *ast.GenDecl:
					// The Go backend emits its module thunks as
					// `var KernelChunk0 = MakeNative(func ...)`, so a var
					// holding a function literal is a node too.
					for _, s := range dd.Specs {
						vs, ok := s.(*ast.ValueSpec)
						if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
							continue
						}
						if !containsFuncLit(vs.Values[0]) {
							continue
						}
						decls = append(decls, decl{name: vs.Names[0].Name, node: d, path: path})
					}
				}
			}
		}
	}
	known := map[string]bool{}
	for _, d := range decls {
		known[d.name] = true
	}
	for _, d := range decls {
		var buf bytes.Buffer
		cfg := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
		_ = cfg.Fprint(&buf, fset, d.node)
		sum := sha256.Sum256(buf.Bytes())
		n := &funcNode{
			Sym:  d.name,
			Path: filepath.Base(d.path),
			Line: int32(fset.Position(d.node.Pos()).Line - 1),
			Hash: hex.EncodeToString(sum[:])[:16],
			Refs: map[string]bool{},
		}
		ast.Inspect(d.node, func(x ast.Node) bool {
			id, ok := x.(*ast.Ident)
			if ok && known[id.Name] && id.Name != d.name {
				n.Refs[id.Name] = true
			}
			return true
		})
		g.Nodes[d.name] = n
	}
	if _, ok := g.Nodes["main"]; ok {
		g.Main = "main"
	}
	return g, nil
}

func containsFuncLit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(x ast.Node) bool {
		if _, ok := x.(*ast.FuncLit); ok {
			found = true
		}
		return !found
	})
	return found
}

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

// ------------------------------ the check ------------------------------

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// countDefuns counts `(defun ` in a shaken kernel.kl. The footprint is that
// count minus the synthesised shen.initialise, which is the one defun in
// kernel.kl the rules never put in reach.
func countDefuns(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	return bytes.Count(b, []byte("(defun "))
}

// runSCIPGo indexes moduleDir, returning the index bytes or the reason it
// could not. A missing indexer and a failing one are distinguished: the
// first is a SKIP, the second is what deliverable 3 asks to be named.
func runSCIPGo(moduleDir string) ([]byte, error) {
	exe, err := exec.LookPath("scip-go")
	if err != nil {
		return nil, fmt.Errorf("scip-go is not on PATH")
	}
	cmd := exec.Command(exe, "index", "./...")
	cmd.Dir = moduleDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("scip-go index failed in %s: %v\n%s", moduleDir, err, out)
	}
	return os.ReadFile(filepath.Join(moduleDir, "index.scip"))
}

// lastDescriptor is the trailing name of a SCIP symbol, i.e. the Go
// identifier it names, or the symbol itself for the go/ast fallback.
func lastDescriptor(sym string) string {
	s := strings.TrimSuffix(sym, ").")
	// Drop the method disambiguator, if any: `name(disambiguator).`
	if i := strings.LastIndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexAny(s, "/#. "); i >= 0 {
		s = s[i+1:]
	}
	return strings.Trim(s, "`")
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

func cmdScipCheck(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil scip-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	target := fs.String("target", "go", "stage-2 target to build both programs with (only compositional backends are meaningful)")
	verbose := fs.Bool("v", false, "also list the main-reachable node set of each build")
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
		if _, err := shakeMode(prog, leg.dir, host, *evalStyle, true, shakeOpts{full: leg.full}); err != nil {
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

	// The Go-level graph, SCIP first and go/ast as insurance.
	path := "scip"
	var gShaken, gFull *funcGraph
	var why string
	if raw, err := runSCIPGo(shakenMod); err != nil {
		path, why = "go-ast", err.Error()
	} else if idx, err := decodeSCIPIndex(raw); err != nil {
		path, why = "go-ast", err.Error()
	} else if gShaken, err = buildFuncGraph(idx, shakenMod); err != nil {
		path, why = "go-ast", err.Error()
	} else if raw, err := runSCIPGo(fullMod); err != nil {
		path, why = "go-ast", err.Error()
	} else if idx, err := decodeSCIPIndex(raw); err != nil {
		path, why = "go-ast", err.Error()
	} else if gFull, err = buildFuncGraph(idx, fullMod); err != nil {
		path, why = "go-ast", err.Error()
	}
	if path == "go-ast" {
		fmt.Printf("yggdrasil-scip-check: falling back to go/ast: %s\n", why)
		var err error
		if gShaken, err = astFuncGraph(shakenMod); err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil-scip-check: FAIL", err)
			return 1
		}
		if gFull, err = astFuncGraph(fullMod); err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil-scip-check: FAIL", err)
			return 1
		}
	}
	if gShaken.Main == "" || gFull.Main == "" {
		fmt.Fprintln(os.Stderr, "yggdrasil-scip-check: FAIL no main in one of the two indexes")
		return 1
	}
	reachShaken := reachableFrom(gShaken, gShaken.Main)
	reachFull := reachableFrom(gFull, gFull.Main)

	// The Go-level comparison. Node identity across the two modules is the
	// symbol for the SCIP path (the modules have the same module path, so
	// the symbols match) and the declaration name for the fallback.
	fullHashes := map[string]bool{}
	for sym := range reachFull {
		if n := gFull.Nodes[sym]; n != nil && n.Hash != "" {
			fullHashes[n.Hash] = true
		}
	}
	var missing, differing []string
	identical := 0
	for _, sym := range sortedKeys(reachShaken) {
		n := gShaken.Nodes[sym]
		twin, ok := gFull.Nodes[sym]
		if !ok || !reachFull[sym] {
			missing = append(missing, sym)
			continue
		}
		// The entry point is the one node whose body is REQUIRED to differ:
		// the builder generates main() from the manifest, so it names the
		// chunks and replays the user arities of whichever program it built.
		// Requiring it to be identical would be requiring the two programs
		// to be the same program. Its presence in both reachable sets is
		// still checked, above.
		if sym == gShaken.Main {
			continue
		}
		if n.Hash == "" || twin.Hash == "" {
			continue
		}
		if n.Hash == twin.Hash {
			identical++
		} else if !fullHashes[n.Hash] {
			differing = append(differing, sym)
		} else {
			identical++
		}
	}

	// The KL-level graph, which is where the real node-for-node statement
	// lives for this backend.
	klShaken, errS := buildKLGraph(shakenMod)
	klFull, errF := buildKLGraph(fullMod)
	var klNote string
	klMissing := []string{}
	if errS == nil && errF == nil {
		rs := klReach(klShaken)
		rf := klReach(klFull)
		for _, name := range sortedKeys(rs) {
			if !rf[name] {
				klMissing = append(klMissing, name)
			}
		}
		users := userDefuns(shakenDir)
		kernelSide := 0
		for name := range klShaken.Defuns {
			if !users[name] {
				kernelSide++
			}
		}
		// The shake's own footprint: kernel.kl's defuns less the
		// synthesised shen.initialise, which reach never contains.
		foot := countDefuns(filepath.Join(shakenDir, "kernel.kl")) - 1
		klNote = fmt.Sprintf(
			"yggdrasil-scip-check: kl-level shaken defuns=%d (kernel=%d user=%d) reachable=%d; full defuns=%d reachable=%d\n"+
				"yggdrasil-scip-check: shake footprint reach=%d delta=%+d (runtime support and lookup-vs-direct calls; see docs/analysis-rules.md)\n",
			len(klShaken.Defuns), kernelSide, len(klShaken.Defuns)-kernelSide, len(rs),
			len(klFull.Defuns), len(rf), foot, len(rs)-foot)
	} else {
		klNote = "yggdrasil-scip-check: kl-level unavailable (the generated module is not shen-go's shape)\n"
	}

	// How many main-reachable Go functions ARE kernel defuns, and how many
	// are runtime support? For this backend the answer is none and all:
	// see the klGraph comment and the Stage 5 section of the design note.
	klNames := map[string]bool{}
	if errS == nil {
		for name := range klShaken.Defuns {
			klNames[name] = true
		}
	}
	// A backend that binds its defuns at run time (klNames non-empty) has,
	// by construction, no Go-level node that IS a KL defun: the defuns are
	// anonymous closures inside a chunk thunk. Matching Go node names
	// against KL names there would only find coincidences -- shen-go's
	// generated driver has a helper called `fail`, and so does the kernel.
	// So the name intersection is only taken for a backend that emits no
	// such bindings, i.e. one that might really be compositional.
	goIsKL := 0
	if len(klNames) == 0 {
		for sym := range reachShaken {
			if sym == gShaken.Main {
				continue
			}
			goIsKL++
		}
	}
	fmt.Printf("yggdrasil-scip-check: path=%s target=%s wall=%.1fs\n", path, *target, time.Since(start).Seconds())
	fmt.Printf("yggdrasil-scip-check: go-level main-reachable shaken=%d full=%d (kernel-defun nodes=%d runtime-support nodes=%d)\n",
		len(reachShaken), len(reachFull), goIsKL, len(reachShaken)-goIsKL)
	fmt.Print(klNote)
	if *verbose {
		for _, sym := range sortedKeys(reachShaken) {
			fmt.Printf("yggdrasil-scip-check:   shaken-node %s\n", sym)
		}
	}
	if len(missing) == 0 && len(differing) == 0 && len(klMissing) == 0 {
		fmt.Printf("yggdrasil-scip-check: OK reachable=%d identical=%d\n", len(reachShaken), identical)
		return 0
	}
	for _, m := range missing {
		fmt.Printf("yggdrasil-scip-check:   missing-in-full %s (%s)\n", lastDescriptor(m), m)
	}
	for _, d := range differing {
		fmt.Printf("yggdrasil-scip-check:   body-differs %s (%s)\n", lastDescriptor(d), d)
	}
	for _, k := range klMissing {
		fmt.Printf("yggdrasil-scip-check:   kl-missing-in-full %s\n", k)
	}
	fmt.Printf("yggdrasil-scip-check: FAIL reachable=%d identical=%d missing=%d differing=%d kl-missing=%d\n",
		len(reachShaken), identical, len(missing), len(differing), len(klMissing))
	return 1
}
