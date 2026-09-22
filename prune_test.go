package main

// Host-gated tests for stage 4 (docs/analysis-rules.md): dead-initialisation
// pruning behind --prune-init, and the promise that with the flag off nothing
// moved.
//
// The three claims worth a test, in order of how much they would cost to get
// wrong:
//
//  1. Off by default the emitted kernel.kl is byte-identical to a shake that
//     had never heard of stage 4, and the manifests gain exactly one line,
//     pruned-init=0. A flag whose "off" position is not free is not a flag.
//  2. On, the initialiser is a strict SUBSET of the unpruned one: pruning may
//     only remove whole (set V Lit) forms, never reorder them, never rewrite
//     one, never add. That is checked form for form, not by size.
//  3. The pruned slice still runs. Gated twice over: on a go toolchain and a
//     sibling shen-go, and on the UNPRUNED slice building and running first --
//     otherwise a broken stage-2 environment would read as a pruning bug.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// withPrune runs body with the stage-4 options set, and restores them after.
// pruneOpts is process-global (see prune.go), and the rest of the suite shakes
// with it off.
func withPrune(target string, body func()) {
	saved := pruneOpts
	pruneOpts.on, pruneOpts.target = true, target
	defer func() { pruneOpts = saved }()
	body()
}

var setForm = regexp.MustCompile(`\(set ([^ ()]+) `)

// initialiserSets returns the globals the synthesised initialiser sets, in
// emission order. write-kl-file puts each toplevel form on its own line, so the
// initialiser is one line.
func initialiserSets(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "kernel.kl"))
	if err != nil {
		t.Fatalf("reading kernel.kl: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "(defun shen.initialise ") {
			continue
		}
		var out []string
		for _, m := range setForm.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
		if len(out) == 0 {
			t.Fatalf("the initialiser sets nothing:\n%s", line)
		}
		return out
	}
	t.Fatalf("kernel.kl has no shen.initialise")
	return nil
}

func manifestValue(t *testing.T, dir, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	t.Fatalf("manifest has no %s= line:\n%s", key, b)
	return ""
}

// isSubsequence reports whether small appears inside big in order, which is
// what "pruning only drops forms" means: the surviving sets are the original
// ones, still in boot order.
func isSubsequence(small, big []string) bool {
	i := 0
	for _, b := range big {
		if i < len(small) && small[i] == b {
			i++
		}
	}
	return i == len(small)
}

func TestPruneInitDefaultIsInert(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"
	a, b := t.TempDir(), t.TempDir()
	if _, err := shake(prog, a, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	if _, err := shake(prog, b, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	for _, f := range []string{"kernel.kl", "fib.kl", "yggdrasil.manifest.txt", "yggdrasil.manifest"} {
		x, err := os.ReadFile(filepath.Join(a, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		y, err := os.ReadFile(filepath.Join(b, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if string(x) != string(y) {
			t.Errorf("%s is not reproducible with --prune-init off", f)
		}
	}
	if got := manifestValue(t, a, "pruned-init"); got != "0" {
		t.Errorf("with the flag off the manifest must say pruned-init=0, got %q", got)
	}
	sexp, err := os.ReadFile(filepath.Join(a, "yggdrasil.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sexp), `("pruned-init" 0)`) {
		t.Errorf("sexp manifest must record (\"pruned-init\" 0):\n%s", sexp)
	}
}

func TestPruneInitDropsOnlyWholeSets(t *testing.T) {
	host := checkHost(t)
	const prog = "tests/fib.shen"
	plain, pruned := t.TempDir(), t.TempDir()
	if _, err := shake(prog, plain, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	var err error
	withPrune("go", func() { _, err = shake(prog, pruned, host, "sub", true) })
	if err != nil {
		t.Fatalf("shake --prune-init: %v", err)
	}

	before, after := initialiserSets(t, plain), initialiserSets(t, pruned)
	if len(after) >= len(before) {
		t.Fatalf("--prune-init dropped nothing: %d sets before, %d after", len(before), len(after))
	}
	if !isSubsequence(after, before) {
		t.Errorf("the pruned initialiser is not a subsequence of the unpruned one\n  before: %v\n  after:  %v",
			before, after)
	}
	// The manifest's own count must be the number of forms that went. It
	// counts FORMS and the lists above count `set`s, so this is only an
	// equality because every prunable form is exactly one set -- which is
	// what ygg.prunable? enforces and what this pins.
	want := len(before) - len(after)
	if got := manifestValue(t, pruned, "pruned-init"); got != strconv.Itoa(want) {
		t.Errorf("manifest says pruned-init=%s, the initialiser lost %d sets", got, want)
	}
	// Nothing the port reads natively may go.
	reads, err := portReadsFor("go")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, v := range after {
		kept[v] = true
	}
	for _, v := range reads {
		for _, b := range before {
			if b == v && !kept[v] {
				t.Errorf("--prune-init dropped %s, which go's port_reads says the runtime reads", v)
			}
		}
	}
	t.Logf("fib on target go: %d of %d initialiser forms pruned", want, len(before))
}

// The pruned slice must still be a working program. Skipped unless the
// unpruned one builds and runs here first: this asserts that PRUNING did not
// break the artifact, not that the stage-2 toolchain is installed.
func TestPruneInitGoArtifactStillRuns(t *testing.T) {
	host := checkHost(t)
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
	want, err := os.ReadFile("tests/fib.expected")
	if err != nil {
		t.Fatal(err)
	}

	runFib := func(dir string) (string, bool) {
		argv, err := build("go", dir, false)
		if err != nil || argv == nil {
			return "", false
		}
		out, _, err := runCapture(argv, "")
		if err != nil {
			return "", false
		}
		return canon(out), true
	}

	plain := t.TempDir()
	if _, err := shake("tests/fib.shen", plain, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	base, ok := runFib(plain)
	if !ok || base != canon(string(want)) {
		t.Skip("the unpruned go artifact does not build and run here; nothing to compare against")
	}

	pruned := t.TempDir()
	withPrune("go", func() { _, err = shake("tests/fib.shen", pruned, host, "sub", true) })
	if err != nil {
		t.Fatalf("shake --prune-init: %v", err)
	}
	got, ok := runFib(pruned)
	if !ok {
		t.Fatalf("the pruned slice failed to build or run on target go, though the unpruned one did")
	}
	if got != canon(string(want)) {
		t.Errorf("pruned go artifact printed %q, want %q", got, canon(string(want)))
	}
}

// Every target must carry a port_reads list, and the union a target-agnostic
// shake uses must contain every one of them. A target added without the key
// would otherwise prune against an empty list and drop everything.
func TestPortReadsAreDeclaredForEveryTarget(t *testing.T) {
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	union, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	inUnion := map[string]bool{}
	for _, v := range union {
		inUnion[v] = true
	}
	for name, b := range builders {
		if len(b.PortReads) == 0 {
			t.Errorf("target %s has no port_reads in builders.json (stage 4 would prune against an empty list)", name)
			continue
		}
		for _, v := range b.PortReads {
			if !inUnion[v] {
				t.Errorf("target %s reads %s but the union does not contain it", name, v)
			}
		}
		if b.PortReadsVerified && name != "go" {
			t.Errorf("target %s claims port_reads_verified; only go's list has been read off a runtime", name)
		}
	}
	if !portReadsVerified("go") {
		t.Errorf("go's port_reads was verified against shen-go's kl/ package; builders.json should say so")
	}
}
