package main

// Host-gated tests for the runtime trace (trace.go, the "runtime call trace"
// section of yggdrasil.shen, docs/analysis-rules.md).
//
// Three things are worth asserting and they need different amounts of
// machinery:
//
//  1. the weaving is OPTIONAL -- an untraced shake is unchanged by the fact
//     that a traced one is possible, and by a traced one having just run;
//  2. the CHECK works -- an uncovered call is caught, a clean trace is not;
//     this needs no stage-2 target at all, only the fact dump and the Shen
//     rule engine, so it runs wherever a stage-1 host does;
//  3. a real RUN of a real artifact is covered -- which needs a stage-2
//     runtime, and skips cleanly when there is none.

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// traceTargets returns the targets a run-based trace check can use here, in
// preference order, or nil when there is no usable runtime.
//
// `kl` (shen-go's bare KLambda VM, now an ordinary builders.json entry) is
// tried whenever a Go toolchain and the sibling shen-go checkout are present.
// `go` is added only when its stage-2 builder actually works, established by
// building an untraced fixture: the builder boots its own full-kernel compiler
// image, which is a much larger thing to go wrong than the shake, and a broken
// one must not read as a tracing regression. See the caveat in
// docs/analysis-rules.md.
func traceTargets(t *testing.T, host []string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		return nil
	}
	builders, err := loadBuilders()
	if err != nil {
		return nil
	}
	dir := siblingDir("go", builders["go"])
	if fi, err := os.Stat(filepath.Join(dir, "cmd", "kl")); err != nil || !fi.IsDir() {
		t.Logf("no sibling shen-go checkout at %s", dir)
		return nil
	}
	targets := []string{"kl"}

	probe := t.TempDir()
	if _, err := shake("tests/hello.shen", probe, host, "sub", true); err != nil {
		return targets
	}
	if argv, err := build("go", probe, false); err == nil && argv != nil {
		targets = append(targets, "go")
	} else {
		t.Logf("the go stage-2 builder is not usable here; checking %v only", targets)
	}
	return targets
}

// TestTraceIsOptional: --trace must not be able to change what an ordinary
// shake writes, including after a traced shake has run in the same process
// (the shaker's flag is a global, so a leak is a real failure mode).
func TestTraceIsOptional(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"
	before, traced, after := t.TempDir(), t.TempDir(), t.TempDir()

	if _, err := shake(prog, before, host, "sub", true); err != nil {
		t.Fatalf("shake before: %v", err)
	}
	if _, err := shake(prog, traced, host, "sub", true, shakeOpts{trace: true}); err != nil {
		t.Fatalf("traced shake: %v", err)
	}
	if _, err := shake(prog, after, host, "sub", true); err != nil {
		t.Fatalf("shake after: %v", err)
	}

	for _, f := range []string{"kernel.kl", "fib.kl", "yggdrasil.manifest", "yggdrasil.manifest.txt"} {
		a, err := os.ReadFile(filepath.Join(before, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		b, err := os.ReadFile(filepath.Join(after, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s changed across a traced shake", f)
		}
	}

	plain, err := os.ReadFile(filepath.Join(before, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "ygg.traced") {
		t.Error("an untraced kernel.kl carries trace advice")
	}
	woven, err := os.ReadFile(filepath.Join(traced, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(ygg.traced ", "(ygg.traced-value ", "(defun ygg.trace-open ", "(do (ygg.trace-open)"} {
		if !strings.Contains(string(woven), want) {
			t.Errorf("a traced kernel.kl has no %q", want)
		}
	}
	// The helpers must not be woven, or ygg.traced would call itself.
	for _, helper := range []string{"ygg.trace-open", "ygg.trace-str", "ygg.trace-line", "ygg.traced", "ygg.traced-value"} {
		if strings.Contains(string(woven), "(defun "+helper+" (") {
			continue
		}
		t.Errorf("kernel.kl has no (defun %s ...)", helper)
	}
	if strings.Contains(string(woven), "(ygg.traced ygg.trace") || strings.Contains(string(woven), "(ygg.traced ygg.traced") {
		t.Error("a trace helper was itself woven: that is unbounded recursion")
	}

	mf, err := os.ReadFile(filepath.Join(traced, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"traced=true", "trace-file=" + traceFileName} {
		if !strings.Contains(string(mf), want) {
			t.Errorf("the traced manifest has no %q", want)
		}
	}
	plainMf, err := os.ReadFile(filepath.Join(before, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plainMf), "traced=") {
		t.Error("an untraced manifest records traced=")
	}
	// Weaving adds primitives (open, write-byte, string->n, pos, tlstr); it
	// must not move needs-eval, which is what decides --web and the whole
	// eval-free story.
	if evalLine(t, string(plainMf)) != evalLine(t, string(mf)) {
		t.Errorf("needs-eval differs between the traced and untraced shakes")
	}
}

func evalLine(t *testing.T, manifest string) string {
	t.Helper()
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(line, "needs-eval=") {
			return line
		}
	}
	t.Fatalf("manifest has no needs-eval= line")
	return ""
}

// TestTraceCheckRules exercises the containment rules themselves with no
// stage-2 runtime: a hand-built trace is fed to the same Shen engine
// `yggdrasil trace-check` uses. The point is that the OK case and the FAIL
// case are both reachable -- a check that can only pass is not a check.
func TestTraceCheckRules(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"
	shakeDir, factsDir := t.TempDir(), t.TempDir()
	if _, err := shake(prog, shakeDir, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	if _, err := facts(prog, factsDir, host, "sub", true); err != nil {
		t.Fatalf("facts: %v", err)
	}
	reach := kernelDefunSet(t, filepath.Join(shakeDir, "kernel.kl"))

	// A trace that entered only footprint functions, plus the user defun and
	// a weaver helper -- neither of which is a kernel call-graph row, so
	// neither may be reported.
	var inFootprint []string
	for f := range reach {
		inFootprint = append(inFootprint, f)
	}
	// shen.initialise is in every real trace -- the weaver puts the entry
	// advice inside it -- and the host half now requires it, as the guard
	// that a called.facts is a whole run and not a truncated prefix.
	// kernelDefuns deliberately leaves it out, so put it back by hand.
	clean := append([]string{"fib", "ygg.traced", "shen.initialise"}, inFootprint...)
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), clean)
	writeFactsTSV(filepath.Join(factsDir, "readglobal.facts"), []string{"*stoutput*"})
	got, err := traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, " OK ") {
		t.Errorf("a trace inside the footprint was not OK: %s", got)
	}

	// Now a kernel defun the shake left out. Any one will do; take the first
	// call-graph row that is not in kernel.kl.
	outside := firstUnreached(t, factsDir, reach)
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), append(clean, outside))
	got, err = traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, "FAIL") || !strings.Contains(got, outside) {
		t.Errorf("a call to %s (not in the footprint) was not reported: %s", outside, got)
	}

	// And a global nothing writes.
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), clean)
	writeFactsTSV(filepath.Join(factsDir, "readglobal.facts"), []string{"ygg.*no-such-global*"})
	got, err = traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, "FAIL") || !strings.Contains(got, "ygg.*no-such-global*") {
		t.Errorf("a read of an unwritten global was not reported: %s", got)
	}
}

func firstUnreached(t *testing.T, factsDir string, reach map[string]bool) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(factsDir, "kernel.facts"))
	if err != nil {
		t.Fatalf("reading kernel.facts: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		name := strings.TrimSpace(line)
		if name != "" && !reach[name] {
			return name
		}
	}
	t.Fatalf("every kernel defun is in the footprint; nothing to test with")
	return ""
}

