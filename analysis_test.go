package main

// The stage-1 Datalog oracle, as a test.
//
// docs/analysis-rules.md restates the shake as a rule set. The claim that
// makes stage 1 worth anything is that the rules describe the shake that
// exists, not a shake someone would like to have: for every fixture, in both
// eval modes, the `reach` relation computed from the dumped facts must equal
// exactly the defuns the shake writes to kernel.kl. (Minus shen.initialise,
// which is synthesised at write time from the toplevel init forms and has no
// row in the call graph, so no rule can or should derive it.)
//
// Two engines evaluate the same analysis/analysis.dl: real Soufflé when it
// is on PATH -- which is what CI runs, see .github/workflows/analysis-oracle.yml
// -- and analysis/refeval.py otherwise. When both are available the test also
// diffs them against each other, because a rule set that two independent
// evaluators read differently is a rule set nobody can rely on.
//
// Host-gated like check_test.go: every case boots a real stage-1 host.

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// kernelDefuns reads the defun names out of a shaken kernel.kl, dropping the
// synthesised initialiser. write-kl-file emits one form per line starting at
// column 0, so a prefix match is exact here.
func kernelDefuns(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	defer f.Close()
	names := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "(defun ") {
			continue
		}
		rest := line[len("(defun "):]
		if i := strings.IndexAny(rest, " ()"); i >= 0 {
			rest = rest[:i]
		}
		if rest != "" && rest != "shen.initialise" {
			names[rest] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning %s: %v", path, err)
	}
	if len(names) == 0 {
		t.Fatalf("%s has no defuns", path)
	}
	return names
}

// relWithRefeval evaluates analysis.dl's rules with the Python reference
// evaluator and returns one output relation.
func relWithRefeval(t *testing.T, factsDir, rel string) map[string]bool {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 and no souffle: cannot evaluate the rules")
	}
	out, err := exec.Command(py, filepath.Join("analysis", "refeval.py"), factsDir, rel).CombinedOutput()
	if err != nil {
		t.Fatalf("refeval.py %s: %v\n%s", rel, err, out)
	}
	return lineSet(string(out))
}

// souffleRun compiles and runs analysis.dl with real Soufflé once, and
// returns a reader for its output relations.
func souffleRun(t *testing.T, souffle, factsDir string) func(string) map[string]bool {
	t.Helper()
	outDir := t.TempDir()
	cmd := exec.Command(souffle, "-F", factsDir, "-D", outDir, filepath.Join("analysis", "analysis.dl"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("souffle: %v\n%s", err, out)
	}
	return func(rel string) map[string]bool {
		csv, err := os.ReadFile(filepath.Join(outDir, rel+".csv"))
		if err != nil {
			t.Fatalf("souffle wrote no %s.csv: %v", rel, err)
		}
		return lineSet(string(csv))
	}
}

func lineSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = true
		}
	}
	return out
}

// diffSets returns what is in got but not want, and vice versa.
func diffSets(got, want map[string]bool) (extra, missing []string) {
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return
}

func sample(xs []string) []string {
	if len(xs) > 10 {
		return xs[:10]
	}
	return xs
}

// manifestComputedNames reads the shake's own computed-names= answer back
// out of the txt manifest, as a set. "none" is the empty set.
func manifestComputedNames(t *testing.T, shakeDir string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(shakeDir, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		v, ok := strings.CutPrefix(line, "computed-names=")
		if !ok {
			continue
		}
		out := map[string]bool{}
		if v == "none" || v == "" {
			return out
		}
		for _, name := range strings.Split(v, ",") {
			out[name] = true
		}
		return out
	}
	t.Fatalf("manifest has no computed-names= line")
	return nil
}

