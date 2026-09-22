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

// Every target must RESOLVE to a port_reads list, and the union a
// target-agnostic shake uses must contain every one of them. A target added
// without a resolvable list would prune against an empty list and drop
// everything.
//
// Note what this does NOT require any more: that every target carry its own
// copy of the key. Twelve of them used to, byte for byte, which made a
// placeholder look like thirteen independent measurements. They now declare
// nothing and inherit builders.json's `_default`, and the assertion moved from
// "the key is present" to "the effective list is non-empty and inside the
// union" -- the property stage 4 actually needs.
func TestPortReadsAreDeclaredForEveryTarget(t *testing.T) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.PortReads) == 0 {
		t.Fatalf("builders.json has no %s.port_reads: every target without its own list "+
			"would prune against nothing", builderDefaultsKey)
	}
	union, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	inUnion := map[string]bool{}
	for _, v := range union {
		inUnion[v] = true
	}
	for name := range builders {
		reads, err := portReadsFor(name)
		if err != nil {
			t.Errorf("portReadsFor(%s): %v", name, err)
			continue
		}
		if len(reads) == 0 {
			t.Errorf("target %s resolves to no port_reads (stage 4 would prune against an empty list)", name)
			continue
		}
		for _, v := range reads {
			if !inUnion[v] {
				t.Errorf("target %s reads %s but the union does not contain it", name, v)
			}
		}
		if portReadsVerified(name) && name != "go" {
			t.Errorf("target %s claims a verified port_reads; only go's list has been read off a runtime", name)
		}
	}
	if !portReadsVerified("go") {
		t.Errorf("go's port_reads was read off shen-go's kl/ package and is covered by a named " +
			"test; builders.json's port_reads_checked_by should say so")
	}
}

// The twelve copies are gone, and must stay gone. A target that re-adds the
// default list verbatim has added no information and reintroduced the drift
// the `_default` block exists to prevent.
func TestOnlyDeclaredPortReadsDifferFromTheDefault(t *testing.T) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	same := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for name, b := range builders {
		if len(b.PortReads) == 0 {
			continue // inherits _default, which is the point
		}
		if same(b.PortReads, defaults.PortReads) {
			t.Errorf("target %s repeats builders.json %s.port_reads verbatim. Delete the key: "+
				"a copy of the default is not a declaration, and two copies drift.",
				name, builderDefaultsKey)
		}
		if !factSourced(b.PortReadsSource) {
			t.Errorf("target %s declares its own port_reads but no port_reads_source. "+
				"A list with no provenance is the placeholder again, wearing a target's name.", name)
		}
	}
}

// yggdrasil.shen carries the same conservative list, for a direct host
// invocation with no Go driver to push one in. builders.json's `_default` is
// the authority; this is what catches the copy drifting from it.
func TestPortReadsDefaultMatchesShen(t *testing.T) {
	_, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("yggdrasil.shen")
	if err != nil {
		t.Fatal(err)
	}
	shen := shenPortReadsDefault(t, string(src))

	want, got := defaults.PortReads, shen
	if len(want) != len(got) {
		t.Errorf("builders.json %s.port_reads has %d names, yggdrasil.shen's "+
			"(set ygg.*port-reads* ...) has %d", builderDefaultsKey, len(want), len(got))
	}
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] != got[i] {
			t.Fatalf("the two default port_reads lists diverge at position %d: "+
				"builders.json says %q, yggdrasil.shen says %q", i, want[i], got[i])
		}
	}
	if t.Failed() {
		inShen := map[string]bool{}
		for _, v := range got {
			inShen[v] = true
		}
		for _, v := range want {
			if !inShen[v] {
				t.Errorf("  only in builders.json %s: %s", builderDefaultsKey, v)
			}
		}
		inJSON := map[string]bool{}
		for _, v := range want {
			inJSON[v] = true
		}
		for _, v := range got {
			if !inJSON[v] {
				t.Errorf("  only in yggdrasil.shen: %s", v)
			}
		}
	}
}