// TestTraceCheckFixtures is the end-to-end case: shake traced, build, run,
// read the trace, and check containment -- on every usable runtime.
func TestTraceCheckFixtures(t *testing.T) {
	host := checkHost(t)
	targets := traceTargets(t, host)
	if len(targets) == 0 {
		t.Skip("no stage-2 runtime available (needs the go toolchain and a sibling shen-go checkout)")
	}
	cases := []struct{ prog, userFn string }{
		{"tests/fib.shen", "fib"},
		{"tests/stdin-sum.shen", "slurp"},
		{"tests/prolog.shen", "likes"},
		{"tests/partial.shen", "f"},
	}
	for _, target := range targets {
		for _, c := range cases {
			target, c := target, c
			name := target + "/" + strings.TrimSuffix(filepath.Base(c.prog), ".shen")
			t.Run(name, func(t *testing.T) {
				res, err := traceCheck(c.prog, t.TempDir(), target,
					defaultStdin(c.prog), host, "sub", shakeOpts{})
				if name := skipName(err); name != "" {
					// A named skip, never a silent pass: the kl
					// runner cannot deliver a fixture stdin, so on
					// kl/stdin-sum there is no run to draw evidence
					// from. See evidencePossible.
					t.Skipf("no evidence obtainable here: %s (%v)", name, err)
				}
				// A target that declares transcript_error_markers must
				// now PASS with none of them in its transcript. Up to
				// shen-go da55c5d this branch ran the other way round:
				// cmd/kl panicked in (shen.initialise) on every boot,
				// recovered, ran the rest, and containment reported OK
				// over a failed boot -- so the test DEMANDED the
				// transcript-error FAIL. At shen-go 30ab469 the boot is
				// clean (`trace-check tests/fib.shen --target kl` reports
				// OK called=33, phase boot=28 program=11), so the
				// expectation is inverted: a marker coming back is a
				// REGRESSION in the port, named here rather than
				// tolerated, and the SHA in .github/shen-go.ref is what
				// would have to have moved to explain it.
				if line := failSentinel(err); strings.Contains(line, "transcript-error") {
					t.Fatalf("%s: %s -- a declared transcript error marker is back. At the "+
						"shen-go pinned in .github/shen-go.ref (30ab469) the %s boot is clean, "+
						"so this is a regression in the port, not an expected gap: %v",
						name, line, target, err)
				}
				if err != nil {
					t.Fatalf("trace-check: %v", err)
				}
				if res == nil {
					t.Skipf("target %s is not runnable here", target)
				}
				if len(transcriptErrorMarkers(target)) > 0 && !res.ok {
					t.Errorf("%s declares transcript_error_markers and did not pass: %s",
						name, res.sentinel)
				}
				if !res.ok {
					t.Fatalf("containment failed: %s", res.sentinel)
				}
				if len(res.called) == 0 {
					t.Fatal("the run recorded no calls")
				}
				// Every port, including the buffering ones, has to get
				// the end-of-run record out. That is what discharges
				// obligation F of docs/port-contract.md.
				if !res.complete {
					t.Error("the trace has no end-of-run record")
				}
				if !contains(res.called, c.userFn) {
					t.Errorf("called does not include the user function %q (got %d names)",
						c.userFn, len(res.called))
				}
				// A run that ended with nothing in the program phase is
				// the instrument failing, and the report has to say so
				// rather than print called-program=0 as a measurement.
				// This is green on kl today (shen-go's VM abandons the
				// initialiser's continuation) and must stay flagged; on
				// go it must never become true unnoticed.
				if len(res.program) == 0 && !res.phaseBroken {
					t.Errorf("no program-phase calls on %s and the report does not flag it: "+
						"called-program=0 would read as a measurement", name)
				}
				if res.phaseBroken {
					t.Logf("%s: phase instrument degenerate (flagged): %s",
						name, degenerateWarning(target))
				}
				t.Logf("%s: %s (readGlobal=%d, %d records, boot=%d program=%d, stdout: %s)",
					name, res.sentinel, len(res.reads), res.records,
					len(res.boot), len(res.program), res.golden.how)
			})
		}
	}
}

// ---- the trace has to be able to say it ENDED --------------------------
//
// Everything downstream of parseTrace reads whatever records it finds as if
// they were the whole run. Before the end-of-run record existed, a trace cut
// to one line and a complete one were the same document: `yggdrasil
// trace-check` reported OK called=1 reach=53 on a called.facts truncated with
// head -1. The three tests below are the three places that has to be caught.

// TestParseTraceEndRecord is the pure-Go half: no host, no artifact. A trace
// without the e record must not read as complete, one with it must, and the
// phase column must partition the called set rather than being dropped.
func TestParseTraceEndRecord(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Truncated: the run's first record and nothing else. This is exactly
	// the repro, at the file the facts are derived from.
	cut, err := parseTrace(write("cut.trace", "f\tshen.initialise\tb\n"))
	if err != nil {
		t.Fatalf("parseTrace: %v", err)
	}
	if cut.complete {
		t.Error("a trace with no e record reports complete: a cut-off run is indistinguishable from a finished one")
	}
	if len(cut.called) != 1 {
		t.Errorf("called = %v, want the one record", cut.called)
	}

	full := "f\tshen.initialise\tb\n" +
		"f\tshen.fillvector\tb\n" +
		"v\t*property-vector*\tb\n" +
		"f\tfib\tp\n" +
		"f\tshen.app\tp\n" +
		"f\tshen.fillvector\tp\n" + // a name in BOTH phases
		"v\t*stoutput*\tp\n" +
		"this is a port writing its own output\n" + // junk: degrade, never fail
		"q\tnot-a-tag\tp\n" +
		"e\tend\n"
	tf, err := parseTrace(write("full.trace", full))
	if err != nil {
		t.Fatalf("parseTrace: %v", err)
	}
	if !tf.complete {
		t.Fatal("a trace with an e record does not report complete")
	}
	if got, want := tf.lines, 7; got != want {
		t.Errorf("records = %d, want %d (junk and the e record are not records)", got, want)
	}
	if !equalStrings(tf.called, []string{"fib", "shen.app", "shen.fillvector", "shen.initialise"}) {
		t.Errorf("called = %v", tf.called)
	}
	if !equalStrings(tf.reads, []string{"*property-vector*", "*stoutput*"}) {
		t.Errorf("reads = %v", tf.reads)
	}
	if !equalStrings(tf.boot, []string{"shen.fillvector", "shen.initialise"}) {
		t.Errorf("boot = %v", tf.boot)
	}
	if !equalStrings(tf.program, []string{"fib", "shen.app", "shen.fillvector"}) {
		t.Errorf("program = %v", tf.program)
	}
	if tf.unknown != 0 {
		t.Errorf("unphased = %d, want 0", tf.unknown)
	}

	// A two-column artifact (an older shaker, or a half-updated tree) must
	// still be read, with the phase unknown -- not silently dropped, which
	// would read as a run that called nothing.
	old, err := parseTrace(write("old.trace", "f\tfib\nv\t*stoutput*\ne\tend\n"))
	if err != nil {
		t.Fatalf("parseTrace: %v", err)
	}
	if !old.complete || len(old.called) != 1 || len(old.reads) != 1 {
		t.Errorf("a two-column trace did not survive: %+v", old)
	}
	if old.unknown != 2 {
		t.Errorf("unphased = %d, want 2", old.unknown)
	}
	if len(old.boot)+len(old.program) != 0 {
		t.Error("a two-column trace invented a phase")
	}

	// The end record is EXACTLY `e<TAB>end`. A port interleaving its own
	// output must not be able to end the run on our behalf, and the comment
	// over the branch claims exactly this, so it is asserted here.
	for _, forged := range []string{
		"f\tfib\tp\ne\tend\tp\n",   // a third column: the port's line, not ours
		"f\tfib\tp\ne\tended\n",    // a longer word
		"f\tfib\tp\ne\tend more\n", // trailing text in the name column
		"f\tfib\tp\nerror\tend\n",  // a tag that merely starts with e
	} {
		tf, err := parseTrace(write("forged.trace", forged))
		if err != nil {
			t.Fatalf("parseTrace: %v", err)
		}
		if tf.complete {
			t.Errorf("a port's own line forged the end of the run: %q", forged)
		}
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestTraceCheckComplete is the end-to-end half on a real artifact: the run
// must produce the end record, both phases must be non-empty, the program
// phase must be a proper subset of the whole (fib's 49,076 records are mostly
// the initialiser's property vector, which is the point of the split), and
// the run's stdout must be the committed golden -- checked in this run, on
// this artifact, not in a separate parity invocation.
func TestTraceCheckComplete(t *testing.T) {
	host := checkHost(t)
	if !contains(traceTargets(t, host), "go") {
		t.Skip("the go stage-2 builder is not usable here")
	}
	res, err := traceCheck("tests/fib.shen", t.TempDir(), "go", "", host, "sub", shakeOpts{})
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if res == nil {
		t.Skip("target go is not runnable here")
	}
	if !res.ok {
		t.Fatalf("containment failed: %s", res.sentinel)
	}
	if !res.complete {
		t.Error("the run produced no end-of-run record")
	}
	if len(res.program) == 0 {
		t.Error("no program-phase calls: the phase never flipped out of boot")
	}
	if len(res.boot) == 0 {
		t.Error("no boot-phase calls: the phase was never boot")
	}
	if len(res.program) >= len(res.called) {
		t.Errorf("program phase (%d) is not strictly smaller than called (%d): the split records nothing",
			len(res.program), len(res.called))
	}
	if res.unknown != 0 {
		t.Errorf("%d records carried no phase column", res.unknown)
	}
	want, err := os.ReadFile("tests/fib.expected")
	if err != nil {
		t.Fatal(err)
	}
	if res.stdout != string(want) {
		t.Errorf("stdout = %q, want %q", res.stdout, string(want))
	}
	if !res.golden.checked {
		t.Errorf("the golden comparison did not run: %s", res.golden.how)
	}
	// calledprogram.facts is a report input: written beside the rule
	// inputs, and deliberately not one of them.
	b, err := os.ReadFile(filepath.Join(res.factsDir, "calledprogram.facts"))
	if err != nil {
		t.Fatalf("calledprogram.facts: %v", err)
	}
	if !strings.Contains(string(b), "fib\n") {
		t.Errorf("calledprogram.facts does not name the user defun:\n%s", b)
	}
	t.Logf("%s (records=%d called=%d program=%d boot=%d neverEntered=%v)",
		res.sentinel, res.records, len(res.called), len(res.program), len(res.boot), res.neverEntered)
}

// TestTraceCheckTruncated is the host half alone, which is the half the Go
// driver's end-of-run record cannot reach: given a called.facts cut to one
// line, `yggdrasil.trace-check` must refuse it rather than report OK on a
// called set of one. Before the guard this printed
// "yggdrasil-trace-check: OK called=1 reach=53".
func TestTraceCheckTruncated(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"
	shakeDir, factsDir := t.TempDir(), t.TempDir()
	if _, err := shake(prog, shakeDir, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	if _, err := facts(prog, factsDir, host, "sub", true); err != nil {
		t.Fatalf("facts: %v", err)
	}
	var footprint []string
	for f := range kernelDefunSet(t, filepath.Join(shakeDir, "kernel.kl")) {
		footprint = append(footprint, f)
	}
	writeFactsTSV(filepath.Join(factsDir, "readglobal.facts"), []string{"*stoutput*"})

	// head -1 called.facts, the whole of the repro.
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), []string{"<-vector"})
	got, err := traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if strings.Contains(got, " OK ") {
		t.Errorf("a one-line called.facts reported OK: %s", got)
	}
	if !strings.Contains(got, "truncated=shen.initialise-not-entered") {
		t.Errorf("no truncation sentinel: %s", got)
	}

	// And the guard must not be a check that only fails: the same relation
	// with the initialiser back in it passes.
	full := append([]string{"fib", "ygg.traced", "shen.initialise"}, footprint...)
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), full)
	got, err = traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, " OK ") {
		t.Errorf("a complete called.facts did not pass: %s", got)
	}
	// With no trace.meta in the directory nobody declared a row count, and
	// the OK line has to say which of the two guards actually ran rather
	// than implying both.
	if !strings.Contains(got, "counts=unverified") {
		t.Errorf("a facts dir with no declared counts claimed to have verified them: %s", got)
	}
}

