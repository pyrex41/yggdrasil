package main

// Host-gated tests for stage 3 of docs/analysis-rules.md: the shake's
// footprint is now the least fixpoint of (value *shake-rules*), evaluated by
// the ygg.dl engine in yggdrasil.shen. The worklist `reach` and the Warshall
// closure stay in the file as differential oracles, and
// (yggdrasil.footprints ...) is the test-only entry point that runs all
// three over one fixture and compares them.
//
// Also here: the computed-name hypothesis, which warns and changes nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runFootprints evaluates (yggdrasil.footprints ["prog"]) in a host process
// and returns its sentinel line. Same trust model as shake/why/facts: the
// sentinel decides, never the exit code.
func runFootprints(prog string, host []string) (string, error) {
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", fmt.Errorf("no Shen host launcher found")
	}
	prog, _ = filepath.Abs(prog)
	root, err := yggRoot()
	if err != nil {
		return "", fmt.Errorf("materialising shaker: %w", err)
	}
	expr := fmt.Sprintf(`(yggdrasil.footprints ["%s"])`, prog)
	argv := append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	out, _ := runAt(wrapExecutable(argv), root)
	i := strings.Index(out, "yggdrasil-footprints:")
	if i < 0 {
		os.Stderr.WriteString(out)
		return "", fmt.Errorf("footprints produced no report (host=%s)", strings.Join(host, " "))
	}
	line := out[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return strings.TrimRight(line, "\r"), nil
}

// The three engines must agree on every fixture that shakes. One eval-free
// and one eval-capable fixture is the interesting split; the Warshall leg
// only runs under its size limit and says "skipped" above it, which is a
// pass, not a silent one.
func TestFootprintEnginesAgree(t *testing.T) {
	host := checkHost(t)
	for _, prog := range []string{
		"tests/fib.shen",
		"tests/interpreter.shen",
		"tests/computed-name.shen",
		"tests/metaeval.shen",
	} {
		prog := prog
		t.Run(filepath.Base(prog), func(t *testing.T) {
			line, err := runFootprints(prog, host)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(line, "agree=true") {
				t.Fatalf("the three footprint engines disagree: %s", line)
			}
			t.Log(line)
		})
	}
}

// The computed-name rule warns, names the containing defun, records the
// same answer in both manifests, and does not refuse the program.
func TestComputedNameWarnsAndRecords(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()

	// shake() swallows host output when quiet; run it loudly so the WARN
	// line lands on this process's stderr and is visible in -v.
	if _, err := shake("tests/computed-name.shen", out, host, "sub", true); err != nil {
		t.Fatalf("a computed name must not refuse the shake: %v", err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txt), "computed-names=computed-call") {
		t.Errorf("manifest.txt must name the defun that computes a name:\n%s", txt)
	}
	sexp, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sexp), `("computed-names" "computed-call")`) {
		t.Errorf("sexp manifest must record the same:\n%s", sexp)
	}
}

// A fixture with no computed name says so explicitly rather than leaving
// the key out: a builder reading computed-names= should never have to guess
// whether the shake looked.
func TestComputedNameNoneIsRecorded(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/fib.shen", out, host, "sub", true); err != nil {
		t.Fatal(err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txt), "computed-names=none") {
		t.Errorf("manifest.txt must record computed-names=none:\n%s", txt)
	}
}

// The WARN sentinel is what a builder greps for; check the shaker really
// emits it, not just the manifest key.
func TestComputedNameWarnSentinel(t *testing.T) {
	host := checkHost(t)
	if host == nil {
		host = defaultHost()
	}
	root, err := yggRoot()
	if err != nil {
		t.Fatal(err)
	}
	prog, _ := filepath.Abs("tests/computed-name.shen")
	dir := t.TempDir()
	expr := fmt.Sprintf(`(yggdrasil.shake ["%s"] "%s")`, prog, dir)
	argv := append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	out, _ := runAt(wrapExecutable(argv), root)
	if !strings.Contains(out, "yggdrasil-shake: WARN computed-name in computed-call") {
		t.Errorf("shake must warn about the computed name:\n%s", out)
	}
}
