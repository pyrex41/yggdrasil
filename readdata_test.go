package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Read-data mode (#27; yggdrasil.shen, "read-data mode"). A program that
// declares (set yggdrasil.*read-data* true) and mentions no eval entry point
// outside the reader family shakes eval-free, with the reader kept and the
// reader's own paths to eval replaced by named errors. These tests hold the
// two halves #27 asked for -- the declared reader shakes needs-eval=false,
// and a read value that does reach eval still demands needs-eval=true --
// plus the equivalence obligation the replacement carries.

func shakeManifest(t *testing.T, prog string, host []string) string {
	t.Helper()
	out := t.TempDir()
	if _, err := shake(prog, out, host, "sub", true); err != nil {
		t.Fatalf("shake %s: %v", prog, err)
	}
	b, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReadDataModeIsDecidedByDeclarationAndEntryPoints(t *testing.T) {
	host := checkHost(t)

	m := shakeManifest(t, "tests/read-data.shen", host)
	if v := manifestKey(t, m, "needs-eval"); v != "false" {
		t.Errorf("read-data: needs-eval=%s, want false", v)
	}
	if v := manifestKey(t, m, "read-data"); v != "true" {
		t.Errorf("read-data: read-data=%s, want true", v)
	}
	if !strings.Contains(m, "\ncannot-reach=eval\n") {
		t.Errorf("read-data: the manifest does not certify cannot-reach=eval:\n%s", m)
	}

	// The falsifying half: the same declaration, but the read value is
	// evaluated. The declaration must be inert.
	m = shakeManifest(t, "tests/read-data-eval.shen", host)
	if v := manifestKey(t, m, "needs-eval"); v != "true" {
		t.Errorf("read-data-eval: needs-eval=%s, want true: the read value reaches eval", v)
	}
	if strings.Contains(m, "read-data=") {
		t.Errorf("read-data-eval: an eval-capable shake wrote a read-data= key:\n%s", m)
	}

	// And without the declaration, `read` is an eval entry point as it
	// always was.
	src, err := os.ReadFile("tests/read-data.shen")
	if err != nil {
		t.Fatal(err)
	}
	undeclared := strings.Replace(string(src), "(set yggdrasil.*read-data* true)", "", 1)
	if undeclared == string(src) {
		t.Fatal("tests/read-data.shen no longer carries the declaration this test removes")
	}
	prog := filepath.Join(t.TempDir(), "undeclared.shen")
	if err := os.WriteFile(prog, []byte(undeclared), 0o644); err != nil {
		t.Fatal(err)
	}
	m = shakeManifest(t, prog, host)
	if v := manifestKey(t, m, "needs-eval"); v != "true" {
		t.Errorf("undeclared: needs-eval=%s, want true: read is an eval entry point unless declared data", v)
	}
	if strings.Contains(m, "read-data=") {
		t.Errorf("undeclared: wrote a read-data= key:\n%s", m)
	}
}

// goArtifact shakes prog (the full program with full set) and builds it for
// go, skipping when the go builder is not available.
func goArtifact(t *testing.T, prog string, host []string, full bool) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(siblingDir("go", builders["go"]), "cmd", "yggdrasil-build")); err != nil {
		t.Skip("no sibling shen-go checkout")
	}
	dir := t.TempDir()
	if _, err := shakeMode(prog, dir, host, "sub", true, shakeOpts{full: full}); err != nil {
		t.Fatalf("shake %s (full=%v): %v", prog, full, err)
	}
	argv, err := build("go", dir, false)
	if err != nil || argv == nil {
		t.Fatalf("go build of %s (full=%v): %v", prog, full, err)
	}
	return argv
}

func runWithStdin(t *testing.T, argv []string, stdin string) string {
	t.Helper()
	a := wrapExecutable(argv)
	cmd := exec.Command(a[0], a[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// The obligation: where the full kernel's reader evaluates nothing, the
// read-data slice reads the same values. The full program (every kernel
// defun, no stripping) is the reference; both run the fixture's stdin, and
// both must print the committed golden.
func TestReadDataSliceReadsWhatTheFullKernelReads(t *testing.T) {
	host := checkHost(t)
	stdin, err := os.ReadFile("tests/read-data.stdin")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("tests/read-data.expected")
	if err != nil {
		t.Fatal(err)
	}
	slice := canon(runWithStdin(t, goArtifact(t, "tests/read-data.shen", host, false), string(stdin)))
	full := canon(runWithStdin(t, goArtifact(t, "tests/read-data.shen", host, true), string(stdin)))
	if slice != full {
		t.Errorf("the read-data slice and the full program read differently\n--- slice:\n%s\n--- full:\n%s", slice, full)
	}
	if slice != canon(string(want)) {
		t.Errorf("read-data slice printed\n%s\nwant (tests/read-data.expected)\n%s", slice, want)
	}
}

// And where the full kernel's reader WOULD evaluate, the slice refuses by
// name instead. Each input is one of the forms whose expansion evaluates
// code; the last two are controls that read without evaluating and must
// not trip a refusal.
func TestReadDataSliceRefusesFormsThatEvaluate(t *testing.T) {
	host := checkHost(t)
	argv := goArtifact(t, "tests/read-data.shen", host, false)
	for _, tc := range []struct {
		input, want string
	}{
		{"(defmacro m-macro [m X] -> X) end", "read a (defmacro ...) form"},
		{"(package p [(+ 1 2)] (a b)) end", "read a (package Name Exceptions ...) form"},
		{"(synonyms num number) end", "read a (synonyms ...) form"},
		{"(datatype t X : number; ______ X : t;) end", "read a (datatype ...) form"},
		{"(define newfn X -> X) end", "read a (define ...) form for a new function newfn"},
	} {
		out := runWithStdin(t, argv, tc.input+"\n")
		if !strings.Contains(out, "yggdrasil read-data: "+tc.want) {
			t.Errorf("input %q: want the %q refusal, got:\n%s", tc.input, tc.want, out)
		}
		if strings.Contains(out, "forms: ") {
			t.Errorf("input %q: the program ran past a refused read:\n%s", tc.input, out)
		}
	}
	for _, input := range []string{
		"(define append X Y -> X) end", // arity already known: nothing evaluated
		"(input) end",                  // a macro whose expansion NAMES input
	} {
		out := runWithStdin(t, argv, input+"\n")
		if strings.Contains(out, "yggdrasil read-data:") || !strings.Contains(out, "forms: 1") {
			t.Errorf("input %q reads without evaluating and must not be refused, got:\n%s", input, out)
		}
	}
}