func TestAnalysisOracleMatchesShake(t *testing.T) {
	host := checkHost(t)

	fixtures, err := filepath.Glob(filepath.Join("tests", "*.shen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures in tests/: %v", err)
	}
	souffle, _ := exec.LookPath("souffle")
	if souffle == "" {
		t.Log("souffle not on PATH; evaluating the rules with analysis/refeval.py only")
	}

	for _, prog := range fixtures {
		prog := prog
		name := strings.TrimSuffix(filepath.Base(prog), ".shen")
		if name == "init-order-bad" {
			// Refused by the stage-2 init-order check on purpose; there is
			// no kernel.kl for the oracle to agree with.
			continue
		}
		t.Run(name, func(t *testing.T) {
			shakeDir, factsDir := t.TempDir(), t.TempDir()
			if _, err := shake(prog, shakeDir, host, "sub", true); err != nil {
				t.Fatalf("shake: %v", err)
			}
			if _, err := facts(prog, factsDir, host, "sub", true); err != nil {
				t.Fatalf("facts: %v", err)
			}
			want := kernelDefuns(t, filepath.Join(shakeDir, "kernel.kl"))

			py := relWithRefeval(t, factsDir, "reach")
			if extra, missing := diffSets(py, want); len(extra)+len(missing) > 0 {
				t.Errorf("refeval reach != kernel.kl defuns (%d vs %d)\n  extra: %v\n  missing: %v",
					len(py), len(want), sample(extra), sample(missing))
			}

			// The stage-3 computed-name relation decides nothing, so it has
			// no kernel.kl to be checked against; what it does have is the
			// shake's own answer, in the manifest. All three must agree.
			pyCN := relWithRefeval(t, factsDir, "computedName")
			if extra, missing := diffSets(pyCN, manifestComputedNames(t, shakeDir)); len(extra)+len(missing) > 0 {
				t.Errorf("refeval computedName != the manifest's computed-names\n  only rules: %v\n  only manifest: %v",
					sample(extra), sample(missing))
			}

			// Stage 4's deadInit decides nothing on this path either (the
			// shake under test ran with --prune-init off, so its pruned-init=
			// is 0 by construction); what it must do is mean the same thing to
			// both engines, over the reach set they have just agreed on.
			pyDead := relWithRefeval(t, factsDir, "deadInit")

			if souffle != "" {
				rel := souffleRun(t, souffle, factsDir)
				so := rel("reach")
				if extra, missing := diffSets(so, want); len(extra)+len(missing) > 0 {
					t.Errorf("souffle reach != kernel.kl defuns (%d vs %d)\n  extra: %v\n  missing: %v",
						len(so), len(want), sample(extra), sample(missing))
				}
				if extra, missing := diffSets(so, py); len(extra)+len(missing) > 0 {
					t.Errorf("souffle and refeval.py disagree on the same rules\n  only souffle: %v\n  only refeval: %v",
						sample(extra), sample(missing))
				}
				if extra, missing := diffSets(rel("computedName"), pyCN); len(extra)+len(missing) > 0 {
					t.Errorf("souffle and refeval.py disagree on computedName\n  only souffle: %v\n  only refeval: %v",
						sample(extra), sample(missing))
				}
				if extra, missing := diffSets(rel("deadInit"), pyDead); len(extra)+len(missing) > 0 {
					t.Errorf("souffle and refeval.py disagree on deadInit\n  only souffle: %v\n  only refeval: %v",
						sample(extra), sample(missing))
				}
			}
		})
	}
}

// The fact dump must not perturb the shake. facts() and shake() are separate
// host processes over the same pipeline; shaking either side of a dump has to
// give the same bytes, or the oracle is measuring something the user never
// gets.
func TestFactsDoesNotChangeTheShake(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"

	before, factsDir, after := t.TempDir(), t.TempDir(), t.TempDir()
	if _, err := shake(prog, before, host, "sub", true); err != nil {
		t.Fatalf("shake before: %v", err)
	}
	if _, err := facts(prog, factsDir, host, "sub", true); err != nil {
		t.Fatalf("facts: %v", err)
	}
	if _, err := shake(prog, after, host, "sub", true); err != nil {
		t.Fatalf("shake after: %v", err)
	}
	for _, f := range []string{"kernel.kl", "fib.kl", "yggdrasil.manifest.txt"} {
		a, err := os.ReadFile(filepath.Join(before, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		b, err := os.ReadFile(filepath.Join(after, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s changed across a fact dump", f)
		}
	}
	// Every relation analysis.dl declares .input for is on disk, even if
	// empty: a missing one is a Soufflé error, and worse, a silently
	// smaller reach set under refeval.py.
	for _, rel := range factRelations {
		if _, err := os.Stat(filepath.Join(factsDir, rel+".facts")); err != nil {
			t.Errorf("fact dump is missing %s.facts", rel)
		}
	}
}
