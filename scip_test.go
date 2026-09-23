package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---- the delta accounting, tested without a toolchain ----

// The footprint and reach sets below are the real ones for tests/fib.shen
// --target go, cut down to the names that matter: `do` and `not` are kept by
// the shake but lowered by shen-go without a symbol lookup, `fib` is the
// user's own defun (it lives outside kernel.kl), and shen.initialise is
// synthesised. Everything else is an ordinary kernel defun the artifact's
// own graph reaches.
func fibFootprint() map[string]bool {
	return map[string]bool{
		"do": true, "not": true,
		"shen.app": true, "shen.str": true, "hd": true,
	}
}

func fibReach() map[string]bool {
	return map[string]bool{
		"shen.app": true, "shen.str": true, "hd": true,
		"fib": true, "shen.initialise": true,
	}
}

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// TestKLDelta is the negative test the check was missing. The old code
// printed `delta=%+d` and never compared anything, so no input could make it
// fail. These cases fail on the inputs that should fail.
func TestKLDelta(t *testing.T) {
	foot, rs := fibFootprint(), fibReach()
	users := set("fib")

	t.Run("declared special forms make the residue empty", func(t *testing.T) {
		missing, extra := klDelta(foot, rs, set("do", "not"), users)
		if len(missing) != 0 || len(extra) != 0 {
			t.Fatalf("residue should be empty: missing=%v extra=%v", missing, extra)
		}
	})

	// The point of the whole change: dropping a name from special_forms must
	// turn the residue non-empty. If this passes with `do` removed, the
	// subtraction is not being asserted.
	t.Run("dropping do from special_forms is a finding", func(t *testing.T) {
		missing, extra := klDelta(foot, rs, set("not"), users)
		if len(extra) != 0 {
			t.Fatalf("extra should still be empty, got %v", extra)
		}
		if len(missing) != 1 || missing[0] != "do" {
			t.Fatalf("missing = %v, want exactly [do]", missing)
		}
	})

	t.Run("declaring nothing reports every special form", func(t *testing.T) {
		missing, _ := klDelta(foot, rs, nil, users)
		if strings.Join(missing, ",") != "do,not" {
			t.Fatalf("missing = %v, want [do not]", missing)
		}
	})

	// A name the artifact reaches that the shake did not keep means the
	// slice is not closed, and is not excused by being a special form.
	t.Run("an unaccounted reachable name is extra", func(t *testing.T) {
		rs2 := fibReach()
		rs2["shen.intern"] = true
		missing, extra := klDelta(foot, rs2, set("do", "not"), users)
		if len(missing) != 0 {
			t.Fatalf("missing = %v, want none", missing)
		}
		if len(extra) != 1 || extra[0] != "shen.intern" {
			t.Fatalf("extra = %v, want exactly [shen.intern]", extra)
		}
	})

	// The user's defuns and shen.initialise are the two other declared
	// subtractions; forgetting either must show up rather than be silent.
	t.Run("a user defun is not extra, an undeclared one is", func(t *testing.T) {
		if _, extra := klDelta(foot, rs, set("do", "not"), users); len(extra) != 0 {
			t.Fatalf("fib and shen.initialise should be accounted for, got %v", extra)
		}
		if _, extra := klDelta(foot, rs, set("do", "not"), nil); strings.Join(extra, ",") != "fib" {
			t.Fatalf("with no user defuns declared, fib should be extra, got %v", extra)
		}
	})

	t.Run("residues are sorted and deterministic", func(t *testing.T) {
		missing, _ := klDelta(set("z", "a", "m"), set(), nil, nil)
		if strings.Join(missing, ",") != "a,m,z" {
			t.Fatalf("missing = %v, want sorted", missing)
		}
	})
}

// TestInlinedClosure pins the consequence of a declared special form. shen-go
// emits PrimIsSymbol at every call site of `symbol?`, so the kept defun
// `symbol?` is never looked up and neither is shen.analyse-symbol?, which
// only it calls. Subtracting the declared name without its subtree turns the
// subtree into a residue; subtracting more than the subtree would hide one.
func TestInlinedClosure(t *testing.T) {
	g := &klGraph{
		Defuns: map[string]map[string]bool{
			"symbol?":              set("shen.analyse-symbol?"),
			"shen.analyse-symbol?": set("shen.alphanums?"),
			"shen.alphanums?":      set(),
			"hd":                   set(),
		},
		Seeds: set("hd"),
	}
	got := sortedKeys(inlinedClosure(g, set("symbol?")))
	want := "shen.alphanums?,shen.analyse-symbol?,symbol?"
	if strings.Join(got, ",") != want {
		t.Fatalf("closure = %v, want %s", got, want)
	}
	// Nothing outside the subtree is swept in.
	if inlinedClosure(g, set("symbol?"))["hd"] {
		t.Error("hd is reached by an ordinary lookup and must not be subtracted")
	}
	// With nothing declared there is no closure, so the whole subtree is a
	// residue: that is what makes the declaration load-bearing.
	foot := set("symbol?", "shen.analyse-symbol?", "shen.alphanums?", "hd")
	missing, _ := klDelta(foot, klReach(g), inlinedClosure(g, nil), nil)
	if strings.Join(missing, ",") != "shen.alphanums?,shen.analyse-symbol?,symbol?" {
		t.Fatalf("missing = %v, want the whole subtree", missing)
	}
}

