package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWrapExecutableFor(t *testing.T) {
	cases := []struct {
		argv []string
		win  bool
		want []string
	}{
		{[]string{`C:\p\shen.cmd`, "x"}, true, []string{"cmd", "/c", `C:\p\shen.cmd`, "x"}},
		{[]string{"builders/lisp/build.sh", "a"}, true, []string{"sh", "builders/lisp/build.sh", "a"}},
		{[]string{`C:\p\app.exe`}, true, []string{`C:\p\app.exe`}},
		{[]string{"/x/app"}, false, []string{"/x/app"}},
	}
	for _, c := range cases {
		if got := wrapExecutableFor(c.argv, c.win); !reflect.DeepEqual(got, c.want) {
			t.Errorf("wrapExecutableFor(%v, %v) = %v, want %v", c.argv, c.win, got, c.want)
		}
	}
}

func TestFindExecutableFor(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "shen.exe"), []byte("MZ"), 0o644)
	base := filepath.Join(dir, "shen")
	if got := findExecutableFor(base, true, []string{".exe"}); got != base+".exe" {
		t.Errorf("windows ext = %q", got)
	}
	if got := findExecutableFor(base, false, []string{".exe"}); got != "" {
		t.Errorf("posix must not invent .exe: %q", got)
	}
}

func TestReorderArgs(t *testing.T) {
	// flags after positionals get pulled forward; value-flag values stay attached
	got := reorderArgs([]string{"prog.shen", "out", "--target", "js", "--run"}, "target")
	want := []string{"--target", "js", "--run", "prog.shen", "out"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %v, want %v", got, want)
	}
}

func TestReorderArgsWebBoolFlag(t *testing.T) {
	// --web is a bool flag (not in valueFlags): it must be pulled forward WITHOUT
	// swallowing the following positional, so PROG/OUTDIR survive intact.
	got := reorderArgs([]string{"prog.shen", "out", "--target", "js", "--web"}, "host", "eval-style", "target")
	want := []string{"--target", "js", "--web", "prog.shen", "out"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs(--web) = %v, want %v", got, want)
	}
}

// --web on an eval-capable program has no valid resolution (--linked is
// mutually exclusive), so the preflight must fail early and say why.
func TestWebPreflight(t *testing.T) {
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// eval-free: preflight passes.
	ok := t.TempDir()
	write(ok, "yggdrasil.manifest.txt", "needs-eval=false\ncannot-reach=eval\n")
	write(ok, "b.kl", "(defun add2 (V1) (+ V1 2))\n")
	if err := webPreflight(ok); err != nil {
		t.Errorf("eval-free program must pass preflight, got: %v", err)
	}

	// eval-capable: preflight fails, names the culprit, and does NOT repeat
	// the stage-2 builder's impossible "--linked" advice as the remedy.
	bad := t.TempDir()
	write(bad, "yggdrasil.manifest.txt", "needs-eval=true\nreaches=eval\n")
	write(bad, "p.kl", "(tc +)\n\n(defun p (V1) (eval V1))\n")
	write(bad, "kernel.kl", "(defun shen.eval-without-macros (V1) (eval-kl V1))\n")
	err := webPreflight(bad)
	if err == nil {
		t.Fatal("needs-eval=true must fail the --web preflight")
	}
	for _, want := range []string{"needs-eval=true", "mutually exclusive", "eval", "tc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("preflight message missing %q:\n%s", want, err)
		}
	}
	// kernel.kl is not user code: its eval-kl must not be blamed on the author.
	if strings.Contains(err.Error(), "eval-kl") {
		t.Errorf("preflight blamed kernel.kl:\n%s", err)
	}

	// No manifest at all: stay quiet and let the stage-2 builder report.
	if err := webPreflight(t.TempDir()); err != nil {
		t.Errorf("missing manifest must not fail preflight, got: %v", err)
	}
}

func TestLoadBuildersEmbedded(t *testing.T) {
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lisp", "lua", "go", "rust", "js", "erlang", "c", "truffle", "truffle-native"} {
		if _, ok := b[want]; !ok {
			t.Errorf("missing target %q", want)
		}
	}
}

func TestCBuilderRecipe(t *testing.T) {
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	bld := b["c"]
	if bld.RunImpl != "shen-c" || bld.DirEnv != "YGGDRASIL_SHEN_C_DIR" {
		t.Fatalf("unexpected c builder: %#v", bld)
	}
	joinedNeeds := strings.Join(bld.Needs, " ")
	if !strings.Contains(joinedNeeds, "cc") || !(strings.Contains(joinedNeeds, "make") || strings.Contains(joinedNeeds, "cmake")) {
		t.Fatalf("c needs = %v (want cc and make or cmake)", bld.Needs)
	}
	if len(bld.Build) != 1 || !strings.Contains(strings.Join(bld.Build[0].Argv, " "), "builders/c/build.sh") {
		t.Fatalf("unexpected c build steps: %#v", bld.Build)
	}
	if env := bld.Build[0].Env; env["SHEN_C"] != "{shen_c}" {
		t.Fatalf("c build env SHEN_C = %v", env)
	}
	if got := strings.Join(bld.Run, " "); got != "{outdir}/app-c/app" {
		t.Fatalf("c run recipe = %q (must run the linked binary, not shen-c script)", got)
	}
	for _, a := range bld.Build[0].Argv {
		if strings.Contains(a, "script") || strings.Contains(a, "app.shen") {
			t.Fatalf("c builder still looks like fake shen-c script wrap: %v", bld.Build[0].Argv)
		}
	}
}

