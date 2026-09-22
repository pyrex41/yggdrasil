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
	"os"
	"os/exec"
	"path/filepath"
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
	traceMode = true
	_, err := shake(prog, traced, host, "sub", true)
	traceMode = false
	if err != nil {
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
	clean := append([]string{"fib", "ygg.traced"}, inFootprint...)
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
				if !contains(res.called, c.userFn) {
					t.Errorf("called does not include the user function %q (got %d names)",
						c.userFn, len(res.called))
				}
				t.Logf("%s: %s (readGlobal=%d, %d records)",
					name, res.sentinel, len(res.reads), res.records)
			})
		}
	}
}