// TestKernelDefunNames checks the footprint parser against the shape a
// shaken kernel.kl actually has, including the synthesised initialiser the
// caller then deletes.
func TestKernelDefunNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.kl")
	// The last defun is the shape of kernel.kl line 889: the KL writer's own
	// source text, quoted. A scan that does not know about string literals
	// reports `fail` as a kept defun and the footprint gates the exit code,
	// so that is a phantom kl-delta-missing in any slice that keeps
	// shen.write-kl-h without fail.
	const src = `(defun do (V1 V2) V2)
(defun not (V1) (if V1 false true))
(defun shen.app (V1 V2 V3) (cn (shen.str V1) V2))
(defun shen.initialise () (do (shen.load-kernel) ()))
(defun shen.write-kl-h (V1) (shen.prhush "(defun fail () shen.fail!)" V1))
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := kernelDefunNames(path)
	if err != nil {
		t.Fatalf("kernelDefunNames: %v", err)
	}
	want := set("do", "not", "shen.app", "shen.initialise", "shen.write-kl-h")
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", sortedKeys(got), sortedKeys(want))
	}
	for n := range want {
		if !got[n] {
			t.Errorf("missing name %q (got %v)", n, sortedKeys(got))
		}
	}
	if got["fail"] {
		t.Error("`fail` came from a string literal, not from a defun: the scan is not skipping strings")
	}
	// A file with no defuns is an error, not an empty footprint: an empty
	// footprint would make the delta check vacuously pass.
	empty := filepath.Join(t.TempDir(), "kernel.kl")
	if err := os.WriteFile(empty, []byte("(set *x* 1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := kernelDefunNames(empty); err == nil {
		t.Error("a kernel.kl with no defuns must be an error, not an empty set")
	}
}

// klModule writes a module directory in the shape yggdrasil-build emits, so
// buildKLGraph can be tested without a Shen host or a Go toolchain.
func klModule(t *testing.T, kernel string) string {
	t.Helper()
	dir := t.TempDir()
	const symbols = `package main

var symdo = MakeSymbol("do")
var symfoo = MakeSymbol("foo")
var symbar = MakeSymbol("bar")
var symquoted = MakeSymbol("quoted")
var symtable = MakeSymbol("table")
var symshen_4initialise = MakeSymbol("shen.initialise")
`
	if err := os.WriteFile(filepath.Join(dir, "symbols.go"), []byte(symbols), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kernel_00.go"), []byte(kernel), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBuildKLGraph covers the three things the recovered graph gets wrong if
// anyone simplifies it back: the body is behind a temporary, a bare symbol in
// constructed data is an edge, and the synthesised initialiser's quoted names
// are not.
func TestBuildKLGraph(t *testing.T) {
	// `foo`'s body is assigned to tmp1 and the binding only names it; `foo`
	// mentions `bar` in call position and `quoted` in data position. The
	// initialiser calls `foo` and quotes `table`, the shape trim-top writes
	// when it rebuilds the arity table against the footprint.
	const kernel = `package main