func TestErlangBuilderRecipe(t *testing.T) {
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	bld := b["erlang"]
	if bld.RunImpl != "shen-erl" || bld.DirEnv != "YGGDRASIL_SHEN_ERL_DIR" {
		t.Fatalf("unexpected erlang builder: %#v", bld)
	}
	if len(bld.Build) != 1 || !strings.Contains(strings.Join(bld.Build[0].Argv, " "), "builders/erlang/build.sh") {
		t.Fatalf("unexpected erlang build steps: %#v", bld.Build)
	}
	if got := strings.Join(bld.Run, " "); got != "{outdir}/app-erlang/run" {
		t.Fatalf("erlang run recipe = %q", got)
	}
}

func TestTruffleBuilderRecipes(t *testing.T) {
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, format, output, run0 string
	}{
		{"truffle", "jvm", "{outdir}/app-truffle", "{outdir}/app-truffle/bin/shen-truffle"},
		{"truffle-native", "native", "{outdir}/app-truffle-native", "{outdir}/app-truffle-native"},
	} {
		bld, ok := b[tc.name]
		if !ok || len(bld.Build) != 2 {
			t.Fatalf("%s: expected Maven packaging and builder steps", tc.name)
		}
		argv := bld.Build[len(bld.Build)-1].Argv
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "--format "+tc.format) || !strings.Contains(joined, tc.output) || !strings.Contains(joined, "--runtime {shen_truffle}/target/shen-truffle.jar") {
			t.Errorf("%s: recipe = %v", tc.name, argv)
		}
		if len(bld.Run) == 0 || bld.Run[0] != tc.run0 {
			t.Errorf("%s: run = %v, want prefix %q", tc.name, bld.Run, tc.run0)
		}
	}
}

func TestReorderArgsTypecheckBoolFlag(t *testing.T) {
	// --typecheck is a bool flag (not in valueFlags): it must be pulled
	// forward WITHOUT swallowing the following positional.
	got := reorderArgs([]string{"prog.shen", "out", "--typecheck", "--target", "lua"}, "host", "eval-style", "target")
	want := []string{"--typecheck", "--target", "lua", "prog.shen", "out"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs(--typecheck) = %v, want %v", got, want)
	}
}

func TestParseCheckOK(t *testing.T) {
	ver, ok := parseCheckOK("boot chatter\nyggdrasil-check: OK files=1 inferences=42 version=S41.2\ntrailer\n")
	if !ok || ver != "S41.2" {
		t.Errorf("parseCheckOK OK line = (%q, %v), want (\"S41.2\", true)", ver, ok)
	}
	// FAIL sentinel is not OK.
	if _, ok := parseCheckOK("yggdrasil-check: FAIL file=x.shen form=2 name=bad\n  type error\n"); ok {
		t.Error("FAIL sentinel parsed as OK")
	}
	// Garbage (host crash, no sentinel) is not OK.
	if _, ok := parseCheckOK("Segmentation fault\n"); ok {
		t.Error("garbage output parsed as OK")
	}
	// OK without a version field still passes, with empty version.
	ver, ok = parseCheckOK("yggdrasil-check: OK files=1\n")
	if !ok || ver != "" {
		t.Errorf("versionless OK = (%q, %v), want (\"\", true)", ver, ok)
	}
}