// TestTraceCheckPrefix is the half of torvalds-3 the sentinel name cannot
// reach. called.facts is written SORTED, so "the tail was lost" is not "the
// end of the file was lost": for fib, shen.initialise is line 21 of 34, and
// a cut to 25 lines keeps it. That cut reported
// "yggdrasil-trace-check: OK called=25 reach=53" -- a strict prefix of the
// run reading as a clean run, which is precisely what the finding said must
// not happen.
//
// The writer therefore declares its row counts in FactsDir/trace.meta, and
// the host half compares. It detects LOSS, not forgery: a hand that rewrites
// trace.meta as well is writing a fiction, and no check confined to one
// directory can say otherwise. That limit is asserted below too, so nobody
// reads the guard as more than it is.
func TestTraceCheckPrefix(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"
	shakeDir, factsDir := t.TempDir(), t.TempDir()
	if _, err := shake(prog, shakeDir, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	if _, err := facts(prog, factsDir, host, "sub", true); err != nil {
		t.Fatalf("facts: %v", err)
	}
	var footprint []string
	for f := range kernelDefunSet(t, filepath.Join(shakeDir, "kernel.kl")) {
		footprint = append(footprint, f)
	}
	sort.Strings(footprint)
	writeFactsTSV(filepath.Join(factsDir, "readglobal.facts"), []string{"*stoutput*"})

	// A whole run: the relation, and a writer that declares what it wrote.
	called := append([]string{"fib", "shen.initialise", "ygg.traced"}, footprint...)
	sort.Strings(called)
	meta := &traceFacts{complete: true, called: called,
		reads: []string{"*stoutput*"}, program: []string{"fib"}, lines: 49076}
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), called)
	if err := writeTraceMeta(factsDir, meta, meta.called); err != nil {
		t.Fatal(err)
	}
	got, err := traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, " OK ") || !strings.Contains(got, "counts=verified") {
		t.Fatalf("a whole run with declared counts did not pass verified: %s", got)
	}

	// Now the cut that keeps shen.initialise. It must not read as a run.
	cut := called[:len(called)-5]
	if !contains(cut, "shen.initialise") {
		t.Fatalf("the prefix does not keep shen.initialise, so it would be caught by the other guard")
	}
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), cut)
	got, err = traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if strings.Contains(got, " OK ") {
		t.Errorf("a called.facts that is a strict prefix of the run reported OK: %s", got)
	}
	if !strings.Contains(got, "truncated=called-count") {
		t.Errorf("no row-count sentinel: %s", got)
	}
	if !strings.Contains(got, strconv.Itoa(len(cut))) || !strings.Contains(got, strconv.Itoa(len(called))) {
		t.Errorf("the sentinel does not say what was read and what was declared: %s", got)
	}

	// A writer that says the trace it read never ended must not be believed
	// about anything else either.
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), called)
	if err := writeTraceMeta(factsDir, &traceFacts{called: called, lines: 1}, called); err != nil {
		t.Fatal(err)
	}
	got, err = traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, "truncated=no-end-record") {
		t.Errorf("a facts dir declaring an unfinished trace passed: %s", got)
	}

	// The honest limit: rewrite the declaration to match the cut and the
	// guard is satisfied. It is a loss detector, not a forgery detector,
	// and the comment above yggdrasil.trace-check says exactly that.
	writeFactsTSV(filepath.Join(factsDir, "called.facts"), cut)
	if err := writeTraceMeta(factsDir, &traceFacts{complete: true, called: cut, lines: 1}, cut); err != nil {
		t.Fatal(err)
	}
	got, err = traceCheckHost(prog, factsDir, host, "sub")
	if err != nil {
		t.Fatalf("trace-check: %v", err)
	}
	if !strings.Contains(got, " OK ") {
		t.Errorf("a consistent (if fictional) facts dir was refused, which is more than the guard claims: %s", got)
	}
}

// TestWriteTraceMeta pins the sidecar's shape, which is a contract with the
// Shen half (ygg.trace-meta-get reads these keys by name).
func TestWriteTraceMeta(t *testing.T) {
	dir := t.TempDir()
	tf := &traceFacts{complete: true, called: []string{"a", "b"}, reads: []string{"*x*"},
		program: []string{"b"}, lines: 7}
	if err := writeTraceMeta(dir, tf, tf.called); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, traceMetaName))
	if err != nil {
		t.Fatal(err)
	}
	want := "complete\ttrue\ncalled\t2\nreadglobal\t1\nprogram\t1\nrecords\t7\n"
	if string(b) != want {
		t.Errorf("trace.meta =\n%q\nwant\n%q", b, want)
	}
	if err := writeTraceMeta(dir, &traceFacts{}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, traceMetaName))
	if !strings.HasPrefix(string(b), "complete\tfalse\n") {
		t.Errorf("an unfinished trace is not declared unfinished: %q", b)
	}
}

