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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// traceTargets returns the targets a run-based trace check can use here, in
// preference order, or nil when there is no usable runtime.
//
// `kl` (shen-go's bare KLambda VM) is tried whenever a Go toolchain and the
// sibling shen-go checkout are present. `go` is added only when its stage-2
// builder actually works, established by building an untraced fixture: the
// builder boots its own full-kernel compiler image, which is a much larger
// thing to go wrong than the shake, and a broken one must not read as a
// tracing regression. See the caveat in docs/analysis-rules.md.
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
	targets := []string{klTarget}

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
	reach := kernelDefuns(t, filepath.Join(shakeDir, "kernel.kl"))

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
					defaultStdin(c.prog), host, "sub")
				if name := skipName(err); name != "" {
					// A named skip, never a silent pass: the kl
					// runner cannot deliver a fixture stdin, so on
					// kl/stdin-sum there is no run to draw evidence
					// from. See evidencePossible.
					t.Skipf("no evidence obtainable here: %s (%v)", name, err)
				}
				if err != nil {
					t.Fatalf("trace-check: %v", err)
				}
				if res == nil {
					t.Skipf("target %s is not runnable here", target)
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
	res, err := traceCheck("tests/fib.shen", t.TempDir(), "go", "", host, "sub")
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
	for f := range kernelDefuns(t, filepath.Join(shakeDir, "kernel.kl")) {
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
	for f := range kernelDefuns(t, filepath.Join(shakeDir, "kernel.kl")) {
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
	if err := writeTraceMeta(factsDir, meta); err != nil {
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
	if err := writeTraceMeta(factsDir, &traceFacts{called: called, lines: 1}); err != nil {
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
	if err := writeTraceMeta(factsDir, &traceFacts{complete: true, called: cut, lines: 1}); err != nil {
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
	if err := writeTraceMeta(dir, tf); err != nil {
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
	if err := writeTraceMeta(dir, &traceFacts{}); err != nil {
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
		g, err := checkGolden(prog, klTarget, "0- 1+ \nfib 20 = 6765\n1- done\n")
		if err != nil {
			t.Fatalf("the golden embedded in a REPL transcript failed: %v", err)
		}
		if !g.checked || g.how != "contained in the kl transcript" {
			t.Errorf("g = %+v", g)
		}
		if _, err := checkGolden(prog, klTarget, "0- 1+ \nfib 20 = 6764\n"); err == nil {
			t.Error("a kl transcript without the golden in it passed")
		}
	})
}

// TestEvidencePossible: the one target/fixture pair on which a trace cannot
// be evidence must be refused UP FRONT and BY NAME, not run and reported OK.
//
// shen-go's cmd/kl reads its program from os.Stdin and takes no file
// argument, so klRunner has to append the fixture's stdin to the KL forms;
// the VM eats them as toplevel forms and tests/stdin-sum answers
// "bytes: 0 digest: 0" against a golden of "bytes: 15 digest: 12410". The
// earlier code declined the stdout comparison and carried on, so a trace of
// that run produced `yggdrasil-trace-check: OK` and a green fixture test.
func TestEvidencePossible(t *testing.T) {
	err := evidencePossible(klTarget, "tests/stdin-sum.stdin")
	if err == nil {
		t.Fatal("kl + a fixture stdin was accepted: the VM eats the stdin bytes as toplevel forms, " +
			"so the run answers the wrong thing and its trace is not evidence")
	}
	if got := skipName(err); got != "kl-runner-cannot-deliver-stdin" {
		t.Errorf("skipName = %q, want the named skip", got)
	}
	// A named skip is not a verdict: it must not print on the FAIL line.
	if got := failSentinel(err); got != "" {
		t.Errorf("a skip was reported as a failure verdict: %q", got)
	}
	// Everything else is allowed through.
	for _, c := range []struct{ target, stdin string }{
		{klTarget, ""}, {"go", "tests/stdin-sum.stdin"}, {"go", ""},
	} {
		if err := evidencePossible(c.target, c.stdin); err != nil {
			t.Errorf("evidencePossible(%q, %q) = %v, want nil", c.target, c.stdin, err)
		}
	}
	if got := skipName(errors.New("plain")); got != "" {
		t.Errorf("skipName invented a skip for a plain error: %q", got)
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