func TestAppendTypecheckManifest(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "yggdrasil.manifest.txt")
	sexp := filepath.Join(dir, "yggdrasil.manifest")
	if err := os.WriteFile(txt, []byte("manifest-version=3\nneeds-eval=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sexp, []byte("(\"yggdrasil-manifest\" 3)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendTypecheckManifest(dir, []string{"node", "/p/shen.js"}, "S41.2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(txt)
	for _, want := range []string{"needs-eval=false", "typechecked=true", "typecheck-host=node /p/shen.js", "typecheck-kernel=S41.2"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("manifest.txt missing %q:\n%s", want, b)
		}
	}
	s, _ := os.ReadFile(sexp)
	if !strings.Contains(string(s), "(\"typechecked\" true)") {
		t.Errorf("sexp manifest missing typechecked line:\n%s", s)
	}
	// Appending to a missing manifest is an error, not a silent create: the
	// gate only records into a manifest a shake already wrote.
	if err := appendTypecheckManifest(t.TempDir(), nil, "x"); err == nil {
		t.Error("append into an empty outdir must fail")
	}
}

// TestManifestPairs covers the reader that step.When and webPreflight share.
// A nil return means "no manifest", which callers must not confuse with "the
// manifest said nothing" -- one is unreadable, the other is a real answer.
func TestManifestPairs(t *testing.T) {
	dir := t.TempDir()
	if got := manifestPairs(dir); got != nil {
		t.Errorf("no manifest should give nil, got %v", got)
	}
	body := "manifest-version=3\nneeds-eval=false\n\nfn=member 2\nfn=flatten 1\n"
	if err := os.WriteFile(filepath.Join(dir, "yggdrasil.manifest.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p := manifestPairs(dir)
	for _, want := range []string{"needs-eval=false", "fn=member 2", "fn=flatten 1"} {
		if !p[want] {
			t.Errorf("missing pair %q", want)
		}
	}
	// Repeated keys are kept whole, not collapsed to a last-wins map.
	if p["needs-eval=true"] {
		t.Error("needs-eval=true must not be present")
	}
	if len(p) != 4 {
		t.Errorf("blank lines should be dropped; got %d pairs: %v", len(p), p)
	}
}

// TestJSBuilderSelectsLinkedOnEval pins the fix for #23.  ShenScript's default
// mode is self-contained but refuses needs-eval=true; --linked can carry the
// compiler but imports from the ShenScript checkout.  Exactly one step must
// apply to any given shake, and --linked must attach to the eval-capable one
// and ONLY to it -- passing it unconditionally would quietly de-portabilise
// every eval-free js artifact, which is the common case.
func TestJSBuilderSelectsLinkedOnEval(t *testing.T) {
	builders, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	js, ok := builders["js"]
	if !ok {
		t.Fatal("no js target")
	}
	byWhen := map[string]step{}
	for _, st := range js.Build {
		if st.When == "" {
			t.Fatalf("every js build step must be conditional, found unconditional %v", st.Argv)
		}
		if _, dup := byWhen[st.When]; dup {
			t.Fatalf("two js steps share when=%q", st.When)
		}
		byWhen[st.When] = st
	}
	free, hasFree := byWhen["needs-eval=false"]
	eval, hasEval := byWhen["needs-eval=true"]
	if !hasFree || !hasEval {
		t.Fatalf("js needs one step per needs-eval value, have %v", byWhen)
	}
	hasLinked := func(st step) bool {
		for _, a := range st.Argv {
			if a == "--linked" {
				return true
			}
		}
		return false
	}
	if !hasLinked(eval) {
		t.Errorf("needs-eval=true step must pass --linked: %v", eval.Argv)
	}
	if hasLinked(free) {
		t.Errorf("needs-eval=false step must NOT pass --linked (it would import "+
			"from the ShenScript checkout): %v", free.Argv)
	}
}

// TestBuildConditionalStepWithoutManifest: a builder with conditional steps and
// no readable manifest must error, not run zero steps.  Skipping silently would
// exit 0 having produced no artifact -- a build that reports success and builds
// nothing.
func TestBuildConditionalStepWithoutManifest(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("js target needs node on PATH")
	}
	_, err := build("js", t.TempDir(), false)
	if err == nil {
		t.Fatal("expected an error when the manifest is missing")
	}
	if !strings.Contains(err.Error(), "conditional build step") {
		t.Errorf("error should name the cause, got: %v", err)
	}
}

// TestEmbeddedTreeHasNoGeneratedCache pins the fix for #21.
//
// main.go embeds `KLambda` wholesale, and go:embed takes whatever is in the
// working tree -- gitignored, locally generated files included. Two derived
// call-graph caches were being swept into the binary this way, which broke two
// properties at once:
//
//   - build reproducibility: embeddedHash() covered them, so the extracted
//     root's identity (and thus what got shaken) depended on untracked files
//     that happened to exist at `go build` time;
//   - shake correctness: a cache is keyed by FILENAME alone and load-call-graph
//     validates only non-emptiness, so a stale one loads and silently
//     under-shakes rather than failing.
//
// The cache now lives at the repo root, which matches no go:embed pattern. This
// test is what keeps it there: adding `KLambda/callgraph-*.shen` back, or a new
// generated artifact under an embedded directory, fails here rather than
// silently changing what a build shakes.
func TestEmbeddedTreeHasNoGeneratedCache(t *testing.T) {
	// Every extension actually present in the tracked tree: KLambda/*.kl,
	// Primitives/**/*.lsp, builders/**/*.sh, yggdrasil.shen, builders.json,
	// PROVENANCE.md and two LICENSE files (no extension).
	allowed := map[string]bool{".kl": true, ".lsp": true, ".sh": true,
		".shen": true, ".json": true, ".md": true, "": true}
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := filepath.Base(p)
		if strings.Contains(base, "callgraph") {
			return fmt.Errorf("generated call-graph cache is embedded: %s\n"+
				"  go:embed sweeps up untracked working-tree files. Keep the cache at the\n"+
				"  repo root (see *callgraph-cache* in yggdrasil.shen), not under KLambda/.", p)
		}
		if ext := filepath.Ext(base); !allowed[ext] {
			return fmt.Errorf("unexpected file type in the embedded tree: %s\n"+
				"  if this is a build artifact it must not be embedded; if it is a real\n"+
				"  source file, add its extension to this test.", p)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}