// ---- the failure paths themselves --------------------------------------
//
// Every check added to make trace-check able to fail is a check whose whole
// purpose is to produce an error, so each one is exercised HERE producing it.
// A failure path that has never been seen failing is the same defect, one
// level up, as the check that could not fail.

// TestRequireComplete: the end-of-run gate, on a parsed trace, with no host
// and no artifact. A trace without the e record must be refused, and the
// refusal must carry the sentinel line -- the only thing the consumers of
// this command look for.
func TestRequireComplete(t *testing.T) {
	cut := &traceFacts{called: []string{"shen.initialise"}, lines: 1}
	err := requireComplete(cut, "/tmp/yggdrasil.trace", "go")
	if err == nil {
		t.Fatal("a trace with no end-of-run record was accepted")
	}
	if got := failSentinel(err); got != "yggdrasil-trace-check: FAIL truncated=no-end-record" {
		t.Errorf("sentinel = %q, want the no-end-record FAIL line", got)
	}
	if !strings.Contains(err.Error(), "buffered tail") {
		t.Errorf("the error does not name the port's buffered tail: %v", err)
	}
	if err := requireComplete(&traceFacts{complete: true, lines: 2}, "x", "go"); err != nil {
		t.Errorf("a complete trace was refused: %v", err)
	}
	// An error that is not a verdict must not be reported as one.
	if got := failSentinel(errors.New("no go toolchain")); got != "" {
		t.Errorf("failSentinel invented a verdict for a plain error: %q", got)
	}
}

// TestCheckGolden covers all five branches of the stdout comparison, the
// three that decline and the two that fail, and pins the comparison to the
// parity gate's: canon() on both sides, so one golden cannot mean two things.
func TestCheckGolden(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "x.shen")
	golden := filepath.Join(dir, "x.expected")
	if err := os.WriteFile(golden, []byte("fib 20 = 6765\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("matches", func(t *testing.T) {
		g, err := checkGolden(prog, "go", "fib 20 = 6765\n")
		if err != nil || !g.checked || g.how != "matches" {
			t.Fatalf("g = %+v, err = %v", g, err)
		}
	})
	// The whole point of using canon(): the parity gate accepts these, so
	// trace-check must too, or the same golden means two different things.
	for _, got := range []string{"fib 20 = 6765", "fib 20 = 6765\n\n", "fib 20 = 6765\r\n"} {
		t.Run("canon/"+strconv.Quote(got), func(t *testing.T) {
			g, err := checkGolden(prog, "go", got)
			if err != nil {
				t.Fatalf("a trailing-newline/CRLF difference failed trace-check "+
					"but passes scripts/parity-gate.sh against the same file: %v", err)
			}
			if !g.checked {
				t.Errorf("the comparison declined: %s", g.how)
			}
		})
	}
	t.Run("mismatch", func(t *testing.T) {
		g, err := checkGolden(prog, "go", "fib 20 = 6764\n")
		if err == nil {
			t.Fatal("a wrong answer passed the golden comparison")
		}
		if got := failSentinel(err); got != "yggdrasil-trace-check: FAIL stdout=mismatch-vs-x.expected" {
			t.Errorf("sentinel = %q, want the stdout mismatch FAIL line", got)
		}
		if !strings.Contains(err.Error(), "6764") {
			t.Errorf("the error does not show what was produced: %v", err)
		}
		if g.checked {
			t.Error("a failed comparison reports itself as checked")
		}
	})
	t.Run("no-golden", func(t *testing.T) {
		g, err := checkGolden(filepath.Join(dir, "absent.shen"), "go", "anything")
		if err != nil {
			t.Fatalf("a fixture with no committed golden failed: %v", err)
		}
		if g.checked || g.path != "" {
			t.Errorf("g = %+v, want a declined comparison", g)
		}
	})
	t.Run("kl-containment", func(t *testing.T) {
		g, err := checkGolden(prog, "kl", "0- 1+ \nfib 20 = 6765\n1- done\n")
		if err != nil {
			t.Fatalf("the golden embedded in a REPL transcript failed: %v", err)
		}
		if !g.checked || g.how != "contained in the kl transcript" {
			t.Errorf("g = %+v", g)
		}
		if _, err := checkGolden(prog, "kl", "0- 1+ \nfib 20 = 6764\n"); err == nil {
			t.Error("a kl transcript without the golden in it passed")
		}
	})
}

// TestEvidencePossible: the one target/fixture pair on which a trace cannot
// be evidence must be refused UP FRONT and BY NAME, not run and reported OK.
//
// shen-go's cmd/kl reads its program from os.Stdin and takes no file argument,
// so the run appends the fixture's stdin to the KL forms; the VM eats them as
// toplevel forms and tests/stdin-sum answers "bytes: 0 digest: 0" against a
// golden of "bytes: 15 digest: 12410". The earlier code declined the stdout
// comparison and carried on, so a trace of that run produced
// `yggdrasil-trace-check: OK` and a green fixture test.
func TestEvidencePossible(t *testing.T) {
	err := evidencePossible("kl", "tests/stdin-sum.stdin")
	if err == nil {
		t.Fatal("kl + a fixture stdin was accepted: the VM eats the stdin bytes as toplevel forms, " +
			"so the run answers the wrong thing and its trace is not evidence")
	}
	if got := skipName(err); got != skipStdinAppended {
		t.Errorf("skipName = %q, want %q", got, skipStdinAppended)
	}
	// A named skip is not a verdict: it must not print on the FAIL line.
	if got := failSentinel(err); got != "" {
		t.Errorf("a skip was reported as a failure verdict: %q", got)
	}
	// Everything else is allowed through.
	for _, c := range []struct{ target, stdin string }{
		{"kl", ""}, {"go", "tests/stdin-sum.stdin"}, {"go", ""},
	} {
		if err := evidencePossible(c.target, c.stdin); err != nil {
			t.Errorf("evidencePossible(%q, %q) = %v, want nil", c.target, c.stdin, err)
		}
	}
	if got := skipName(errors.New("plain")); got != "" {
		t.Errorf("skipName invented a skip for a plain error: %q", got)
	}
}

// hickey-13. The skip above is driven by a DECLARED FACT, not by the string
// "kl". That distinction is the whole of the change: the refusal used to be
// `target == klTarget`, which meant a second runtime with the same property
// would have been run against bytes it cannot receive, and reported OK.
//
// This reads builders.json rather than a hard-coded expectation on either
// side: whichever targets declare stdin=appended-to-program are exactly the
// ones evidencePossible refuses a fixture stdin on, and the rest are exactly
// the ones it lets through.
func TestStdinFactDrivesTheSkip(t *testing.T) {
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	var declaring, plain []string
	for name := range builders {
		if stdin, _ := runFacts(name); stdin == stdinAppendedToProgram {
			declaring = append(declaring, name)
		} else {
			plain = append(plain, name)
		}
	}
	if len(declaring) == 0 {
		t.Fatalf("no target declares stdin=%s; the skip this test is about can never fire, "+
			"which means nothing exercises it", stdinAppendedToProgram)
	}
	for _, name := range declaring {
		if got := skipName(evidencePossible(name, "tests/stdin-sum.stdin")); got != skipStdinAppended {
			t.Errorf("target %s declares stdin=%s and trace-check does not skip it: %q",
				name, stdinAppendedToProgram, got)
		}
		if err := evidencePossible(name, ""); err != nil {
			t.Errorf("target %s with no fixture stdin was refused: %v", name, err)
		}
	}
	for _, name := range plain {
		if err := evidencePossible(name, "tests/stdin-sum.stdin"); err != nil {
			t.Errorf("target %s does not declare stdin=%s but was skipped: %v",
				name, stdinAppendedToProgram, err)
		}
	}
	// And the two facts kl declares are the two softenings this file makes.
	stdin, stdout := runFacts("kl")
	if stdin != stdinAppendedToProgram || stdout != stdoutTranscript {
		t.Errorf("kl's declared run facts are (%q, %q); trace.go's two softenings are keyed on "+
			"(%q, %q)", stdin, stdout, stdinAppendedToProgram, stdoutTranscript)
	}
	// A target that declares neither gets neither softening: byte equality
	// against the golden, and no skip.
	if _, err := checkGolden("tests/fib.shen", "go", "0- 1+ \nfib 20 = 6765\n"); err == nil {
		t.Error("a transcript passed the golden comparison on a target that declares stdout=program")
	}
}