var KernelChunk0 = MakeNative(func(__e *ControlFlow) {
	tmp1 := MakeNative(func(__e *ControlFlow) {
		tmp2 := Call(__e, PrimFunc(symbar), V1)
		__e.Return(PrimCons(symquoted, tmp2))
	}, 1)
	tmp3 := Call(__e, ns2_1set, symfoo, tmp1)
	_ = tmp3
	tmp4 := MakeNative(func(__e *ControlFlow) {
		__e.Return(PrimCons(symtable, Call(__e, PrimFunc(symfoo))))
	}, 0)
	tmp5 := Call(__e, ns2_1set, symshen_4initialise, tmp4)
	_ = tmp5
	tmp6 := MakeNative(func(__e *ControlFlow) { __e.Return(Nil) }, 0)
	_ = Call(__e, ns2_1set, symbar, tmp6)
	_ = Call(__e, ns2_1set, symquoted, tmp6)
	_ = Call(__e, ns2_1set, symtable, tmp6)
	run(&e, "shen.initialise", PrimFunc(MakeSymbol("shen.initialise")))
})
`
	g, err := buildKLGraph(klModule(t, kernel))
	if err != nil {
		t.Fatalf("buildKLGraph: %v", err)
	}
	// Following the tmp1 hop is what gives foo any edges at all.
	if got := strings.Join(sortedKeys(g.Defuns["foo"]), ","); got != "bar,quoted" {
		t.Errorf("foo's edges = %v, want [bar quoted]: the body is behind a temporary", got)
	}
	// A bare symbol in constructed data is an edge, as analysis.dl D1 says.
	if !g.Defuns["foo"]["quoted"] {
		t.Error("a bare symbol in constructed data must be an edge (analysis.dl D1)")
	}
	// The initialiser calls foo and quotes table; only the call is an edge.
	if !g.Defuns[klInitialiser]["foo"] {
		t.Error("the initialiser's call to foo must be an edge")
	}
	if g.Defuns[klInitialiser]["table"] {
		t.Error("the initialiser quotes `table` against the footprint (D7): quoting it is not an edge")
	}
	// main.go's MakeSymbol entry point is what makes the initialiser a seed.
	if !g.Seeds[klInitialiser] {
		t.Errorf("shen.initialise must be seeded by the driver's entry point, seeds=%v", sortedKeys(g.Seeds))
	}
	// And so the reach set is the initialiser's subtree, not everything.
	if got := strings.Join(sortedKeys(klReach(g)), ","); got != "bar,foo,quoted,shen.initialise" {
		t.Errorf("reach = %v, want [bar foo quoted shen.initialise]; `table` is only quoted", got)
	}
}

// TestGoBuilderDeclaresSpecialForms pins the declaration the check subtracts
// by. Without it the shaken footprint cannot be accounted for, and a silent
// change to builders.json would make the check fail rather than lie -- but
// this names the reason directly.
func TestGoBuilderDeclaresSpecialForms(t *testing.T) {
	builders, err := loadBuilders()
	if err != nil {
		t.Fatalf("loadBuilders: %v", err)
	}
	bd, ok := builders["go"]
	if !ok {
		t.Fatal("no go builder")
	}
	// The declaration is a CLASS -- every kernel defun shen-go lowers without
	// a symbol lookup -- not the residue of whichever fixture was run last.
	// It is `do` (the one parse head in src/compiler.shen that is also a
	// kernel defun) plus the whole intersection of codegen.go's shenPrimitive
	// table with kernel.kl's defun names. Fitting it to one fixture is what
	// turned scip-check red on metaeval and tc-interp.
	want := []string{"do", "integer?", "not", "read-file-as-bytelist", "read-file-as-string", "symbol?", "variable?"}
	got := append([]string(nil), bd.SpecialForms...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("special_forms = %v, want %v", got, want)
	}
	if bd.SpecialFormsSource == "" {
		t.Fatal("special_forms without a special_forms_source is an undeclared subtraction")
	}
	// A source that does not cite a line of the port is not a citation.
	for _, n := range want {
		if !strings.Contains(bd.SpecialFormsSource, n) {
			t.Errorf("special_forms_source does not account for %q", n)
		}
	}
	if !strings.Contains(bd.SpecialFormsSource, "codegen.go:") || !strings.Contains(bd.SpecialFormsSource, "compiler.shen:") {
		t.Errorf("special_forms_source cites no shen-go file:line: %q", bd.SpecialFormsSource)
	}
}

// TestSpecialFormsAudit covers the third residue: the declaration itself.
// klDelta subtracts every declared name without asking whether the name is
// real, so a stale or misspelled entry would silence a finding with no
// output at all. specialFormsAudit is what says so.
func TestSpecialFormsAudit(t *testing.T) {
	foot, rs := fibFootprint(), fibReach()
	// The full kernel has every name the port could possibly lower.
	full := set("do", "not", "symbol?", "variable?", "integer?", "shen.app", "shen.str", "hd")

	t.Run("applied names are the ones that did work", func(t *testing.T) {
		applied, via, unused, unknown := specialFormsAudit(foot, rs, set("do", "not", "symbol?"), nil, full)
		if strings.Join(applied, ",") != "do,not" {
			t.Errorf("applied = %v, want [do not]", applied)
		}
		// symbol? is a real kernel defun this slice simply did not keep.
		if strings.Join(unused, ",") != "symbol?" {
			t.Errorf("unused = %v, want [symbol?]", unused)
		}
		if len(unknown) != 0 || len(via) != 0 {
			t.Errorf("unknown = %v, viaClosure = %v, want none", unknown, via)
		}
	})

	t.Run("a declared name the artifact reaches anyway is unused, not applied", func(t *testing.T) {
		applied, _, unused, _ := specialFormsAudit(foot, rs, set("hd"), nil, full)
		if len(applied) != 0 {
			t.Errorf("applied = %v, want none: hd is reached by an ordinary lookup", applied)
		}
		if strings.Join(unused, ",") != "hd" {
			t.Errorf("unused = %v, want [hd]", unused)
		}
	})

	// The failing case: a declaration that no longer names a kernel defun.
	t.Run("a stale declaration is a finding", func(t *testing.T) {
		_, _, _, unknown := specialFormsAudit(foot, rs, set("do", "not", "nto"), nil, full)
		if strings.Join(unknown, ",") != "nto" {
			t.Fatalf("unknown = %v, want exactly [nto]", unknown)
		}
	})

	// With no full footprint to check against, nothing can be called stale.
	t.Run("no full kernel means no unknown verdict", func(t *testing.T) {
		if _, _, _, unknown := specialFormsAudit(foot, rs, set("nto"), nil, nil); len(unknown) != 0 {
			t.Errorf("unknown = %v, want none without a full kernel", unknown)
		}
	})

	// What the closure added is reported apart from what was declared, so
	// the verdict never credits the declaration with more than it did.
	t.Run("the closure is reported apart from the declaration", func(t *testing.T) {
		accounted := set("do", "not", "shen.str")
		applied, via, _, _ := specialFormsAudit(foot, rs, set("do", "not"), accounted, full)
		if strings.Join(applied, ",") != "do,not" {
			t.Errorf("applied = %v, want [do not]", applied)
		}
		// shen.str is reachable here, so the closure did no work on it.
		if len(via) != 0 {
			t.Errorf("viaClosure = %v, want none: shen.str is reachable", via)
		}
		rs2 := fibReach()
		delete(rs2, "shen.str")
		_, via, _, _ = specialFormsAudit(foot, rs2, set("do", "not"), accounted, full)
		if strings.Join(via, ",") != "shen.str" {
			t.Errorf("viaClosure = %v, want [shen.str]", via)
		}
	})
}

// ---- the host-gated end-to-end check ----

// TestScipCheckFixtures runs the real subcommand over two fixtures. It skips
// -- never fails -- when anything it does not own is missing: the Shen host,
// the Go toolchain, or a sibling shen-go whose stage-2 builder cannot build
// the module here.
func TestScipCheckFixtures(t *testing.T) {
	if os.Getenv("YGGDRASIL_HOST") == "" && defaultHost() == nil {
		t.Skip("no Shen host: set $YGGDRASIL_HOST")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	bin := buildCLI(t)
	for _, fixture := range []string{"fib", "hello"} {
		t.Run(fixture, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), fixture)
			cmd := exec.Command(bin, "scip-check", filepath.Join("tests", fixture+".shen"), dir, "--target", "go")
			out, err := cmd.CombinedOutput()
			s := string(out)
			if strings.Contains(s, "stage 2:") || strings.Contains(s, "SKIP") {
				t.Skipf("stage 2 could not build here:\n%s", lastLines(s, 6))
			}
			if err != nil {
				t.Fatalf("scip-check: %v\n%s", err, lastLines(s, 20))
			}
			if !strings.Contains(s, "yggdrasil-scip-check: OK ") {
				t.Fatalf("no OK line:\n%s", lastLines(s, 20))
			}
			// The verdict must say what it subtracted, by name. The
			// assertion is the invariant -- an empty residue and a clean
			// audit -- not this fixture's particular residue, because
			// pinning the residue is what made correcting the declaration
			// look like a regression last time.
			if !strings.Contains(s, "kl-level delta accounted: special_forms=") {
				t.Fatalf("no accounted line naming the special forms:\n%s", lastLines(s, 20))
			}
			// The declaration is audited too: a name that is not a kernel
			// defun at all must be reported rather than silently subtracted.
			if !strings.Contains(s, "unknown=none") {
				t.Fatalf("no special_forms audit line, or a stale declaration:\n%s", lastLines(s, 20))
			}
			for _, bad := range []string{"kl-delta-missing", "kl-delta-extra", "kl-missing-in-full", "special-forms-unknown"} {
				if strings.Contains(s, bad) {
					t.Fatalf("residue reported (%s):\n%s", bad, lastLines(s, 20))
				}
			}
			// The Go-level comparison is gone; it must not come back
			// without the delta assertion coming with it.
			if strings.Contains(s, "go-level main-reachable") {
				t.Fatalf("the Go-level line is back:\n%s", lastLines(s, 20))
			}
		})
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
