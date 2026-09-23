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
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// withPrune is the stage-4 options a --prune-init shake for target runs under.
// Nothing to save or restore: the mode is an argument to shake(), so a test
// that asks for pruning cannot leak it into the rest of the suite.
func withPrune(target string) shakeOpts {
	return shakeOpts{pruneInit: true, target: target}
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
	if _, err := shake(prog, pruned, host, "sub", true, withPrune("go")); err != nil {
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
	reads, known, err := portReadsFor("go")
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("go declares a port_reads list; portReadsFor must report it as known")
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
		out, _, err := runCapture(argv, "", "")
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
	if _, err := shake("tests/fib.shen", pruned, host, "sub", true, withPrune("go")); err != nil {
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

// What every target must resolve to, now that most of them resolve to
// UNKNOWN.
//
// The old promise was "every target resolves to a non-empty list, and the
// union contains it". That was satisfiable only because `_default` held a
// 35-name guess every undeclared target inherited, which is the thing this
// commit deleted. The promises that survive it, and are the ones stage 4
// actually needs:
//
//  1. a target that DECLARES a list resolves to its own, and the
//     target-agnostic union contains every name in it -- otherwise a
//     no-target --prune-init would prune something a measured port reads;
//  2. a target that declares none resolves to unknown AND to the empty list,
//     so nothing downstream can attribute another port's globals to it --
//     `facts --target lua` wrote go's five into portReads.facts as lua's EDB
//     for exactly as long as this returned the union;
//  3. at least one target has declared a list, or the union is empty and
//     --prune-init has nothing to prune against at all;
//  4. only `go` is verified, and `go` is.
func TestPortReadsAreDeclaredForEveryTarget(t *testing.T) {
	builders, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.PortReads) != 0 {
		t.Errorf("builders.json %s.port_reads is a list of %d names again. It must be %q: a "+
			"default list is inherited by every unmeasured target, which makes absence of a "+
			"measurement look like one", builderDefaultsKey, len(defaults.PortReads), portReadsUnknown)
	}
	union, unionKnown, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	_ = union
	if len(union) == 0 {
		t.Fatalf("no target declares a port_reads list: the target-agnostic union is empty and " +
			"--prune-init has nothing to prune against")
	}
	declared, unknown, err := portReadsCoverage()
	if err != nil {
		t.Fatal(err)
	}
	if unionKnown != (len(unknown) == 0) {
		t.Errorf("portReadsFor(\"\") reports known=%v with %d undeclared targets", unionKnown, len(unknown))
	}
	inUnion := map[string]bool{}
	for _, v := range union {
		inUnion[v] = true
	}
	for name := range builders {
		reads, known, err := portReadsFor(name)
		if err != nil {
			t.Errorf("portReadsFor(%s): %v", name, err)
			continue
		}
		if known != contains(declared, name) {
			t.Errorf("portReadsFor(%s) reports known=%v; portReadsCoverage puts it in the other half",
				name, known)
		}
		if known {
			if len(reads) == 0 {
				t.Errorf("target %s is known but resolves to no port_reads", name)
			}
			for _, v := range reads {
				if !inUnion[v] {
					t.Errorf("target %s reads %s but the union does not contain it", name, v)
				}
			}
		} else if len(reads) != 0 {
			// The empty list, and nothing borrowed from a port that was
			// measured: a relation attributed to a runtime nobody read it
			// off is the 35-name default again, one layer down.
			t.Errorf("target %s is unknown but portReadsFor returned %v; unknown resolves to "+
				"the empty list, and only the --prune-init-unverified path substitutes the union",
				name, reads)
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

// The unknown value is a VALUE, not a typo escape hatch: any other string in
// port_reads is an error. A file that said "unkown" and was read as "nobody
// measured this" would be the original bug with a smaller blast radius.
func TestPortReadsRejectsAnyOtherString(t *testing.T) {
	var p portReadsList
	if err := p.UnmarshalJSON([]byte(`"unknown"`)); err != nil || p != nil {
		t.Errorf("%q must parse as the empty list: %v, %v", portReadsUnknown, p, err)
	}
	if err := p.UnmarshalJSON([]byte(`"unkown"`)); err == nil {
		t.Error("a misspelled unknown was accepted as one")
	}
	if err := p.UnmarshalJSON([]byte(`["*stinput*"]`)); err != nil || len(p) != 1 {
		t.Errorf("a declared list must still parse: %v, %v", p, err)
	}
	if err := p.UnmarshalJSON([]byte(`7`)); err == nil {
		t.Error("a number was accepted as a port_reads value")
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
	raw, err := rawBuilders()
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range builders {
		if len(b.PortReads) == 0 {
			// Inheriting is the point -- but an EMPTY list is not the same
			// thing as an absent key to a reader, and the file must not
			// contain one. effectivePortReads reads `[]` as "declares
			// nothing"; a human reading builders.json reads it as "this port
			// reads no globals", which would be a licence to prune every
			// init form. Keep the two readings from ever meeting.
			if _, present := raw[name]["port_reads"]; present {
				t.Errorf("target %s declares an EMPTY port_reads. Delete the key: "+
					"effectivePortReads treats it as no declaration and inherits %s, "+
					"so the file would say one thing and the shake do another.",
					name, builderDefaultsKey)
			}
			continue
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

// yggdrasil.shen carries the same list the Go driver would push in for a
// target-agnostic shake, for a direct host invocation with no Go driver to
// push one. builders.json is the authority; this is what catches the copy
// drifting from it.
//
// What it pins CHANGED with the representation. `_default.port_reads` is the
// string "unknown" now, so there is no default list to compare against; the
// list the Shen side must equal is portReadsFor(""), the union over the
// targets that have actually declared one. Today that is go's five. The 35
// names this used to pin were a guess, and the whole point of the change is
// that a guess must not be the value a reader inherits.
func TestPortReadsDefaultMatchesShen(t *testing.T) {
	union, _, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("yggdrasil.shen")
	if err != nil {
		t.Fatal(err)
	}
	shen := shenPortReadsDefault(t, string(src))

	want, got := union, shen
	if len(want) != len(got) {
		t.Errorf("the union of the declared port_reads lists in builders.json has %d names, "+
			"yggdrasil.shen's (set ygg.*port-reads* ...) has %d", len(want), len(got))
	}
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] != got[i] {
			t.Fatalf("the two lists diverge at position %d: builders.json's union says %q, "+
				"yggdrasil.shen says %q", i, want[i], got[i])
		}
	}
	if t.Failed() {
		inShen := map[string]bool{}
		for _, v := range got {
			inShen[v] = true
		}
		for _, v := range want {
			if !inShen[v] {
				t.Errorf("  only in builders.json: %s", v)
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
	// Bound the scan at the function's closing brace. gofmt puts it at column
	// zero, so the first "\n}" after the header ends the body. Scanning to EOF
	// happened to be correct only because InstallKernelFast is the last
	// function in the file; a helper appended after it that called
	// overridePrimitive would have been counted as a kernel rebinding.
	body := src[i:]
	if j := strings.Index(body, "\n}"); j >= 0 {
		body = body[:j+2]
	} else {
		t.Skipf("%s has no closing brace at column zero; kl/kernelfast.go is not gofmt'd "+
			"and this parser cannot tell the function's body from the rest of the file", fn)
	}
	out := map[string]bool{}
	for _, m := range kernelFastRebindRe.FindAllStringSubmatch(body, -1) {
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
		for _, r := range contractRows(raw[target], raw[builderDefaultsKey], builders[target], defaults) {
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

	// A target that inherits the default inherits UNKNOWN, and the row has
	// to say so out loud rather than printing a list it did not measure. It
	// must also still say WHERE that unknown came from -- the source is the
	// reason nobody measured it, which is the useful half.
	luaRows := rows("lua")
	pr := luaRows["port_reads"]
	if pr.status != "unknown" {
		t.Errorf("lua declares no port_reads and %s says %q; the report says %q with summary %q",
			builderDefaultsKey, portReadsUnknown, pr.status, pr.summary)
	}
	if !pr.inherited {
		t.Errorf("lua inherits port_reads from %s; the report must say so", builderDefaultsKey)
	}
	if !strings.Contains(pr.summary, portReadsUnknown) {
		t.Errorf("lua's port_reads summary does not contain %q: %q", portReadsUnknown, pr.summary)
	}
	if !factSourced(pr.source) {
		t.Errorf("lua's unknown port_reads row prints no source; the reason a fact was never "+
			"measured is what a reader needs. source=%q", pr.source)
	}
	var refusal bool
	for _, n := range pr.notes {
		if strings.Contains(n, "--prune-init") {
			refusal = true
		}
	}
	if !refusal {
		t.Errorf("the unknown row does not say what unknown COSTS (--prune-init refuses it): %v", pr.notes)
	}
	if factChecked(defaults.PortReadsCheckedBy) {
		t.Errorf("builders.json %s.port_reads claims a checked_by (%q); nothing checks an "+
			"unknown against any runtime", builderDefaultsKey, defaults.PortReadsCheckedBy)
	}
}

// The declared-empty case, on both readers at once. This is the case the file
// does not contain (TestOnlyDeclaredPortReadsDifferFromTheDefault keeps it
// out) and the one where the two implementations of the inheritance rule used
// to disagree: the report said "declared, 0 entries" while portReadsFor
// returned the 35-name default, i.e. the report built to make the data
// trustworthy stated the opposite of what the shaker would do. Synthetic
// inputs, because the point is precisely that builders.json has no such target
// and never should.
//
// `[]` still means "declares nothing" on both sides, and what it now inherits
// is UNKNOWN rather than a list. That is the safe direction and the only one:
// reading `[]` as "this port reads no globals" would be a licence to prune
// every init form, from a key somebody could type by accident.
func TestDeclaredEmptyPortReadsInheritsInBothReaders(t *testing.T) {
	_, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.PortReads) != 0 {
		t.Fatalf("builders.json %s.port_reads is a list again; this test is about what an "+
			"empty declaration inherits", builderDefaultsKey)
	}
	empty := builder{PortReads: portReadsList{}}

	// Reader 1: the shaker.
	eff, known := effectivePortReads(empty, defaults)
	if known || len(eff) != 0 {
		t.Fatalf("effectivePortReads on a declared-empty target returned (%v, %v), want the "+
			"unknown the default carries", eff, known)
	}

	// Reader 2: the contract report, on a raw block that states the key as an
	// empty list.
	block := map[string]json.RawMessage{
		"port_reads":        json.RawMessage(`[]`),
		"port_reads_source": json.RawMessage(`"a port that measured nothing"`),
	}
	raw, err := rawBuilders()
	if err != nil {
		t.Fatal(err)
	}
	r := contractFactRow("port_reads", block, raw[builderDefaultsKey], empty, defaults)
	if !r.inherited {
		t.Errorf("the contract report calls a declared-empty port_reads a declaration of its "+
			"own (%q); effectivePortReads inherits %s, and the report must say what the "+
			"shaker will do", r.summary, builderDefaultsKey)
	}
	if r.status != "unknown" {
		t.Errorf("a declared-empty port_reads reports %q; both readers resolve it to unknown", r.status)
	}
	if !strings.Contains(r.summary, portReadsUnknown) {
		t.Errorf("the report summarises a declared-empty port_reads as %q", r.summary)
	}
	var noted bool
	for _, n := range r.notes {
		if strings.Contains(n, "empty list") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the report inherits over a declared-empty list without saying it did; "+
			"the reader sees `\"port_reads\": []` in the file and an unknown row here. notes=%v",
			r.notes)
	}

	// And a target that DOES declare a list is still its own, on both sides.
	own := builder{PortReads: portReadsList{"*stinput*"}}
	if got, known := effectivePortReads(own, defaults); !known || len(got) != 1 {
		t.Errorf("effectivePortReads overrode a declared one-name list: %v %v", got, known)
	}
	ownBlock := map[string]json.RawMessage{
		"port_reads":        json.RawMessage(`["*stinput*"]`),
		"port_reads_source": json.RawMessage(`"measured"`),
	}
	if r := contractFactRow("port_reads", ownBlock, raw[builderDefaultsKey], own, defaults); r.inherited {
		t.Errorf("the contract report inherited over a target's own declared list: %q", r.summary)
	}
}

// The parser that produces the `verified` in `native_overrides verified` must
// read InstallKernelFast's body and nothing else. It used to scan from the
// function header to END OF FILE, which was correct only by the accident that
// InstallKernelFast is the last function in kl/kernelfast.go: a helper
// appended after it that called overridePrimitive would have been counted as a
// kernel rebinding, and the fact would have gone on reading `verified` while
// the test compared against a list that is not the one the kernel installs.
// shen-go is read-only, so the reintroduction is caught here on synthetic
// source rather than by waiting for that file to grow a helper.
func TestKernelFastRebindingsStopsAtTheClosingBrace(t *testing.T) {
	const src = `package kl

func InstallKernelFast() {
	overridePrimitive("inside-one", nil)
	BindSymbolFunc(MakeSymbol("inside-two"), nil)
}

func someHelperAddedLater() {
	overridePrimitive("outside", nil)
	overrideNative("also-outside", nil)
}
`
	got := kernelFastRebindings(t, src)
	want := map[string]bool{"inside-one": true, "inside-two": true}
	for n := range want {
		if !got[n] {
			t.Errorf("kernelFastRebindings missed %q, which is inside InstallKernelFast", n)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("kernelFastRebindings returned %q, which is in a function AFTER "+
				"InstallKernelFast. The scan is unbounded again, so native_overrides is "+
				"being compared against rebindings the kernel install does not perform.", n)
		}
	}
}

// The legend is a claim like any other row, and it was the last place the old
// one-word-over-two-predicates problem survived: it read "verified = a named
// test fails when the fact drifts", which asserts a DIRECTION that one of the
// two checked_by strings does not have. go's port_reads is checked by building
// the pruned slice -- adding a name it does not read can break that build, but
// removing one from a list that is already a superset only prunes less and
// nothing fails. So the legend may not promise a direction; it must send the
// reader to the string that states one, and the strings must state it.
func TestContractLegendPromisesNoMoreThanCheckedBySays(t *testing.T) {
	legend := strings.ToLower(strings.Join(contractLegend, " "))
	for _, banned := range []string{"fails when the fact drifts", "always", "guarantees"} {
		if strings.Contains(legend, banned) {
			t.Errorf("the contract legend claims %q. The two checked_by strings in "+
				"builders.json do not have the same strength, so the legend may only "+
				"point at them: %v", banned, contractLegend)
		}
	}
	if !strings.Contains(legend, "checked_by") || !strings.Contains(legend, "direction") {
		t.Errorf("the legend must name checked_by and tell the reader to read it for the "+
			"direction of the check; got %v", contractLegend)
	}

	builders, _, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	// Every fact whose row will read `verified` must disclose the direction in
	// its own checked_by, since the legend no longer does it for them.
	checks := map[string]string{
		"go.port_reads":       builders["go"].PortReadsCheckedBy,
		"go.native_overrides": builders["go"].NativeOverridesCheckedBy,
	}
	for name, s := range checks {
		if !factChecked(s) {
			t.Errorf("%s has no checked_by, so its row cannot read verified", name)
			continue
		}
		low := strings.ToLower(s)
		if !strings.Contains(low, "direction") && !strings.Contains(low, "both ways") &&
			!strings.Contains(low, "removed") {
			t.Errorf("%s's checked_by does not say in which direction the named test "+
				"catches drift, and the legend no longer says it for them: %q", name, s)
		}
	}
}

// ---- the mode is an argument, and an unverified list is refused ----
//
// These four need no host: they are about the expression the Go side builds,
// which is the whole of what --prune-init, --target and --trace mean here.

// The default shake's host expression is untouched -- the property every
// other stage-4 promise rests on. Asserted on the string, not on a golden
// artifact, so a regression names itself.
func TestShakeExprDefaultIsByteIdentical(t *testing.T) {
	const want = `(yggdrasil.shake ["/p/prog.shen"] "/p/out")`
	got, err := shakeExpr("/p/prog.shen", "/p/out", shakeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("default shakeExpr = %q, want %q", got, want)
	}
	wrapped, err := wrapShakeExpr(got, shakeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != want {
		t.Errorf("default wrapShakeExpr rewrote the expression:\n  got  %q\n  want %q", wrapped, want)
	}
	// And each mode picks its own entry point off the options alone.
	for _, c := range []struct {
		o  shakeOpts
		fn string
	}{
		{shakeOpts{}, "yggdrasil.shake"},
		{shakeOpts{full: true}, "yggdrasil.shake-full"},
		{shakeOpts{trace: true}, "yggdrasil.shake-traced"},
	} {
		got, err := shakeExpr("/p/prog.shen", "/p/out", c.o)
		if err != nil {
			t.Fatalf("shakeExpr(%+v): %v", c.o, err)
		}
		if !strings.HasPrefix(got, "("+c.fn+" ") {
			t.Errorf("shakeExpr(%+v) = %q, want the %s entry point", c.o, got, c.fn)
		}
	}
	if _, err := shakeExpr("/p/prog.shen", "/p/out", shakeOpts{full: true, trace: true}); err == nil {
		t.Error("--trace with --no-shake must still be refused")
	}
}

// torvalds-11: --prune-init against a target whose port_reads list is a
// placeholder used to prune silently. It must now refuse, by name.
//
// hickey-14 sharpened what the refusal SAYS. There is no placeholder any more:
// a target that declares nothing resolves to unknown, and the message has to
// say that nobody measured this port rather than that its list is unverified,
// because those are different repairs (measure it, versus write a test for the
// list you have).
func TestPruneInitRefusesUnverifiedTarget(t *testing.T) {
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	var unverified string
	for name := range builders {
		// T4 replaced the port_reads_verified boolean with
		// port_reads_checked_by; portReadsVerified is the one place that
		// word is defined now, and it is what wrapShakeExpr consults, so
		// the test asks the same question the refusal does.
		if !portReadsVerified(name) {
			unverified = name
			break
		}
	}
	if unverified == "" {
		t.Skip("every target's port_reads is verified; nothing to refuse")
	}
	const expr = `(yggdrasil.shake ["/p/prog.shen"] "/p/out")`
	// Held in its own variable: the assertions below reach back into this
	// refusal's text after other calls have returned their own errors.
	_, refusal := wrapShakeExpr(expr, shakeOpts{pruneInit: true, target: unverified})
	if refusal == nil {
		t.Fatalf("--prune-init --target %s was accepted; nothing has measured its port_reads", unverified)
	}
	if !strings.Contains(refusal.Error(), unverified) {
		t.Errorf("the refusal must name the target; got %q", refusal)
	}
	for _, want := range []string{"port_reads", "--prune-init-unverified"} {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("the refusal must mention %q; got %q", want, refusal)
		}
	}
	if _, known, _ := portReadsFor(unverified); !known && !strings.Contains(refusal.Error(), portReadsUnknown) {
		t.Errorf("%s resolves to unknown and the refusal does not say the word: %q", unverified, refusal)
	}

	// The escape hatch proceeds, and still installs a list -- the union over
	// the targets that HAVE declared one, since this target has none.
	got, err := wrapShakeExpr(expr, shakeOpts{pruneInit: true, target: unverified, allowUnverifiedPortReads: true})
	if err != nil {
		t.Fatalf("--prune-init-unverified must proceed: %v", err)
	}
	if !strings.Contains(got, "(set ygg.*prune-init* true)") {
		t.Errorf("the escape hatch must still ask for pruning:\n%s", got)
	}
	// The union over the DECLARED lists, which is what the WARN above says
	// it prunes against -- portReadsFor(unverified) is the empty list, and
	// pruning against nothing would drop every init form.
	union, _, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(set ygg.*port-reads* ["+strings.Join(union, " ")+"])") {
		t.Errorf("the escape hatch installed a list that is neither the union it announced "+
			"nor anything else recognisable:\n%s", got)
	}
	if reads, _, _ := portReadsFor(unverified); len(reads) != 0 {
		t.Errorf("portReadsFor(%s) returned %v; an unknown target resolves to nothing",
			unverified, reads)
	}

	// A verified target is never refused. The target-agnostic union no longer
	// is the way out -- see the next test.
	if _, err := wrapShakeExpr(expr, shakeOpts{pruneInit: true, target: "go"}); err != nil {
		t.Errorf("--prune-init --target go must be accepted: %v", err)
	}
	// And the refusal must not send the reader down a path that is itself
	// refused, which "shake without --target" now is: a message whose first
	// suggestion produces a second refusal is how a user concludes the flag
	// is broken rather than that the data is missing.
	if _, noTarget := wrapShakeExpr(expr, shakeOpts{pruneInit: true}); noTarget != nil {
		if strings.Contains(refusal.Error(), "Shake without --target (") {
			t.Errorf("the refusal recommends shaking without --target, which is itself refused:\n%v", refusal)
		}
		if !strings.Contains(refusal.Error(), "--target go") {
			t.Errorf("the refusal names no accepted way through; got %q", refusal)
		}
	}
}

// What a target-agnostic --prune-init prunes against, what it says about it,
// and why it is REFUSED by default.
//
// The union used to be over every builder's EFFECTIVE list, which included the
// 35-name guess, so "shake without --target: the union over every builder is
// sound for any of them" was true of the guess and of nothing else. It is now
// over the DECLARED lists only -- honest, and SMALLER, and a smaller list
// prunes MORE. A shake with no --target is by definition a slice that may be
// built for a port nobody measured, so the union is sound for exactly the
// ports it is over and the flag must be asked for: this is the same refusal a
// named unknown target gets, one level up.
func TestTargetAgnosticPruneNamesWhatTheUnionIsOver(t *testing.T) {
	declared, unknown, err := portReadsCoverage()
	if err != nil {
		t.Fatal(err)
	}
	if len(declared) == 0 {
		t.Fatal("no target declares a port_reads list; the union is empty")
	}
	union, known, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	if known != (len(unknown) == 0) {
		t.Errorf("the union reports known=%v with %d unmeasured targets", known, len(unknown))
	}
	// Every name in the union comes from a target that declared it: an
	// inherited guess reaching the union is the bug.
	builders, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	fromDeclared := map[string]bool{}
	for _, name := range declared {
		reads, _ := effectivePortReads(builders[name], defaults)
		for _, v := range reads {
			fromDeclared[v] = true
		}
	}
	for _, v := range union {
		if !fromDeclared[v] {
			t.Errorf("the union contains %s, which no target declared", v)
		}
	}

	const expr = `(yggdrasil.shake ["/p/prog.shen"] "/p/out")`
	if len(unknown) == 0 {
		t.Skip("every target declares a port_reads list; there is nothing left to refuse")
	}

	// Refused by default, and the refusal says both halves out loud.
	_, err = wrapShakeExpr(expr, shakeOpts{pruneInit: true})
	if err == nil {
		t.Fatalf("--prune-init with no --target was accepted, though %d target(s) have declared "+
			"no port_reads and a no-target slice may be built for one of them", len(unknown))
	}
	for _, want := range append(append([]string{"--prune-init-unverified"}, declared...), unknown...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}

	// The flag proceeds, installs the union, and says the same two halves as
	// a WARN. Captured off stderr, because a message nobody can read is the
	// same as no message.
	var got string
	stderr := captureStderr(t, func() {
		var err error
		got, err = wrapShakeExpr(expr, shakeOpts{pruneInit: true, allowUnverifiedPortReads: true})
		if err != nil {
			t.Fatalf("--prune-init-unverified with no --target must proceed: %v", err)
		}
	})
	if !strings.Contains(got, "(set ygg.*prune-init* true)") {
		t.Errorf("the escape hatch must still ask for pruning:\n%s", got)
	}
	if !strings.Contains(got, "(set ygg.*port-reads* ["+strings.Join(union, " ")+"])") {
		t.Errorf("the escape hatch installed a different list than the union:\n%s", got)
	}
	for _, want := range append(append([]string{"WARN", "UNION"}, declared...), unknown...) {
		if !strings.Contains(stderr, want) {
			t.Errorf("the target-agnostic --prune-init WARN does not mention %q:\n%s", want, stderr)
		}
	}
}

// `facts` and `build --target T` without --prune-init are untouched by any of
// this: they install a list and prune nothing, so there is nothing to refuse.
// A refusal that leaked into them would break `yggdrasil facts` on twelve
// targets for a flag those invocations never passed.
func TestPruneRefusalIsOnlyOnPruning(t *testing.T) {
	const expr = `(yggdrasil.facts ["/p/prog.shen"] "/p/out")`
	if _, err := wrapShakeExpr(expr, shakeOpts{}); err != nil {
		t.Errorf("the default shake was refused: %v", err)
	}
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	for name := range builders {
		if _, err := wrapShakeExpr(expr, shakeOpts{target: name}); err != nil {
			t.Errorf("facts --target %s prunes nothing and must not be refused: %v", name, err)
		}
	}
}

// hickey-14, the second reader. `yggdrasil facts --target lua` installs a
// port_reads list as the shake's portReads EDB and prunes nothing. While
// portReadsFor handed out the union for an unknown target, that dumped go's
// five globals into portReads.facts under lua's name: a measured port's
// relation, silently attributed to a port nobody has measured, in a file whose
// whole purpose is to be read as fact.
//
// Unknown now installs the EMPTY relation and says so on stderr, once. Empty
// is the honest EDB -- it is not this port's reads, and it is not another
// port's either -- and it agrees with what `yggdrasil contract --target lua`
// prints, which is the property the two readers have to share.
func TestFactsOnUnknownPortReadsInstallsAnEmptyRelation(t *testing.T) {
	const expr = `(yggdrasil.facts ["/p/prog.shen"] "/p/out")`
	builders, defaults, err := parseBuilders()
	if err != nil {
		t.Fatal(err)
	}
	var unknownTarget string
	for name, b := range builders {
		if _, known := effectivePortReads(b, defaults); !known {
			unknownTarget = name
			break
		}
	}
	if unknownTarget == "" {
		t.Skip("every target declares a port_reads list")
	}

	var got string
	stderr := captureStderr(t, func() {
		var err error
		got, err = wrapShakeExpr(expr, shakeOpts{target: unknownTarget})
		if err != nil {
			t.Fatalf("facts --target %s must not be refused: %v", unknownTarget, err)
		}
	})
	if !strings.Contains(got, "(set ygg.*port-reads* [])") {
		t.Errorf("facts --target %s installs a non-empty portReads relation:\n%s", unknownTarget, got)
	}
	union, _, err := portReadsFor("")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range union {
		if strings.Contains(got, v) {
			t.Errorf("facts --target %s installs %s, which was read off another port's runtime:\n%s",
				unknownTarget, v, got)
		}
	}
	for _, want := range []string{"WARN", portReadsUnknown, unknownTarget, "EMPTY"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the WARN does not mention %q:\n%s", want, stderr)
		}
	}
	if n := strings.Count(stderr, "yggdrasil: WARN"); n != 1 {
		t.Errorf("want exactly one WARN line, got %d:\n%s", n, stderr)
	}

	// The contract report says the same word about the same target.
	raw, err := rawBuilders()
	if err != nil {
		t.Fatal(err)
	}
	r := contractFactRow("port_reads", raw[unknownTarget], raw[builderDefaultsKey],
		builders[unknownTarget], defaults)
	if r.status != portReadsUnknown {
		t.Errorf("facts installs an empty relation for %s and the contract report says %q",
			unknownTarget, r.status)
	}

	// A target that HAS declared one is untouched: its own list, no WARN.
	declared, _, err := portReadsCoverage()
	if err != nil || len(declared) == 0 {
		t.Fatalf("no declared target to check the other half against: %v", err)
	}
	stderr = captureStderr(t, func() {
		var err error
		got, err = wrapShakeExpr(expr, shakeOpts{target: declared[0]})
		if err != nil {
			t.Fatal(err)
		}
	})
	reads, _, err := portReadsFor(declared[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(set ygg.*port-reads* ["+strings.Join(reads, " ")+"])") {
		t.Errorf("facts --target %s does not install its own declared list:\n%s", declared[0], got)
	}
	if stderr != "" {
		t.Errorf("a measured target warned about nothing: %s", stderr)
	}
}

// Every `_checked_by` in builders.json that is not "none" must name something
// that EXISTS: a Test function in a *_test.go here, or a file in the repo.
//
// This is the flag's only defence. `verified` is the strongest word the
// contract report prints, and it is earned by a string in a data file -- so a
// string naming a test nobody wrote, or a test somebody later renamed, inflates
// every row that cites it and nothing notices. Two of the values in this file
// said `scripts/parity-gate.sh` for a fact the gate checks on three targets in
// CI and skips entirely where a toolchain is absent; they now say "none" and
// explain why, which is what this test is here to keep true of the rest.
func TestCheckedByNamesSomethingThatExists(t *testing.T) {
	raw, err := rawBuilders()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`func (Test[A-Za-z0-9_]+)\(`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range fn.FindAllStringSubmatch(string(b), -1) {
			tests[m[1]] = true
		}
	}
	if len(tests) == 0 {
		t.Fatal("no Test functions found; this test would pass vacuously")
	}
	named := regexp.MustCompile(`Test[A-Za-z0-9_]+`)
	path := regexp.MustCompile(`(scripts|analysis|builders|docs)/[A-Za-z0-9_./-]+`)

	checked := 0
	for target, block := range raw {
		for key, v := range block {
			if !strings.HasSuffix(key, "_checked_by") {
				continue
			}
			val := jsonString(v)
			if !factChecked(val) {
				continue
			}
			checked++
			hits := 0
			for _, name := range named.FindAllString(val, -1) {
				hits++
				if !tests[name] {
					t.Errorf("builders.json %s.%s names %s, which no *_test.go defines. "+
						"A checked_by that names nothing is how a row reads `verified` "+
						"with nothing behind it", target, key, name)
				}
			}
			for _, rel := range path.FindAllString(val, -1) {
				hits++
				if _, err := os.Stat(rel); err != nil {
					t.Errorf("builders.json %s.%s names %s, which does not exist", target, key, rel)
				}
			}
			if hits == 0 {
				t.Errorf("builders.json %s.%s is %q: it claims something checks the fact but "+
					"names no Test function and no file. Say \"none: <why>\" instead",
					target, key, val)
			}
		}
	}
	if checked == 0 {
		t.Error("no fact in builders.json claims a checked_by; this test would never catch one")
	}
	t.Logf("%d checked_by values name a test or a file", checked)
}

// captureStderr runs fn with os.Stderr replaced by a pipe and returns what was
// written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stderr = saved
	out := <-done
	r.Close()
	return out
}

// hickey-9 / torvalds-9: `facts --target T` selects a port_reads list. It used
// to have to say pruneInit to do so, because a global was the only channel.
func TestFactsTargetSelectsListWithoutPruning(t *testing.T) {
	const expr = `(yggdrasil.facts ["/p/prog.shen"] "/p/out")`
	got, err := wrapShakeExpr(expr, shakeOpts{target: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(set ygg.*prune-init* false)") {
		t.Errorf("a target without --prune-init must not ask the shaker to prune:\n%s", got)
	}
	reads, _, err := portReadsFor("go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(set ygg.*port-reads* ["+strings.Join(reads, " ")+"])") {
		t.Errorf("go's port_reads list is not installed:\n%s", got)
	}
	// An unverified target is fine here: nothing is being pruned.
	if _, err := wrapShakeExpr(expr, shakeOpts{target: "lua"}); err != nil {
		t.Errorf("facts --target lua must not be refused (it prunes nothing): %v", err)
	}
}

// shake()/facts() take AT MOST one shakeOpts. The variadic is a default
// argument; two would silently mean one of them was ignored.
func TestShakeOptsVariadicTakesOne(t *testing.T) {
	if _, err := only(nil); err != nil {
		t.Errorf("no options must mean the zero value: %v", err)
	}
	o, err := only([]shakeOpts{{trace: true}})
	if err != nil || !o.trace {
		t.Errorf("one option must pass through: %+v %v", o, err)
	}
	if _, err := only([]shakeOpts{{}, {}}); err == nil {
		t.Error("two shakeOpts must be an error, not a silent choice")
	}
}