// TestNeverEnteredReports: the user-defun report must survive its inputs
// being odd, and must say UNKNOWN rather than nothing when it cannot be
// produced. Before this it returned nil on an unreadable manifest, which
// reads identically to "every user defun was entered".
func TestNeverEnteredReports(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "yggdrasil.manifest.txt")
	body := "traced=true\nfn=fib 1\nfn=helper 2\nfn=unused 0\nprimitive=close\n"
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := neverEntered(dir, []string{"fib", "shen.initialise"})
	if err != nil {
		t.Fatalf("neverEntered: %v", err)
	}
	if !equalStrings(got, []string{"helper", "unused"}) {
		t.Errorf("neverEntered = %v, want the two unexercised defuns", got)
	}
	if got, err := neverEntered(dir, []string{"fib", "helper", "unused"}); err != nil || len(got) != 0 {
		t.Errorf("neverEntered = %v, %v; want empty and no error", got, err)
	}
	// The branch that used to vanish.
	got, err = neverEntered(t.TempDir(), []string{"fib"})
	if err == nil {
		t.Fatal("an unreadable manifest produced a silent empty report")
	}
	if got != nil {
		t.Errorf("neverEntered = %v, want nil beside the error", got)
	}
	if !strings.Contains(err.Error(), "cannot be reported") {
		t.Errorf("the error does not say the report could not be produced: %v", err)
	}
}

// TestPhaseDegenerate: the phase instrument has to be able to report its own
// failure. A run that ENDED, whose every record carried a phase, and in which
// nothing is tagged program, did not observe a program that called nothing --
// it failed to flip. That is what --target kl does today (shen-go's VM
// abandons the initialiser's continuation), and reporting it as
// called-program=0 without comment is the drift this test exists against.
func TestPhaseDegenerate(t *testing.T) {
	cases := []struct {
		name string
		tf   traceFacts
		want bool
	}{
		{"kl: ended, all boot, nothing program",
			traceFacts{complete: true, boot: []string{"fib", "shen.initialise"}}, true},
		{"a healthy split", traceFacts{complete: true,
			boot: []string{"shen.initialise"}, program: []string{"fib"}}, false},
		{"unfinished run: the gate above catches it, this does not claim to",
			traceFacts{boot: []string{"shen.initialise"}}, false},
		{"an unphased two-column artifact is not degenerate, it is unphased",
			traceFacts{complete: true, unknown: 3}, false},
		{"nothing at all",
			traceFacts{complete: true}, false},
	}
	for _, c := range cases {
		if got := c.tf.phaseDegenerate(); got != c.want {
			t.Errorf("%s: phaseDegenerate = %v, want %v", c.name, got, c.want)
		}
	}
	w := degenerateWarning("kl")
	for _, want := range []string{"DEGENERATE", "kl", "called-program=0", "calledprogram.facts"} {
		if !strings.Contains(w, want) {
			t.Errorf("the degenerate warning does not mention %q:\n%s", want, w)
		}
	}
}

// ---- (A) the pruned artifact, torvalds-12 ------------------------------
//
// Until these, nothing here ever looked at a PRUNED artifact: trace-check
// shook with pruning off, so the trace's readGlobal relation could not see a
// global whose initialiser --prune-init had removed, and the only runtime
// evidence stage 4 had was one fib stdout comparison on one target.

// TestPrunedReadViolations is the rule as a pure function: the three sets in,
// the FAIL set out, no host and no runtime. The negative case below is the
// same rule driven by a real artifact; this one pins the algebra, and in
// particular that port_reads is an escape hatch rather than decoration.
func TestPrunedReadViolations(t *testing.T) {
	pruned := []string{"shen.*tc*", "*home-directory*", "shen.*spy*"}
	ports := []string{"*home-directory*", "*stoutput*"}
	cases := []struct {
		name  string
		reads []string
		want  []string
	}{
		{"a read of a pruned global nobody declares", []string{"shen.*tc*"}, []string{"shen.*tc*"}},
		{"a read of a pruned global the port declares", []string{"*home-directory*"}, nil},
		{"a read of a global that was never pruned", []string{"*stoutput*", "*hush*"}, nil},
		{"both, sorted and deduplicated",
			[]string{"shen.*spy*", "shen.*tc*", "shen.*tc*", "*home-directory*"},
			[]string{"shen.*spy*", "shen.*tc*"}},
		{"nothing read at all", nil, nil},
	}
	for _, c := range cases {
		got := prunedReadViolations(c.reads, pruned, ports)
		if !equalStrings(got, c.want) {
			t.Errorf("%s: prunedReadViolations = %v, want %v", c.name, got, c.want)
		}
	}
	// Nothing pruned is the default shake, and it can never produce a
	// violation however much the run reads.
	if got := prunedReadViolations([]string{"*hush*", "shen.*tc*"}, nil, nil); len(got) != 0 {
		t.Errorf("an unpruned build produced violations: %v", got)
	}
}

// TestPrunedGlobalsIsTheDifference: "the shake pruned this" is read off two
// artifacts, not declared. The manifest records a count; the names come from
// diffing the two initialisers, so a build that pruned nothing yields nothing
// and anything the two share cancels.
func TestPrunedGlobalsIsTheDifference(t *testing.T) {
	unpruned := []string{"*hush*", "shen.*tc*", "*version*", "shen.*spy*"}
	kept := []string{"*hush*", "*version*"}
	if got := prunedGlobals(unpruned, kept); !equalStrings(got, []string{"shen.*spy*", "shen.*tc*"}) {
		t.Errorf("prunedGlobals = %v, want the two dropped globals", got)
	}
	if got := prunedGlobals(unpruned, unpruned); len(got) != 0 {
		t.Errorf("a build that pruned nothing reported %v", got)
	}
}

// TestTraceCheckPrunedFixtures: the positive half, end to end on a real
// pruned artifact. fib prunes 28 of the initialiser's globals and reads none
// of them, which is the claim stage 4 makes and had no runtime evidence for.
func TestTraceCheckPrunedFixtures(t *testing.T) {
	host := checkHost(t)
	targets := traceTargets(t, host)
	if !contains(targets, "go") {
		t.Skip("the go stage-2 builder is not usable here; --prune-init needs a target whose port_reads are verified")
	}
	res, err := traceCheck("tests/fib.shen", t.TempDir(), "go", "", host, "sub",
		shakeOpts{pruneInit: true})
	if err != nil {
		t.Fatalf("trace-check --prune-init: %v", err)
	}
	if res == nil {
		t.Skip("go is not runnable here")
	}
	if !res.ok {
		t.Fatalf("the pruned fib artifact did not pass: %s", res.sentinel)
	}
	if len(res.pruned) == 0 {
		t.Fatal("--prune-init pruned nothing, so the check had nothing to check: " +
			"the two reference builds are identical and the negative case cannot fail either")
	}
	if len(res.prunedRead) != 0 {
		t.Errorf("fib read pruned globals: %v", res.prunedRead)
	}
	if !res.complete {
		t.Error("the trace of the pruned artifact has no end-of-run record")
	}
	if res.golden.how != "matches" {
		t.Errorf("the pruned artifact's stdout was not compared: %s", res.golden.how)
	}
	t.Logf("fib pruned %d globals, read %d of them outside port_reads: %s",
		len(res.pruned), len(res.prunedRead), res.sentinel)
}

