package main

// Tests for `yggdrasil lower` / `lower-check` (lower.go).
//
// Three of the four things worth checking here need no host: the install-phase
// gate is a decision about builders.json, the KL surgery is a decision about
// bytes, and `badLowering` is a decision about two sets. Only the measurement
// and the end-to-end SKIP need a shake, and those skip when there is no host,
// by the same convention as check_test.go.
//
// One assertion deserves its reason spelled out. The count `--report-only`
// prints is compared against builders.json and the shaken kernel.kl, never
// against a literal. A literal here would be a second, unmaintained copy of a
// number that already lives in two files, and the first time the kernel or the
// override list moved it would either be wrong or be "fixed" by copying the
// tool's own output back into the test -- which checks nothing.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// ---- the gate ----

// The declaration on `go` is `installed_after: "before-initialise"` as of the
// shen-go pinned in .github/shen-go.ref (30ab469): the generated main calls
// InstallKernelFast inside the kernel chunk loop, before `shen.initialise`.
// So the gate OPENS, and this is the test that fails -- correctly -- if
// someone edits the phase key, drops it, or drops the _checked_by that makes
// the override list trustworthy.
//
// This was the other way round up to shen-go da55c5d, when the declaration
// read "shen.initialise" and the pass had to refuse. The refusal branch is
// still covered, by TestLowerGateRefusesAnUncheckedOverrideList,
// TestLowerGateRefusesUndeclaredPhase and TestRefusalLastLineIsReasonSpecific,
// on hand-built blocks rather than on whatever `go` happens to declare.
func TestLowerGateOpensForGo(t *testing.T) {
	g, err := lowerGateFor("go")
	if err != nil {
		t.Fatalf("lowerGateFor(go): %v", err)
	}
	if !g.ok {
		t.Fatalf("the gate refused lowering for go (%s), whose "+
			"native_overrides_installed_after is %q:\n%s", g.reason, g.phase, g.refusal())
	}
	if g.phase != "before-initialise" {
		t.Errorf("native_overrides_installed_after = %q, want before-initialise", g.phase)
	}
	if !lowerPhaseOK[strings.ToLower(g.phase)] {
		t.Errorf("the declared phase %q is not one of the spellings lowerPhaseOK accepts", g.phase)
	}
	if len(g.names) == 0 {
		t.Error("go declares no native_overrides; there would be nothing to lower")
	}
	// An open gate deletes code on the strength of the declaration, so the
	// declaration has to carry its provenance and its check.
	if !factChecked(g.checkedBy) {
		t.Errorf("native_overrides_checked_by = %q: an open gate on an unchecked list "+
			"deletes defuns on someone's recollection", g.checkedBy)
	}
	for _, want := range []string{"30ab469", "InstallKernelFast"} {
		if !strings.Contains(g.phaseSource, want) {
			t.Errorf("native_overrides_installed_after_source does not mention %q:\n%s",
				want, g.phaseSource)
		}
	}
}

// A target that declares no native_overrides -- every target but go -- has
// nothing to lower, and that is a different refusal with a different word for
// it. "No declaration" is not "no overrides".
func TestLowerGateRefusesTargetsWithNoOverrides(t *testing.T) {
	g, err := lowerGateFor("lisp")
	if err != nil {
		t.Fatalf("lowerGateFor(lisp): %v", err)
	}
	if g.ok || g.reason != "no-native-overrides" {
		t.Fatalf("lisp: ok=%v reason=%q, want a no-native-overrides refusal", g.ok, g.reason)
	}
	if !strings.Contains(g.refusal(), "nobody measured it") {
		t.Errorf("the refusal reads as a claim about the port rather than about the declaration:\n%s", g.refusal())
	}
}

// A list with no phase is the reading port-contract.md calls out as
// false-by-omission. It must refuse rather than assume the convenient half.
func TestLowerGateRefusesUndeclaredPhase(t *testing.T) {
	g := lowerGateFromBlock("fake", map[string]json.RawMessage{
		"native_overrides": json.RawMessage(`["reverse"]`),
	})
	if g.ok || g.reason != "native-install-phase-undeclared" {
		t.Fatalf("ok=%v reason=%q, want native-install-phase-undeclared", g.ok, g.reason)
	}
}

// And the configuration this code exists for: a port that installs its natives
// before shen.initialise. No shipped target declares it, so the test declares
// one.
func fakeBeforeInitialiseGate(names ...string) lowerGate {
	raw, _ := json.Marshal(names)
	return lowerGateFromBlock("fake", map[string]json.RawMessage{
		"native_overrides":                        json.RawMessage(raw),
		"native_overrides_installed_after":        json.RawMessage(`"before-initialise"`),
		"native_overrides_source":                 json.RawMessage(`"fake port, kernelfast.go InstallKernelFast"`),
		"native_overrides_checked_by":             json.RawMessage(`"TestLowerGatePermitsBeforeInitialise (lower_test.go)"`),
		"native_overrides_installed_after_source": json.RawMessage(`"fake port, main.go: InstallKernelFast is emitted before shen.initialise"`),
	})
}