// shenPortReadsDefault reads the symbol list out of yggdrasil.shen's
// (set ygg.*port-reads* [...]) form. The form is one bracketed list of
// self-evaluating symbols with `\\` line comments in it, so dropping the
// comments and splitting on whitespace is the whole parse.
func shenPortReadsDefault(t *testing.T, src string) []string {
	t.Helper()
	const open = "(set ygg.*port-reads*"
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatalf("yggdrasil.shen has no %s form", open)
	}
	rest := src[i+len(open):]
	lb := strings.Index(rest, "[")
	rb := strings.Index(rest, "]")
	if lb < 0 || rb < lb {
		t.Fatalf("yggdrasil.shen's %s form has no [...] list", open)
	}
	var out []string
	for _, line := range strings.Split(rest[lb+1:rb], "\n") {
		if c := strings.Index(line, `\\`); c >= 0 {
			line = line[:c]
		}
		for _, f := range strings.Fields(line) {
			if f = strings.TrimLeft(f, "["); f != "" {
				out = append(out, f)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("yggdrasil.shen's %s form parsed as empty", open)
	}
	return out
}

// native_overrides had a `_verified: true` flag, a provenance string, and no
// consumer whatsoever -- nothing read the key, so nothing could contradict it.
// It was wrong: the declared list was four symbols short of what
// InstallKernelFast actually rebinds (<-vector, ==, @p, shen.hds=?). This is
// the test the report's `checked_by` names, and the reason the word "verified"
// now costs something.
//
// Skipped without a sibling shen-go checkout: the fact is about that source,
// and there is nothing to compare against when it is absent.
func TestNativeOverridesMatchKernelFast(t *testing.T) {
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	b := builders["go"]
	if len(b.NativeOverrides) == 0 {
		t.Fatal("builders.json's go entry declares no native_overrides")
	}
	if b.NativeOverridesInstalledAfter != "shen.initialise" {
		t.Errorf("go's native_overrides_installed_after is %q; shen-go's generated main runs "+
			"shen.initialise before InstallKernelFast, so the overrides are installed AFTER "+
			"boot and the kernel's KL bodies do run", b.NativeOverridesInstalledAfter)
	}

	path := filepath.Join(siblingDir("go", b), "kl", "kernelfast.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no sibling shen-go checkout to read %s from", path)
	}
	got := kernelFastRebindings(t, string(src))

	declared := map[string]bool{}
	for _, n := range b.NativeOverrides {
		declared[n] = true
	}
	for n := range got {
		if !declared[n] {
			t.Errorf("InstallKernelFast rebinds %q, which builders.json's native_overrides "+
				"does not list. Level-3 delta accounting would then treat %q as a kept "+
				"defun that is never entered, i.e. a finding.", n, n)
		}
	}
	for n := range declared {
		if !got[n] {
			t.Errorf("builders.json lists %q as a native override, but InstallKernelFast in "+
				"%s does not rebind it", n, path)
		}
	}
	if !t.Failed() {
		t.Logf("%d native overrides, matching InstallKernelFast in %s", len(declared), path)
	}
}

// kernelFastRebindings returns the kernel names InstallKernelFast binds to
// natives. Every rebinding in that function goes through one of five calls,
// each taking the KL name as its first string literal.
var kernelFastRebindRe = regexp.MustCompile(
	`(?:overridePrimitive|overrideNative|restoreCanonicalPrimitive|canonicalOrMake)\("([^"]+)"|` +
		`BindSymbolFunc\(MakeSymbol\("([^"]+)"\)`)

func kernelFastRebindings(t *testing.T, src string) map[string]bool {
	t.Helper()
	const fn = "func InstallKernelFast()"
	i := strings.Index(src, fn)
	if i < 0 {
		t.Skipf("the sibling shen-go's kl/kernelfast.go has no %s; it has been restructured "+
			"and native_overrides needs re-deriving by hand", fn)
	}
	out := map[string]bool{}
	for _, m := range kernelFastRebindRe.FindAllStringSubmatch(src[i:], -1) {
		if m[1] != "" {
			out[m[1]] = true
		} else if m[2] != "" {
			out[m[2]] = true
		}
	}
	if len(out) == 0 {
		t.Skipf("parsed no rebindings out of %s; the parser no longer matches its shape", fn)
	}
	return out
}

// The contract report is the consumer native_overrides did not have. It must
// print the phase: an override list with no "installed after what" reads as
// "these KL bodies never run", and on shen-go they do run -- shen.initialise
// executes before InstallKernelFast.
func TestContractReportNamesSourceAndPhase(t *testing.T) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rawBuilders()
	if err != nil {
		t.Fatal(err)
	}
	rows := func(target string) map[string]contractRow {
		out := map[string]contractRow{}
		for _, r := range contractRows(raw[target], raw[builderDefaultsKey], builders[target]) {
			out[r.key] = r
		}
		return out
	}

	goRows := rows("go")
	for _, key := range []string{"port_reads", "native_overrides"} {
		r, ok := goRows[key]
		if !ok {
			t.Fatalf("the contract report has no %s row for go", key)
		}
		if r.status != "verified" {
			t.Errorf("go's %s reports %q; it has a source and a named test, so it is verified",
				key, r.status)
		}
		if !factSourced(r.source) {
			t.Errorf("go's %s row prints no source", key)
		}
		if !factChecked(r.checkedBy) {
			t.Errorf("go's %s row prints no checked_by", key)
		}
	}
	if nov := goRows["native_overrides"]; !strings.Contains(nov.summary, "installed_after=shen.initialise") {
		t.Errorf("go's native_overrides row does not say when the natives are installed: %q", nov.summary)
	}

	// A fact nobody declared must read as unknown, not as a silent absence
	// and not as a pass.
	for _, key := range []string{"port_writes", "native_deps", "call_style", "dispatch"} {
		r, ok := goRows[key]
		if !ok {
			t.Fatalf("the contract report omits the undeclared key %s entirely; absence of a "+
				"fact must look like absence, which means a row saying unknown", key)
		}
		if r.status != "unknown" {
			t.Errorf("%s is not declared for go but the report says %q", key, r.status)
		}
	}

	// An inheriting target must say so, and must never read as verified.
	luaRows := rows("lua")
	pr := luaRows["port_reads"]
	if !pr.inherited {
		t.Errorf("lua inherits port_reads from %s; the report must say so", builderDefaultsKey)
	}
	if pr.status != "declared" {
		t.Errorf("lua's inherited port_reads reports %q; an unmeasured placeholder is declared, "+
			"never verified", pr.status)
	}
	if factChecked(defaults.PortReadsCheckedBy) {
		t.Errorf("builders.json %s.port_reads claims a checked_by (%q); nothing checks the "+
			"conservative default against any runtime", builderDefaultsKey, defaults.PortReadsCheckedBy)
	}
}