// TestTraceCheckPrunedReadFails is the negative half, and the whole of
// torvalds-12 in one test.
//
// tests/computed-read.shen builds the name shen.*tc* out of a string, so there
// is no symbol for rawsym to keep alive and no readsIn or reads row:
// --prune-init deletes (set shen.*tc* false) from the initialiser, and the
// artifact reads the global anyway. The run is CORRECT -- it writes the global
// itself first, and its stdout matches the golden -- so nothing but the trace
// can see this.
//
// The two halves of the test are the finding: without --prune-init the same
// program on the same target reports OK, because the host half's uncoveredRead
// rule takes `initwrite` from the UNPRUNED analysis and still believes the
// initialiser writes shen.*tc*.
func TestTraceCheckPrunedReadFails(t *testing.T) {
	host := checkHost(t)
	targets := traceTargets(t, host)
	if !contains(targets, "go") {
		t.Skip("the go stage-2 builder is not usable here")
	}
	const prog = "tests/computed-read.shen"

	// Unpruned: the check that existed before this commit, on the artifact
	// that is about to fail. It passes, and that is the defect.
	clean, err := traceCheck(prog, t.TempDir(), "go", "", host, "sub", shakeOpts{})
	if err != nil {
		t.Fatalf("trace-check (no pruning): %v", err)
	}
	if clean == nil {
		t.Skip("go is not runnable here")
	}
	if !clean.ok {
		t.Fatalf("the UNPRUNED artifact already fails (%s), so the pruned failure below "+
			"would not be evidence about pruning", clean.sentinel)
	}

	res, err := traceCheck(prog, t.TempDir(), "go", "", host, "sub", shakeOpts{pruneInit: true})
	if err == nil {
		t.Fatalf("the pruned artifact passed: it reads shen.*tc* at run time and the shake "+
			"deleted the (set shen.*tc* false) that initialises it (pruned %d globals)",
			len(res.pruned))
	}
	if name := skipName(err); name != "" {
		t.Skipf("no evidence obtainable here: %s", name)
	}
	line := failSentinel(err)
	if line == "" {
		t.Fatalf("the failure was not reported on the sentinel line, so no consumer that "+
			"greps for the verdict can see it: %v", err)
	}
	if !strings.Contains(line, "pruned-read=") || !strings.Contains(line, "shen.*tc*") {
		t.Errorf("the sentinel does not name the global: %s", line)
	}
	if res == nil || !contains(res.prunedRead, "shen.*tc*") {
		t.Errorf("the result does not carry the violation: %+v", res)
	}
	if res != nil && !contains(res.pruned, "shen.*tc*") {
		t.Errorf("shen.*tc* is not in the pruned set, so the failure came from somewhere else: %v", res.pruned)
	}
	t.Logf("pruned artifact: %s", line)
}

// ---- (B) the containment check that can fail ---------------------------