func TestLowerGatePermitsBeforeInitialise(t *testing.T) {
	g := fakeBeforeInitialiseGate("reverse", "map")
	if !g.ok {
		t.Fatalf("a port whose natives are installed before shen.initialise was refused: %s", g.refusal())
	}
	if g.reason != "" {
		t.Errorf("a permitted gate carries a refusal reason %q", g.reason)
	}
}

// ---- the rule ----

// badLowering(F) :- lowered(F), !equiv(F). The whole correctness check of the
// pass, tested on a drop list the pass would not have produced, because a rule
// that is only ever fed conforming input is a rule nothing has evaluated.
func TestBadLoweringIsTheRule(t *testing.T) {
	equiv := map[string]bool{"reverse": true, "map": true}
	if bad := badLowering([]string{"reverse", "map"}, equiv); len(bad) != 0 {
		t.Errorf("badLowering on a conforming drop list = %v, want empty", bad)
	}
	bad := badLowering([]string{"map", "shen.fake", "hd"}, equiv)
	if len(bad) != 2 || bad[0] != "hd" || bad[1] != "shen.fake" {
		t.Errorf("badLowering = %v, want [hd shen.fake]", bad)
	}
}

// ---- the KL surgery ----

// A kernel.kl in miniature, with the one shape a naive line-based pass gets
// wrong: a string literal carrying a newline and an unbalanced paren, which is
// exactly what the real kernel's vector accessors contain.
const lowerFixtureKernel = `(defun @s (V1 V2) (cn V1 V2))

(defun reverse (V1) (shen.reverse_help V1 ()))

(defun vector-> (V1 V2 V3) (if (= V2 0) (simple-error "cannot access 0th element (
") (address-> V1 V2 V3)))

(defun map (V1 V2) (shen.map-h V1 V2 ()))

(set shen.*call* 0)

(defun hd (V1) (shen.hd V1))
`

func writeLowerFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kernel.kl"), []byte(lowerFixtureKernel), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second slice file and a manifest, so "nothing else changes" has
	// something to be true of.
	if err := os.WriteFile(filepath.Join(dir, "fib.kl"), []byte("(defun fib (N) N)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "yggdrasil.manifest.txt"), []byte("version=4\nshaken=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The headline property: the lowered kernel.kl is the original minus exactly
// the named defuns and nothing else. Checked as a byte-level property of the
// remainder rather than against a hand-written expected file -- every line the
// lowering kept must appear, unaltered and in order, and every line it removed
// must belong to a defun that was named.
func TestLowerSliceDropsExactlyTheNamedDefuns(t *testing.T) {
	dir := writeLowerFixture(t)
	g := fakeBeforeInitialiseGate("reverse", "map", "shen.not-in-this-slice")

	res, err := lowerSlice(dir, g)
	if err != nil {
		t.Fatalf("lowerSlice: %v", err)
	}
	if got := strings.Join(res.dropped, ","); got != "reverse,map" {
		t.Fatalf("dropped = %q, want reverse,map (file order, and only names present in the slice)", got)
	}

	lowered, err := os.ReadFile(filepath.Join(res.dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	assertDeletionOnly(t, lowerFixtureKernel, string(lowered), map[string]bool{"reverse": true, "map": true})

	// The surviving defuns, as a set, are the original set minus the drops.
	before, err := kernelDefunNames(filepath.Join(dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := kernelDefunNames(filepath.Join(res.dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	for name := range before {
		want := name != "reverse" && name != "map"
		if after[name] != want {
			t.Errorf("after lowering, defun %s present=%v, want %v", name, after[name], want)
		}
	}
	// The non-kernel files are byte copies.
	for _, f := range []string{"fib.kl", "yggdrasil.manifest.txt"} {
		a, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(res.dir, f))
		if err != nil {
			t.Fatalf("%s is missing from the lowered slice: %v", f, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs between the canonical and lowered slices; lowering must change kernel.kl and nothing else", f)
		}
	}
	// The canonical slice is the artifact of record and must be untouched.
	canonical, err := os.ReadFile(filepath.Join(dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != lowerFixtureKernel {
		t.Error("lowering modified OUTDIR/kernel.kl; the canonical slice is the artifact of record")
	}
	// And the report names every drop with its citation.
	rep, err := os.ReadFile(filepath.Join(res.dir, loweringReportName))
	if err != nil {
		t.Fatalf("no %s: %v", loweringReportName, err)
	}
	for _, name := range res.dropped {
		line := "lowered " + name + "\t"
		if !strings.Contains(string(rep), line) {
			t.Errorf("%s has no line for %s:\n%s", loweringReportName, name, rep)
		}
	}
	if !strings.Contains(string(rep), "kernelfast.go InstallKernelFast") {
		t.Errorf("%s does not carry the builders.json source citation:\n%s", loweringReportName, rep)
	}
}

// assertDeletionOnly is the byte-compare of the remainder: `after` must be
// `before` with whole lines removed, in order, and every removed run of
// non-blank lines must start a defun that was named for deletion. That is
// stronger than diffing against a golden file -- it fails on any rewrite of a
// surviving byte, and it does not need updating when the fixture grows.
func assertDeletionOnly(t *testing.T, before, after string, dropped map[string]bool) {
	t.Helper()
	bl := strings.Split(before, "\n")
	al := strings.Split(after, "\n")
	i := 0
	for j := 0; j < len(bl); j++ {
		if i < len(al) && al[i] == bl[j] {
			i++
			continue
		}
		// bl[j] was deleted. A deleted blank line is the separator of a
		// deleted defun; a deleted non-blank line must either open a
		// named defun or continue one.
		if strings.TrimSpace(bl[j]) == "" {
			continue
		}
		if name := klDefunName(bl[j]); name != "" {
			if !dropped[name] {
				t.Fatalf("line %d was deleted but %q was not named for lowering: %q", j+1, name, bl[j])
			}
			continue
		}
		// A continuation line of a deleted multi-line defun: the previous
		// original line must also have been deleted, which it was, since we
		// only get here after failing to match.
		if strings.HasPrefix(bl[j], "(") {
			t.Fatalf("line %d was deleted but opens a top-level form that is not a lowered defun: %q", j+1, bl[j])
		}
	}
	if i != len(al) {
		t.Fatalf("the lowered kernel has %d line(s) that are not in the original, starting at %q; "+
			"lowering must delete, never rewrite", len(al)-i, al[i])
	}
}

// A multi-line defun -- one whose string literal carries a newline -- must go
// in one piece, taking its continuation lines with it and leaving the form
// that follows intact.
func TestDropDefunsHandlesMultiLineForms(t *testing.T) {
	out, dropped, err := dropDefuns(lowerFixtureKernel, map[string]bool{"vector->": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "vector->" {
		t.Fatalf("dropped = %v, want [vector->]", dropped)
	}
	if strings.Contains(out, "cannot access 0th element") {
		t.Error("the multi-line defun's continuation line survived; the splitter is not string-aware")
	}
	if !strings.Contains(out, "(defun map (V1 V2)") || !strings.Contains(out, "(set shen.*call* 0)") {
		t.Errorf("lowering took more than the named defun:\n%s", out)
	}
	assertDeletionOnly(t, lowerFixtureKernel, out, map[string]bool{"vector->": true})
}

// A refused gate must produce no lowered/ directory at all: an empty or
// partial one is a directory the next command would read.
//
// The refused gate is built here rather than read off `go`. It used to be
// go's own: up to shen-go da55c5d that target declared its natives installed
// after shen.initialise, so lowerGateFor("go") WAS a refusal and this test
// got one for free. At 30ab469 the gate opens on go (see
// TestLowerGateOpensForGo), and a test about the refused path must not depend
// on which way a shipped target happens to be declared today.
func TestLowerSliceRefusesAndWritesNothing(t *testing.T) {
	dir := writeLowerFixture(t)
	g := lowerGateFromBlock("after-init-port", map[string]json.RawMessage{
		"native_overrides":                        json.RawMessage(`["vector->"]`),
		"native_overrides_installed_after":        json.RawMessage(`"shen.initialise"`),
		"native_overrides_source":                 json.RawMessage(`"a port whose natives land after the initialiser"`),
		"native_overrides_checked_by":             json.RawMessage(`"TestLowerSliceRefusesAndWritesNothing"`),
		"native_overrides_installed_after_source": json.RawMessage(`"fake port, main.go: shen.initialise is emitted before InstallKernelFast"`),
	})
	if g.ok || g.reason != "natives-installed-after-initialise" {
		t.Fatalf("the fixture gate is not the refusal this test needs: ok=%v reason=%q", g.ok, g.reason)
	}
	if _, err := lowerSlice(dir, g); err == nil {
		t.Fatal("lowerSlice accepted a natives-installed-after-initialise declaration")
	}
	if _, err := os.Stat(filepath.Join(dir, loweredDirName)); !os.IsNotExist(err) {
		t.Errorf("a refused lowering left %s behind", filepath.Join(dir, loweredDirName))
	}
}

// ---- the CLI, end to end ----

func lowerCLI(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH; cannot build the CLI")
	}
	return buildCLI(t)
}

// buildCLI compiles this package into a fresh temp dir and returns the
// binary's path. Windows will not exec a file without its .exe suffix, so
// the name carries one there; every test that shells out to the CLI goes
// through here rather than naming the binary itself.
func buildCLI(t *testing.T) string {
	t.Helper()
	name := "yggdrasil"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the CLI: %v\n%s", err, out)
	}
	return bin
}

// needGoLowerStage1 skips, by name, when the REAL gate path on go cannot be
// run here: it needs a stage-1 Shen host to shake with, the go toolchain, and
// the sibling shen-go checkout the `go` builder compiles against. Each is a
// separate named skip so a green run that skipped says which piece was absent.
func needGoLowerStage1(t *testing.T) {
	t.Helper()
	if os.Getenv("YGGDRASIL_HOST") == "" && defaultHost() == nil {
		t.Skip("no-stage1-host: no Shen host available (build ../shen-cl or set $YGGDRASIL_HOST)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no-go-toolchain: go is not on PATH; the go stage-2 builder cannot run")
	}
	if os.Getenv("YGGDRASIL_SHEN_GO_DIR") == "" {
		if _, err := os.Stat(filepath.Join("..", "shen-go")); err != nil {
			t.Skip("no-sibling-shen-go: no shen-go checkout ($YGGDRASIL_SHEN_GO_DIR unset and " +
				"../shen-go absent); the go builder cannot run")
		}
	}
}

// `yggdrasil lower --target go` through the REAL gate -- no fake, no swapped
// seam -- on the target the gate now admits. Up to shen-go da55c5d the same
// command refused with natives-installed-after-initialise and this test
// asserted the refusal; at 30ab469 it shakes, drops, and writes lowered/.
//
// The dropped count is derived, never written down: it is the size of the
// intersection of go's declared native_overrides with the kernel defuns the
// shake kept, which is exactly what --report-only computes. A literal here
// would be a third copy of a number that already lives in two files.
func TestLowerCLILowersGoForReal(t *testing.T) {
	needGoLowerStage1(t)
	bin := lowerCLI(t)
	dir := filepath.Join(t.TempDir(), "out")
	cmd := exec.Command(bin, "lower", "tests/fib.shen", dir, "--target", "go")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lower --target go failed: %v\n%s", err, lastLines(string(out), 20))
	}
	s := string(out)
	if !strings.Contains(s, "yggdrasil-lower: OK target=go") {
		t.Fatalf("no OK sentinel:\n%s", lastLines(s, 20))
	}

	// What the gate deleted, recomputed from the two files it rests on.
	g, err := lowerGateFor("go")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := reportLowering(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	// reportLowering counts against the CANONICAL kernel, which lower left
	// alone, so len(rep.overridden) is the number that should have gone.
	if len(rep.overridden) == 0 {
		t.Fatal("the shaken fib slice kept no defun go declares a native for; " +
			"this test would then be asserting nothing")
	}
	want := "dropped=" + strconv.Itoa(len(rep.overridden))
	if !strings.Contains(s, want) {
		t.Errorf("the sentinel does not say %q (expected from builders.json x kernel.kl):\n%s",
			want, lastLines(s, 4))
	}
	t.Logf("real gate on go: %s", strings.SplitN(s[strings.Index(s, "yggdrasil-lower: OK"):], "\n", 2)[0])

	// lowered/ exists, carries its report, and the canonical slice is intact.
	lowDir := filepath.Join(dir, loweredDirName)
	report, err := os.ReadFile(filepath.Join(lowDir, loweringReportName))
	if err != nil {
		t.Fatalf("no lowering report: %v", err)
	}
	if !strings.Contains(string(report), "installed-after=before-initialise") {
		t.Errorf("the report does not record the phase it acted on:\n%s", report)
	}
	canonical, err := kernelDefunNames(filepath.Join(dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	lowered, err := kernelDefunNames(filepath.Join(lowDir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	for name := range canonical {
		_, kept := lowered[name]
		declared := false
		for _, n := range g.names {
			if n == name {
				declared = true
				break
			}
		}
		if declared == kept {
			t.Errorf("kernel defun %q: declared-native=%v, kept-in-lowered=%v; "+
				"lowering must drop exactly the declared names", name, declared, kept)
		}
	}
}

// `yggdrasil lower-check --target go`, for real, on the gate the port now
// opens -- and it FAILS. This test records that failure rather than hiding it,
// and it is two-sided: if the run starts passing it fails and says what to
// rewrite.
//
// Why it fails. The install PHASE is what builders.json declares and what the
// gate reads, and that fact is now true: shen-go 30ab469 calls
// InstallKernelFast inside the kernel chunk loop, before shen.initialise. But
// the phase is not the whole precondition for lowering. shen-go's overrides
// are CONDITIONAL on the kernel having defined the name: overridePrimitive and
// overrideNative (kl/kernelfast.go:211 and :218) both open with
//
//	if kernelBound(name) == nil { return }
//
// so deleting F's defun from kernel.kl does not leave a native bound to F --
// it stops the native from being installed at all. On tests/fib.shen the
// lowered artifact builds and then dies in its initialiser:
//
//	yggdrasil: shen.initialise failed: variable vector not bound
//
// because `vector` was dropped, its native was skipped, and shen.initialise's
// `(set *property-vector* (vector 20000))` found nothing bound.
//
// So the lowered slice is wrong on go, the check says so, and NOTHING here
// weakens it. docs/lowering.md carries the measurement. What would have to
// change is shen-go's: an unconditional install, or a fact Yggdrasil can read
// that says the install is conditional -- at which point this test's
// expectation flips back.
func TestLowerCheckOnGoFailsOnShenGoConditionalInstall(t *testing.T) {
	needGoLowerStage1(t)
	bin := lowerCLI(t)
	dir := filepath.Join(t.TempDir(), "out")
	cmd := exec.Command(bin, "lower-check", "tests/fib.shen", dir, "--target", "go", "--reference", "go")
	out, err := cmd.CombinedOutput()
	s := string(out)
	if strings.Contains(s, "SKIP reason=toolchain-missing") {
		t.Skipf("no-go-toolchain: the go toolchain is not usable here:\n%s", lastLines(s, 6))
	}
	if err == nil {
		t.Fatalf("lower-check --target go now PASSES. shen-go's InstallKernelFast has stopped "+
			"skipping names the kernel did not define (the kernelBound guard at "+
			"kl/kernelfast.go:211/:218), or the pass has changed. Rewrite the measurement in "+
			"docs/lowering.md and this test:\n%s", lastLines(s, 20))
	}
	if !strings.Contains(s, "yggdrasil-lower-check: FAIL") {
		t.Fatalf("lower-check failed without printing a FAIL sentinel:\n%s", lastLines(s, 20))
	}
	// The exact shape matters: a build or run failure of the LOWERED leg,
	// not a disagreement of outputs and not a refused gate.
	if !strings.Contains(s, "run=lowered@go") {
		t.Errorf("the FAIL does not name the lowered@go run; the failure has moved "+
			"and docs/lowering.md no longer describes it:\n%s", lastLines(s, 20))
	}
	if !strings.Contains(s, "not bound") {
		t.Errorf("the lowered artifact did not die on an unbound name; the cause has moved:\n%s",
			lastLines(s, 20))
	}
	t.Logf("real lower-check on go (expected FAIL, recorded in docs/lowering.md):\n%s", lastLines(s, 4))
}

// ---- the measurement ----

// `--report-only` must work on go today -- it is the mode that is never
// refused, because the number docs/lowering.md quotes has to be computable on
// the port as it is. The expected count is derived from builders.json and the
// shaken kernel.kl, never written down here.
func TestLowerReportOnlyCountsAgainstBuildersJSON(t *testing.T) {
	if os.Getenv("YGGDRASIL_HOST") == "" && defaultHost() == nil {
		t.Skip("no Shen host available (build ../shen-cl or set $YGGDRASIL_HOST)")
	}
	bin := lowerCLI(t)
	dir := filepath.Join(t.TempDir(), "out")
	cmd := exec.Command(bin, "lower", "tests/fib.shen", dir, "--target", "go", "--report-only")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lower --report-only: %v\n%s", err, lastLines(string(out), 20))
	}
	s := string(out)
	if !strings.Contains(s, "yggdrasil-lower: report-only target=go") {
		t.Fatalf("no report-only sentinel:\n%s", s)
	}

	// The expectation, computed: the kept kernel defuns intersected with
	// go's declared native_overrides.
	names, err := kernelDefunNames(filepath.Join(dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := lowerGateFor("go")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, n := range g.names {
		if names[n] {
			want = append(want, n)
		}
	}
	if len(want) == 0 {
		t.Fatal("fib's slice keeps no natively overridden defun; the measurement has nothing to report " +
			"and this test would pass vacuously")
	}
	gotKept := reportField(t, s, "kept-kernel-defuns=")
	gotOver := reportField(t, s, "native-overridden=")
	gotDeclared := reportField(t, s, "declared=")
	if gotKept != len(names) {
		t.Errorf("kept-kernel-defuns=%d, want %d (the defuns in the shaken kernel.kl)", gotKept, len(names))
	}
	if gotOver != len(want) {
		t.Errorf("native-overridden=%d, want %d (kept kernel defuns in go's native_overrides)", gotOver, len(want))
	}
	if gotDeclared != len(g.names) {
		t.Errorf("declared=%d, want %d (the length of go's native_overrides)", gotDeclared, len(g.names))
	}
	// The names, not just the count: a count that is right for the wrong
	// reason is the failure mode a single integer cannot catch.
	if !strings.Contains(s, "names: "+strings.Join(want, ",")) {
		t.Errorf("the report does not name the overridden defuns as %s:\n%s", strings.Join(want, ","), s)
	}
	// --report-only must not have written a lowered slice.
	if _, err := os.Stat(filepath.Join(dir, loweredDirName)); !os.IsNotExist(err) {
		t.Error("--report-only wrote a lowered slice")
	}
	t.Logf("fib on go: %d of %d kept kernel defuns have a declared native (%s)", gotOver, gotKept, strings.Join(want, ", "))
}

// reportField pulls an integer out of a `key=N` pair in the report.
func reportField(t *testing.T, s, key string) int {
	t.Helper()
	i := strings.Index(s, key)
	if i < 0 {
		t.Fatalf("no %s in the report:\n%s", key, s)
	}
	rest := s[i+len(key):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("%s is not a number in %q", key, rest[:20])
	}
	return n
}

// The canonical slice is the artifact of record: a lower run must leave
// OUTDIR's own kernel.kl byte-identical to what a plain shake writes.
func TestLowerLeavesTheCanonicalSliceAlone(t *testing.T) {
	if os.Getenv("YGGDRASIL_HOST") == "" && defaultHost() == nil {
		t.Skip("no Shen host available (build ../shen-cl or set $YGGDRASIL_HOST)")
	}
	host := defaultHost()
	plain := t.TempDir()
	if _, err := shake("tests/fib.shen", plain, host, "sub", true); err != nil {
		t.Fatalf("plain shake: %v", err)
	}
	lowered := t.TempDir()
	if _, err := shake("tests/fib.shen", lowered, host, "sub", true); err != nil {
		t.Fatalf("shake for lowering: %v", err)
	}
	// Lower under the fake before-initialise declaration, using go's real
	// list, so the surgery actually runs over the real kernel.
	real, err := lowerGateFor("go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lowerSlice(lowered, fakeBeforeInitialiseGate(real.names...)); err != nil {
		t.Fatalf("lowerSlice on a real slice: %v", err)
	}
	a, err := os.ReadFile(filepath.Join(plain, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(lowered, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("lowering changed OUTDIR/kernel.kl; the canonical slice must be byte-identical to a plain shake's")
	}
	// And the lowered copy really is smaller, by exactly the declared names
	// that were in the slice.
	low, err := os.ReadFile(filepath.Join(lowered, loweredDirName, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	assertDeletionOnly(t, string(b), string(low), func() map[string]bool {
		m := map[string]bool{}
		for _, n := range real.names {
			m[n] = true
		}
		return m
	}())
}

// ---- adjacent and terminal drops (the case that panicked) ----

// dropDefuns stepped `lo` back over the blank line before a FINAL form so the
// file would not end in one. When the previous form had also been dropped,
// `copied` was already past that blank line and src[copied:lo] was a reversed
// range: "slice bounds out of range [17:16]". Two adjacent drops at the end of
// a file is not an exotic input -- it is what lowering a slice whose last two
// kernel defuns are both natively overridden does.
func TestDropDefunsAdjacentAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		drop []string
		want string
	}{
		{
			name: "the last two forms, both dropped",
			src:  "(defun a (V) V)\n\n(defun b (V) V)\n",
			drop: []string{"a", "b"},
			want: "",
		},
		{
			name: "the first two of three",
			src:  "(defun a (V) V)\n\n(defun b (V) V)\n\n(defun c (V) V)\n",
			drop: []string{"a", "b"},
			want: "(defun c (V) V)\n",
		},
		{
			name: "two adjacent in the middle",
			src:  "(defun a (V) V)\n\n(defun b (V) V)\n\n(defun c (V) V)\n\n(defun d (V) V)\n",
			drop: []string{"b", "c"},
			want: "(defun a (V) V)\n\n(defun d (V) V)\n",
		},
		{
			name: "every form dropped",
			src:  "(defun a (V) V)\n\n(defun b (V) V)\n\n(defun c (V) V)\n",
			drop: []string{"a", "b", "c"},
			want: "",
		},
		{
			name: "the only form dropped",
			src:  "(defun a (V) V)\n",
			drop: []string{"a"},
			want: "",
		},
		{
			name: "a final form dropped after a kept one",
			src:  "(defun a (V) V)\n\n(defun b (V) V)\n",
			drop: []string{"b"},
			want: "(defun a (V) V)\n",
		},
		{
			name: "a non-defun toplevel between two drops is kept",
			src:  "(defun a (V) V)\n\n(set x 0)\n\n(defun b (V) V)\n",
			drop: []string{"a", "b"},
			want: "(set x 0)\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drop := map[string]bool{}
			for _, n := range tc.drop {
				drop[n] = true
			}
			got, dropped, err := dropDefuns(tc.src, drop)
			if err != nil {
				t.Fatalf("dropDefuns: %v", err)
			}
			if len(dropped) != len(tc.drop) {
				t.Errorf("dropped %v, want %d names", dropped, len(tc.drop))
			}
			if got != tc.want {
				t.Errorf("dropDefuns =\n%q\nwant\n%q", got, tc.want)
			}
			assertDeletionOnly(t, tc.src, got, drop)
		})
	}
}

// ---- the fourth gate condition ----

// A native_overrides list nothing re-derives from the port's source is a list
// that can drift without being told, and lowering deletes code on its say-so.
// go's own list is the cautionary case and is on the record in lowering.md: it
// went four symbols short of what InstallKernelFast rebinds while carrying a
// `_verified: true` nothing read.
func TestLowerGateRefusesAnUncheckedOverrideList(t *testing.T) {
	g := lowerGateFromBlock("fake", map[string]json.RawMessage{
		"native_overrides":                        json.RawMessage(`["reverse"]`),
		"native_overrides_installed_after":        json.RawMessage(`"before-initialise"`),
		"native_overrides_checked_by":             json.RawMessage(`"none"`),
		"native_overrides_installed_after_source": json.RawMessage(`"fake port"`),
	})
	if g.ok || g.reason != "native-overrides-unchecked" {
		t.Fatalf("ok=%v reason=%q, want native-overrides-unchecked", g.ok, g.reason)
	}
	msg := g.refusal()
	if !strings.Contains(msg, "native_overrides_checked_by") {
		t.Errorf("the refusal does not name the key that is empty:\n%s", msg)
	}
	// A missing key reads the same as an explicit "none": both mean nothing
	// checks the list.
	absent := lowerGateFromBlock("fake", map[string]json.RawMessage{
		"native_overrides":                 json.RawMessage(`["reverse"]`),
		"native_overrides_installed_after": json.RawMessage(`"before-initialise"`),
	})
	if absent.ok || absent.reason != "native-overrides-unchecked" {
		t.Fatalf("an absent checked_by: ok=%v reason=%q, want native-overrides-unchecked", absent.ok, absent.reason)
	}
}

// The -ize alias is gone: a gate that accepts a spelling no declaration uses
// accepts a typo as a permission.
func TestLowerGateRejectsUnknownPhaseSpellings(t *testing.T) {
	for _, phase := range []string{"before-initialize", "before initialise", "preinit", "shen.initialize"} {
		g := lowerGateFromBlock("fake", map[string]json.RawMessage{
			"native_overrides":                 json.RawMessage(`["reverse"]`),
			"native_overrides_installed_after": json.RawMessage(`"` + phase + `"`),
			"native_overrides_checked_by":      json.RawMessage(`"SomeTest"`),
		})
		if g.ok {
			t.Errorf("the gate accepted the phase spelling %q, which no declaration uses", phase)
		}
	}
	// And the two that are accepted, in both cases.
	for _, phase := range []string{"none", "before-initialise", "Before-Initialise"} {
		g := lowerGateFromBlock("fake", map[string]json.RawMessage{
			"native_overrides":                 json.RawMessage(`["reverse"]`),
			"native_overrides_installed_after": json.RawMessage(`"` + phase + `"`),
			"native_overrides_checked_by":      json.RawMessage(`"SomeTest"`),
		})
		if !g.ok {
			t.Errorf("the gate refused the declared phase %q: %s", phase, g.reason)
		}
	}
}

// The way out of a refusal differs by reason, so the last line has to. It used
// to tell a reader with an undeclared phase or an unchecked list to go and
// move shen-go's InstallKernelFast, which is not their problem.
func TestRefusalLastLineIsReasonSpecific(t *testing.T) {
	block := func(kv map[string]string) map[string]json.RawMessage {
		out := map[string]json.RawMessage{"native_overrides": json.RawMessage(`["reverse"]`)}
		for k, v := range kv {
			out[k] = json.RawMessage(`"` + v + `"`)
		}
		return out
	}
	phaseBad := lowerGateFromBlock("fake", block(map[string]string{
		"native_overrides_installed_after": "shen.initialise",
		"native_overrides_checked_by":      "SomeTest",
	}))
	unchecked := lowerGateFromBlock("fake", block(map[string]string{
		"native_overrides_installed_after": "before-initialise",
		"native_overrides_checked_by":      "none",
	}))
	undeclared := lowerGateFromBlock("fake", block(nil))

	if !strings.Contains(phaseBad.refusal(), "Moving the install ahead of shen.initialise") {
		t.Errorf("the phase refusal does not say to move the install:\n%s", phaseBad.refusal())
	}
	if strings.Contains(unchecked.refusal(), "Moving the install ahead") {
		t.Errorf("the unchecked-list refusal tells the reader to move the install, which is not the problem:\n%s",
			unchecked.refusal())
	}
	if !strings.Contains(unchecked.refusal(), "native_overrides_checked_by") {
		t.Errorf("the unchecked-list refusal does not say what to write:\n%s", unchecked.refusal())
	}
	if strings.Contains(undeclared.refusal(), "Moving the install ahead") {
		t.Errorf("the undeclared-phase refusal tells the reader to move the install:\n%s", undeclared.refusal())
	}
	if !strings.Contains(undeclared.refusal(), "native_overrides_installed_after") {
		t.Errorf("the undeclared-phase refusal does not say what to declare:\n%s", undeclared.refusal())
	}
}

// ---- the three-way parity, actually run ----

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// cmdLowerCheck prints its verdict with fmt.Println, which is how every
// consumer finds it, so the test has to read the same channel.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// withFakeGate swaps the gate seam for the duration of a test. No shipped
// target declares its natives installed before initialisation, so without this
// the whole path below `if !g.ok` is unreachable.
func withFakeGate(t *testing.T, g lowerGate) {
	t.Helper()
	prev := lowerGateForTarget
	lowerGateForTarget = func(string) (lowerGate, error) { return g, nil }
	t.Cleanup(func() { lowerGateForTarget = prev })
}

// The three-way parity, run for real on go under a faked before-initialise
// declaration, in both directions: it must print OK when the lowered slice
// agrees with the canonical one, and FAIL naming the third pair when it does
// not. Until a port moves its native install this is the only way this code
// runs at all.
//
// The lowering drops nothing (the fake table names a defun no slice contains),
// so the lowered artifact is byte-identical to the canonical one and the OK is
// a real agreement rather than a coincidence. `--reference go` keeps the test
// to one toolchain; the consequence is that the middle pair
// (canonical-reference vs canonical-target) compares a run with itself, which
// is why the assertions below are about the golden pair and the third pair.
// The reference leg proper is the existing parity gate's job (parity_test.go).
func TestLowerCheckThreeWayParityPassesAndFails(t *testing.T) {
	if os.Getenv("YGGDRASIL_HOST") == "" && defaultHost() == nil {
		t.Skip("no Shen host available (build ../shen-cl or set $YGGDRASIL_HOST)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH; the go stage-2 builder cannot run")
	}
	if os.Getenv("YGGDRASIL_SHEN_GO_DIR") == "" {
		t.Skip("no sibling shen-go checkout ($YGGDRASIL_SHEN_GO_DIR); the go builder cannot run")
	}
	// A table naming a defun no slice contains: the gate opens, and the
	// pass drops nothing, so the lowered slice is a byte copy.
	withFakeGate(t, fakeBeforeInitialiseGate("shen.no-such-kernel-defun"))

	outdir := filepath.Join(t.TempDir(), "out")

	// --- the positive half, through the real subcommand ---
	var code int
	stdout := captureStdout(t, func() {
		code = cmdLowerCheck([]string{"tests/fib.shen", outdir, "--target", "go", "--reference", "go"})
	})
	if strings.Contains(stdout, "SKIP reason=toolchain-missing") {
		t.Skipf("the go toolchain is not usable here:\n%s", lastLines(stdout, 6))
	}
	if code != 0 {
		t.Fatalf("lower-check exited %d on an agreeing lowering:\n%s", code, lastLines(stdout, 20))
	}
	if !strings.Contains(stdout, "yggdrasil-lower-check: OK target=go reference=go dropped=0 golden=fib.expected") {
		t.Fatalf("no OK sentinel naming the golden:\n%s", stdout)
	}

	loweredDir := filepath.Join(outdir, loweredDirName)
	// The premise of the OK: the lowered kernel really is the canonical one.
	a, err := os.ReadFile(filepath.Join(outdir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(loweredDir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("the fake table dropped something; the OK above was not a byte-identical comparison")
	}

	// --- the negative half ---
	// Redefine a kernel function the program's output goes through, by
	// appending to the lowered kernel: a later defun rebinds the name, so
	// the artifact still builds and boots and answers differently. That is
	// the shape a wrong equiv row has -- a different answer, not a crash --
	// and it is what the third comparison exists to catch.
	corrupt := string(b) + "\n(defun shen.app (V1 V2 V3) (cn \"WRONG-LOWERING\" V2))\n"
	if err := os.WriteFile(filepath.Join(loweredDir, "kernel.kl"), []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	pairs, labels, goldenWord, cerr := lowerCheckRuns("tests/fib.shen", "go", "go", outdir, loweredDir, "")
	if cerr != nil {
		var skip *lowerCheckSkip
		if errors.As(cerr, &skip) {
			t.Skipf("the go toolchain is not usable here: %v", cerr)
		}
		t.Fatalf("the corrupted lowered slice did not build and run; this test needs it to run and "+
			"answer differently, not to crash: %v", cerr)
	}
	lines, ok := lowerCheckVerdict(pairs, "go", "go", goldenWord, 0, labels)
	if ok {
		t.Fatalf("lower-check reported OK on a lowered slice that redefines shen.app:\n%s",
			strings.Join(lines, "\n"))
	}
	want := "yggdrasil-lower-check: FAIL pair=canonical-target-vs-lowered-target target=go dropped=0"
	if lines[0] != want {
		t.Fatalf("sentinel = %q\nwant      %q\n(all lines:\n%s\n)", lines[0], want, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "WRONG-LOWERING") {
		t.Errorf("the FAIL does not show the two outputs:\n%s", strings.Join(lines, "\n"))
	}
	t.Logf("three-way parity verdict on a corrupted lowering:\n%s", strings.Join(lines, "\n"))
}