// TestOutsideReach pins the predicate, including the two exclusions that stop
// it from failing on things reach never claimed: a user defun and the weaver's
// own helpers are not kernel rows.
func TestOutsideReach(t *testing.T) {
	kernel := []string{"shen.app", "shen.printF", "pr"}
	reach := []string{"shen.app", "pr"}
	cases := []struct {
		name   string
		called []string
		want   []string
	}{
		{"a kernel defun outside the slice", []string{"shen.printF"}, []string{"shen.printF"}},
		{"only names the slice has", []string{"pr", "shen.app"}, nil},
		{"a user defun is not a kernel row", []string{"fib", "main"}, nil},
		{"the weaver's helpers are not kernel rows", []string{"ygg.traced", "shen.initialise"}, nil},
		{"deduplicated and sorted",
			[]string{"shen.printF", "shen.printF", "pr"}, []string{"shen.printF"}},
	}
	for _, c := range cases {
		if got := outsideReach(c.called, kernel, reach); !equalStrings(got, c.want) {
			t.Errorf("%s: outsideReach = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestShakeExprFullTraced: the full traced artifact is its own named mode. The
// refusal of --trace with --no-shake is still a refusal -- nothing gets the
// woven full program by setting the two booleans that were never meant to
// compose -- and the mode that does want it says so.
func TestShakeExprFullTraced(t *testing.T) {
	got, err := shakeExpr("/p/prog.shen", "/p/out", shakeOpts{traceFull: true})
	if err != nil {
		t.Fatalf("traceFull was refused: %v", err)
	}
	if !strings.Contains(got, "yggdrasil.shake-full-traced") {
		t.Errorf("traceFull did not choose the woven full entry point: %s", got)
	}
	if _, err := shakeExpr("/p/prog.shen", "/p/out", shakeOpts{full: true, trace: true}); err == nil {
		t.Error("--no-shake with --trace is no longer refused: the new mode removed the guard " +
			"instead of standing beside it")
	}
	for _, o := range []shakeOpts{{traceFull: true, full: true}, {traceFull: true, trace: true}} {
		if _, err := shakeExpr("/p/prog.shen", "/p/out", o); err == nil {
			t.Errorf("%+v was accepted: traceFull must not be reachable by piling flags on", o)
		}
	}
}

// TestTraceCheckFullContains is the positive half of hickey-1's stronger form:
// the FULL fib artifact runs, and everything its PROGRAM phase entered is in
// the slice's reach. The boot phase is not, by a wide margin, and that is
// reported rather than failed -- a full artifact's boot IS the eval-capable
// initialiser the shake threw away.
func TestTraceCheckFullContains(t *testing.T) {
	host := checkHost(t)
	targets := traceTargets(t, host)
	if !contains(targets, "go") {
		t.Skip("--full needs a stage-2 builder that can build the whole kernel; go is not usable here")
	}
	res, err := traceCheck("tests/fib.shen", t.TempDir(), "go", "", host, "sub",
		shakeOpts{traceFull: true})
	if err != nil {
		t.Fatalf("trace-check --full: %v", err)
	}
	if res == nil {
		t.Skip("go is not runnable here")
	}
	if !res.ok {
		t.Fatalf("the full fib run left the slice's reach in the program phase: %s (%v)",
			res.sentinel, res.programOutside)
	}
	if len(res.programOutside) != 0 {
		t.Errorf("program-phase calls outside reach: %v", res.programOutside)
	}
	// The check is only worth anything if the artifact really does hold more
	// than the slice: with kernel == reach every call is inside by
	// construction, which is the defect --full exists to fix.
	if len(res.bootOutside) == 0 {
		t.Error("the full artifact's boot entered nothing outside the slice, so this build is " +
			"not the full program and the containment check could not have failed")
	}
	if len(res.program) == 0 {
		t.Error("no program-phase calls at all: the phase instrument, not the containment, is what ran")
	}
	t.Logf("full fib: reach=%d program=%d outside-reach=%d boot-outside-reach=%d (%s)",
		len(res.reach), len(res.program), len(res.programOutside), len(res.bootOutside), res.sentinel)
}

// TestTraceCheckFullUncovered is the negative half: the containment check
// FAILS, naming the function, on a program that reaches a kernel defun the
// shake dropped.
//
// tests/computed-call.shen resolves shen.printF through (intern "shen.printF")
// and the kernel's lambda table, so no syntactic analysis can see the call.
// The SLICE cannot demonstrate it -- shen.printF is not in the artifact and
// trim-top restricts the lambda-table literal to the footprint, so the call is
// an error there rather than a record, which is exactly hickey-1's point.
// TestTraceCheckSliceCannotSeeIt is that control.
func TestTraceCheckFullUncovered(t *testing.T) {
	host := checkHost(t)
	targets := traceTargets(t, host)
	if !contains(targets, "go") {
		t.Skip("--full needs a stage-2 builder that can build the whole kernel; go is not usable here")
	}
	const prog = "tests/computed-call.shen"

	res, err := traceCheck(prog, t.TempDir(), "go", "", host, "sub", shakeOpts{traceFull: true})
	if name := skipName(err); name != "" {
		t.Skipf("no evidence obtainable here: %s", name)
	}
	if err != nil && res == nil {
		t.Fatalf("trace-check --full: %v", err)
	}
	if res == nil {
		t.Skip("go is not runnable here")
	}
	if res.ok {
		t.Fatalf("the full run of a program that calls shen.printF by a computed name passed: %s",
			res.sentinel)
	}
	if !strings.Contains(res.sentinel, "shen.printF") {
		t.Errorf("the verdict does not name the function: %s", res.sentinel)
	}
	if !contains(res.programOutside, "shen.printF") {
		t.Errorf("the program-phase containment set does not carry it: %v", res.programOutside)
	}
	// The run itself was correct: this is not a crash being reported as a
	// containment failure.
	if res.golden.how != "matches" {
		t.Errorf("the run's stdout was not the golden (%s), so the failure may be the run's, "+
			"not the footprint's", res.golden.how)
	}
	t.Logf("full computed-call: %s (reach=%d, boot-outside=%d)",
		res.sentinel, len(res.reach), len(res.bootOutside))
}

// TestTraceCheckSliceCannotSeeIt is the control for the test above, and the
// reason --full has to exist at all. On the SHAKEN artifact the same program
// does not fail the containment check -- it cannot even produce a clean run,
// because the name it computes is not in the slice. An empty uncoveredCall
// there is a fact about the artifact, not about the rules.
func TestTraceCheckSliceCannotSeeIt(t *testing.T) {
	host := checkHost(t)
	targets := traceTargets(t, host)
	if !contains(targets, "go") {
		t.Skip("the go stage-2 builder is not usable here")
	}
	res, err := traceCheck("tests/computed-call.shen", t.TempDir(), "go", "", host, "sub", shakeOpts{})
	if err == nil && res != nil && res.ok {
		t.Fatal("the shaken slice ran a program that calls a kernel defun outside its footprint " +
			"and reported OK: either the shake now keeps shen.printF, in which case this fixture " +
			"no longer tests anything, or containment is broken")
	}
	if res != nil && !res.ok {
		t.Logf("the slice reported a failure of its own: %s", res.sentinel)
		return
	}
	t.Logf("the slice could not produce a run at all, which is hickey-1 exactly: %v", err)
}

// TestPhaseBoundaryIsTheFirstProgramForm: the phase flip is woven into the
// user files, at the first toplevel form that is not a definition -- not at
// the end of shen.initialise, where it used to be.
//
// The two points coincide on a shaken artifact and do not on a full one:
// installing the user's own defuns is initialisation, and a port with the whole
// kernel behind it does that through the kernel's arity table
// (shen.store-arity and five more), which on fib was the first 424 records of
// the "program" phase, every one of them outside the slice's reach. A
// containment check reading those as the program leaving its footprint fails
// for the wrong reason, which is only one step better than one that cannot fail.
func TestPhaseBoundaryIsTheFirstProgramForm(t *testing.T) {
	host := checkHost(t)
	dir := t.TempDir()
	if _, err := shake("tests/fib.shen", dir, host, "sub", true, shakeOpts{trace: true}); err != nil {
		t.Fatalf("traced shake: %v", err)
	}
	const flip = "(set ygg.*trace-phase* 112)"
	user, err := os.ReadFile(filepath.Join(dir, "fib.kl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(user)
	i := strings.Index(body, flip)
	if i < 0 {
		t.Fatalf("the user KL carries no phase flip:\n%s", body)
	}
	if n := strings.Count(body, flip); n != 1 {
		t.Errorf("the flip is woven %d times, so the boundary is not a boundary", n)
	}
	// Before it: definitions only. After it: the program.
	if j := strings.LastIndex(body, "(defun "); j > i {
		t.Error("a defun is emitted after the phase flip, so its installation would be tagged program")
	}
	if !strings.Contains(body[i:], "(pr ") {
		t.Errorf("the program's own toplevel form is not after the flip:\n%s", body[i:])
	}
	kernel, err := os.ReadFile(filepath.Join(dir, "kernel.kl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(kernel), "\n") {
		if strings.HasPrefix(line, "(defun shen.initialise ") && strings.Contains(line, flip) {
			t.Error("the initialiser still flips the phase: the boundary would be its return, " +
				"and the port's installation of the user defuns would land in the program phase")
		}
	}
}

// hickey-13, the other half: no target's NAME may be special-cased in the
// tool. `kl` was a runner in trace.go with its own descriptor, its own golden
// rule and its own skip, reachable from one subcommand and invisible to the
// rest -- a target that was not a target. It is an entry in builders.json now,
// and what is peculiar about it is declared there (program_file, stdin,
// stdout, transcript_error_markers).
//
// THIS IS A LINT, and its name says so, because what it checks is narrower
// than "no special case": it parses each non-test Go source and fails on a
// string literal that names `kl` -- "kl" itself, "cmd/kl" or any other literal
// containing it as a path or word, and the entry's run_impl string. Parsing
// rather than grepping is what lets the prose say "shen-go's cmd/kl reads its
// program from stdin" in a comment, which is the true sentence this file is
// built on, while a literal with the same text in the code fails.
//
// What it does NOT catch: a name reached some other way -- built up from
// pieces, read from a constant, matched with a prefix. The behavioural tests
// are what cover that (TestStdinFactDrivesTheSkip, and the assertion below
// that the entry carries everything the deleted runner did); this one catches
// the obvious regression cheaply and in one place.
func TestLintNoKlNameLiteralInNonTestGoSources(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	impl := "shen-go cmd/kl"
	if builders, err := loadBuilders(); err == nil {
		impl = builders["kl"].RunImpl
	}
	// A literal is a hit when it IS the target name, or when it carries it
	// as a path segment or a whole word -- "cmd/kl", "./cmd/kl", "kl vm" --
	// or when it is the entry's run_impl. Substrings of longer words
	// ("klvm", "kernel.kl") are not: those name files and binaries, which
	// is not what special-casing a target looks like.
	word := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])kl($|[^A-Za-z0-9_.-])`)
	hit := func(v string) bool {
		return v == "kl" || v == impl || word.MatchString(v)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0) // no comments: prose may say it
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value) // handles `raw` and "quoted"
			if err != nil || !hit(v) {
				return true
			}
			t.Errorf(`%s: the string literal %s names the target kl.

  Every target reaches build/run/parity/trace-check through builders.json.
  What is peculiar about a target belongs on its entry as a declared fact
  (kl's program_file/stdin/stdout/transcript_error_markers), read back
  through programFileFor, runFacts or transcriptErrorMarkers -- not as a
  name compared in Go, which the next such target would silently not match.`,
				fset.Position(lit.Pos()), lit.Value)
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("no non-test Go sources were scanned; this lint would pass vacuously")
	}
	// The lint has to be able to fire: a literal it would not catch means
	// it is checking a spelling nobody uses.
	for _, v := range []string{"kl", "cmd/kl", "./cmd/kl", impl} {
		if !hit(v) {
			t.Errorf("the lint would not catch the literal %q", v)
		}
	}
	for _, v := range []string{"klvm", "kernel.kl", "shen-go", "yggdrasil.manifest.txt"} {
		if hit(v) {
			t.Errorf("the lint fires on %q, which names a file rather than the target", v)
		}
	}

	// And the target really is reachable as data: nothing but builders.json
	// puts it there.
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	b, ok := builders["kl"]
	if !ok {
		t.Fatal("builders.json has no kl entry; trace-check --target kl would now be an unknown target")
	}
	if b.ProgramFile == "" || len(b.Build) < 2 || b.Stdin != stdinAppendedToProgram ||
		b.Stdout != stdoutTranscript || len(b.TranscriptErrorMarkers) == 0 {
		t.Errorf("kl's entry does not declare what the deleted runner did: %+v", b)
	}
}

// The build helper the kl recipe calls is the old klRunner's feed-file logic,
// and it has to stay logic: kernel, then the initialiser, then the user files
// IN MANIFEST ORDER. A recipe cannot express that, which is why the step is a
// named helper rather than a shell line -- and why it is worth a test that
// does not need a toolchain.
func TestProgramFileStepConcatenatesInManifestOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("kernel.kl", "(defun kernel () 0)\n")
	write("b.kl", "(defun b () 2)\n")
	write("a.kl", "(defun a () 1)\n")
	// b before a, deliberately: the manifest's order is the program's, and
	// a directory listing's is not.
	write("yggdrasil.manifest.txt", "fn=a 0\nuser=b.kl\nuser=a.kl\n")

	out := filepath.Join(dir, "feed.kl")
	if err := programFileStep([]string{dir, out}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "(defun kernel () 0)\n\n(shen.initialise)\n(defun b () 2)\n\n(defun a () 1)\n\n"
	if string(got) != want {
		t.Errorf("program-file wrote\n  %q\nwant\n  %q", got, want)
	}
	// Its failures are errors, not empty files: a slice with no user= line
	// would otherwise be fed to the VM as a kernel and nothing else.
	write("yggdrasil.manifest.txt", "fn=a 0\n")
	if err := programFileStep([]string{dir, out}); err == nil {
		t.Error("a manifest with no user= line produced a program file anyway")
	}
	if err := programFileStep([]string{dir}); err == nil {
		t.Error("program-file accepted the wrong number of arguments")
	}
}

// `yggdrasil run tests/fib.shen OUT --target kl` works -- which it could not
// before, because `kl` was not a target and `run` had never heard of it. The
// whole path is exercised: the two build steps off builders.json (the VM, then
// the feed file the named helper writes), programFileFor resolving the entry's
// program_file, and openRunStdin putting that file on the run's stdin ahead of
// whatever the caller sends. Host-gated and toolchain-gated like the rest.
func TestRunOnKlTargetFeedsTheProgramOnStdin(t *testing.T) {
	host := checkHost(t)
	if traceTargets(t, host) == nil {
		t.Skip("no go toolchain or no sibling shen-go checkout")
	}
	dir := t.TempDir()
	if _, err := shake("tests/fib.shen", dir, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	argv, err := build("kl", dir, false)
	if err != nil {
		t.Fatalf("build --target kl: %v", err)
	}
	if argv == nil {
		t.Skip("the kl builder's tools are not on PATH")
	}
	feed, err := programFileFor("kl", dir)
	if err != nil {
		t.Fatal(err)
	}
	if feed == "" {
		t.Fatal("kl declares a program_file; programFileFor resolved it to nothing")
	}
	if _, err := os.Stat(feed); err != nil {
		t.Fatalf("the program-file build step wrote no %s: %v", feed, err)
	}
	// Without it the VM would read the caller's stdin as its program and
	// print nothing of the fixture: that is the run this target needs and
	// the reason the fact exists.
	out, _, err := runCapture(argv, feed, "")
	if err != nil {
		// cmd/kl ends by recovering a panic at EOF, so a non-zero exit is
		// the normal case here; the transcript is the evidence.
		t.Logf("the kl VM exited non-zero (it panics at EOF): %v", err)
	}
	want, err := os.ReadFile("tests/fib.expected")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(canon(out), canon(string(want))) {
		t.Errorf("the kl transcript does not contain %q", canon(string(want)))
	}
	// Containment is what this test claims about the PLUMBING: the program
	// reached the VM on stdin and ran. Up to shen-go da55c5d the same
	// transcript also carried the boot panic, and that was LOGGED here
	// rather than asserted, because this test is about stdin delivery. At
	// shen-go 30ab469 the boot is clean, so the marker check is an
	// assertion: a marker in this transcript is a regression in the port.
	if m, line := transcriptError(transcriptErrorMarkers("kl"), out); m != "" {
		t.Errorf("the kl transcript carries the declared error marker %q at %q. At the "+
			"shen-go pinned in .github/shen-go.ref (30ab469) the kl boot is clean, so a "+
			"marker here means the port regressed", m, line)
	}
	// And with no program file the same argv produces no such answer, which
	// is what makes the assertion above about the plumbing rather than about
	// the VM having the program from somewhere else.
	bare, _, _ := runCapture(argv, "", "")
	if strings.Contains(canon(bare), canon(string(want))) {
		t.Error("the VM printed the fixture's answer with no program fed to it")
	}
}

// hickey-13 follow-up, and the one that mattered most: containment is not a
// verdict. `trace-check --target kl` printed OK on every fixture while the
// transcript carried
//
//	Panic: &{22 implementation error in shen.change-pointer-value}
//	Recovered in Eval: (shen.initialise)
//
// and a goroutine dump -- the VM recovers and runs the next toplevel form, so
// the fixture's answer WAS in the transcript and the comparison passed over a
// run whose initialiser had failed. The target now declares
// transcript_error_markers, and checkGolden checks them before it concludes
// anything from containment.
//
// That panic is gone at the shen-go pinned in .github/shen-go.ref (30ab469):
// every fixture's kl transcript is clean today, and TestTraceCheckFixtures
// asserts it. The markers stay declared and this test stays, on SYNTHETIC
// transcripts, because what it holds is the ordering -- markers before
// containment -- which is a property of the reader and not of whichever boot
// the pin currently has. A live-transcript version of this test would have
// gone green for the wrong reason.
//
// The markers come from builders.json, not from this file: a test that carried
// its own copy would pass while the declaration said something else.
func TestTranscriptErrorMarkersFailBeforeContainment(t *testing.T) {
	markers := transcriptErrorMarkers("kl")
	if len(markers) == 0 {
		t.Fatal("kl declares stdout=repl-transcript and no transcript_error_markers: " +
			"containment is then the whole verdict, which is what this test exists against")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "x.shen")
	if err := os.WriteFile(filepath.Join(dir, "x.expected"), []byte("fib 20 = 6765\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A clean transcript: prompts, echoes, the answer. Still passes.
	clean := "0- 1+ \nfib 20 = 6765\n1- done\n"
	g, err := checkGolden(prog, "kl", clean)
	if err != nil || !g.checked {
		t.Fatalf("a clean transcript containing the golden failed: %+v %v", g, err)
	}

	// The real one, per marker: the truth is in there, and it still fails.
	for _, m := range markers {
		t.Run(strings.TrimSpace(m), func(t *testing.T) {
			bad := "0- 1+ \n53 #> " + m + "&{22 implementation error in shen.change-pointer-value}\n" +
				"fib 20 = 6765\n1- done\n"
			if !strings.Contains(canon(bad), canon("fib 20 = 6765\n")) {
				t.Fatal("this fixture is meant to CONTAIN the truth")
			}
			g, err := checkGolden(prog, "kl", bad)
			if err == nil {
				t.Fatalf("a transcript carrying %q passed because the truth appeared after it: %+v", m, g)
			}
			if got := failSentinel(err); got != "yggdrasil-trace-check: FAIL stdout=transcript-error" {
				t.Errorf("sentinel = %q, want the transcript-error FAIL line", got)
			}
			if !strings.Contains(err.Error(), m) {
				t.Errorf("the failure does not name the marker it matched: %v", err)
			}
			if !strings.Contains(err.Error(), "shen.change-pointer-value") {
				t.Errorf("the failure does not quote the line it matched: %v", err)
			}
			if g.checked {
				t.Error("a refused comparison reports itself as checked")
			}
		})
	}

	// A target whose stdout is the program's is not affected: the markers
	// are a property of a transcript, and equality already fails on one.
	if _, err := checkGolden(prog, "go", "fib 20 = 6765\n"); err != nil {
		t.Errorf("an ordinary target's exact output was refused: %v", err)
	}

	// A fixture with NO committed golden must still fail on a marker. This
	// is the case that got away: the check lived inside the comparison, and
	// tests/partial.shen ships no .expected, so kl/partial reported OK over
	// the same panicking boot that failed kl/fib. A marker is a fact about
	// the run, so it cannot be conditional on a file the fixture may not
	// have.
	nogolden := filepath.Join(dir, "absent.shen")
	if g, err := checkGolden(nogolden, "kl", clean); err != nil || g.checked {
		t.Fatalf("a clean transcript with no golden must decline, not fail: %+v %v", g, err)
	}
	bad := "0- 1+ \n53 #> Panic: &{22 implementation error in shen.change-pointer-value}\nfib\n"
	g, err = checkGolden(nogolden, "kl", bad)
	if err == nil {
		t.Fatalf("a transcript carrying a marker passed because the fixture ships no golden: %+v", g)
	}
	if got := failSentinel(err); got != "yggdrasil-trace-check: FAIL stdout=transcript-error" {
		t.Errorf("sentinel = %q, want the transcript-error FAIL line", got)
	}

	// And an empty golden is a FAIL of its own, because containment holds
	// against every transcript there is.
	empty := filepath.Join(dir, "e.shen")
	if err := os.WriteFile(filepath.Join(dir, "e.expected"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = checkGolden(empty, "kl", clean)
	if err == nil {
		t.Fatalf("an empty golden passed by containment: %+v", g)
	}
	if got := failSentinel(err); got != "yggdrasil-trace-check: FAIL stdout=golden-empty" {
		t.Errorf("sentinel = %q, want the golden-empty FAIL line", got)
	}
	if g.checked {
		t.Error("a comparison that could not fail reports itself as checked")
	}
}
