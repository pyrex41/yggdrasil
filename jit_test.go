package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// jitCheckout returns the sibling Shen/JIT checkout, or skips.
func jitCheckout(t *testing.T) string {
	t.Helper()
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	dir := siblingDir("jit", b["jit"])
	if _, err := os.Stat(filepath.Join(dir, "src", "port", "shen-jit.kl")); err != nil {
		t.Skip("no sibling shen-jit checkout")
	}
	return dir
}

// jitBuild shakes prog and builds it for the jit target, returning the shake
// directory and the run argv.
func jitBuild(t *testing.T, prog string, o shakeOpts) (string, []string) {
	t.Helper()
	host := checkHost(t)
	jitCheckout(t)
	dir := t.TempDir()
	if _, err := shake(prog, dir, host, "sub", true, o); err != nil {
		t.Fatal(err)
	}
	argv, err := build("jit", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if argv == nil {
		t.Skip("the jit target's tools are not on PATH")
	}
	return dir, argv
}

func compareSets(t *testing.T, what string, declared []string, derived map[string]bool) {
	t.Helper()
	inDeclared := map[string]bool{}
	for _, n := range declared {
		inDeclared[n] = true
		if !derived[n] {
			t.Errorf("builders.json's jit %s lists %q, which the sources do not", what, n)
		}
	}
	for n := range derived {
		if !inDeclared[n] {
			t.Errorf("the sources have %q, which builders.json's jit %s does not list", n, what)
		}
	}
}

func TestJitBuilderRecipe(t *testing.T) {
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	bld := b["jit"]
	if bld.RunImpl != "shen-jit" || bld.DirEnv != "YGGDRASIL_SHEN_JIT_DIR" {
		t.Fatalf("unexpected jit builder: %#v", bld)
	}
	if len(bld.Build) != 1 || !strings.HasSuffix(bld.Build[0].Argv[0], "builders/jit/build.sh") {
		t.Fatalf("unexpected jit build steps: %#v", bld.Build)
	}
	if bld.Build[0].Env["SHEN_JIT"] != "{shen_jit}" {
		t.Fatalf("jit build env = %v", bld.Build[0].Env)
	}
	if got := strings.Join(bld.Run, " "); got != "{outdir}/app-jit" {
		t.Fatalf("jit run recipe = %q", got)
	}
}

// The kernel is what yggdrasil.shen asks a host for, and what the manifest says.
func TestKernelIs432(t *testing.T) {
	init, err := os.ReadFile("KLambda/init.kl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(init), `(set *version* "43.2")`) {
		t.Error(`KLambda/init.kl does not set *version* to "43.2"`)
	}
	shaker, err := os.ReadFile("yggdrasil.shen")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`(= (version) "43.2")`, `["kernel-version" "43.2"]`, `kernel-version=43.2~%`} {
		if !strings.Contains(string(shaker), want) {
			t.Errorf("yggdrasil.shen lacks %s", want)
		}
	}
}

var (
	klDefunRe   = regexp.MustCompile(`\(defun (\S+) \(`)
	klValueRe   = regexp.MustCompile(`\(value ([^\s()]+)\)`)
	cGlobalRe   = regexp.MustCompile(`intern_cstr\(vm, "([^"]*\*[^"]*)"\)`)
	cStdGlobals = []string{"*stinput*", "*stoutput*", "*sterror*"}
)

func TestJitNativeOverridesMatchPort(t *testing.T) {
	dir := jitCheckout(t)
	port, err := os.ReadFile(filepath.Join(dir, "src", "port", "shen-jit.kl"))
	if err != nil {
		t.Fatal(err)
	}
	kernel := map[string]bool{}
	files, err := filepath.Glob("KLambda/*.kl")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range klDefunRe.FindAllStringSubmatch(string(src), -1) {
			kernel[m[1]] = true
		}
	}
	derived := map[string]bool{}
	for _, m := range klDefunRe.FindAllStringSubmatch(string(port), -1) {
		if kernel[m[1]] {
			derived[m[1]] = true
		}
	}
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	compareSets(t, "native_overrides", b["jit"].NativeOverrides, derived)
}

func TestJitPortReadsMatchSources(t *testing.T) {
	dir := jitCheckout(t)
	derived := map[string]bool{}
	for _, g := range cStdGlobals {
		derived[g] = true
	}
	for _, f := range []string{"src/port/shen-jit.kl", "src/lowerer/lower.shen"} {
		src, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range klValueRe.FindAllStringSubmatch(string(src), -1) {
			derived[m[1]] = true
		}
	}
	runtime, err := filepath.Glob(filepath.Join(dir, "src", "runtime", "*.c"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range runtime {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range cGlobalRe.FindAllStringSubmatch(string(src), -1) {
			derived[m[1]] = true
		}
	}
	declared, _, err := portReadsFor("jit")
	if err != nil {
		t.Fatal(err)
	}
	compareSets(t, "port_reads", declared, derived)
}

// Every golden the parity gate uses, on the jit target.
func TestJitFixtures(t *testing.T) {
	goldens, err := filepath.Glob("tests/*.expected")
	if err != nil {
		t.Fatal(err)
	}
	for _, golden := range goldens {
		name := strings.TrimSuffix(filepath.Base(golden), ".expected")
		// See scripts/parity-gate.sh: their slices are not parity material.
		if name == "computed-call" || name == "computed-read" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			_, argv := jitBuild(t, "tests/"+name+".shen", shakeOpts{})
			cmd := exec.Command(argv[0], argv[1:]...)
			if in, err := os.Open("tests/" + name + ".stdin"); err == nil {
				defer in.Close()
				cmd.Stdin = in
			}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if string(out) != string(want) {
				t.Fatalf("stdout = %q, want %q", out, want)
			}
		})
	}
}

func readLines(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, l := range strings.Fields(string(b)) {
		out[l] = true
	}
	return out
}

// An eval-free slice leaves the lowerer out, and is smaller for it; one that
// evaluates carries it and can compile at run time.
func TestJitEvalFreeSliceHoldsNoCompiler(t *testing.T) {
	dir, _ := jitBuild(t, "tests/fib.shen", shakeOpts{})
	work := filepath.Join(dir, "app-jit.build", "stage0")
	kept := readLines(t, filepath.Join(work, "jit-port-kept.txt"))
	for n := range kept {
		if strings.HasPrefix(n, "shen-jit.lower.") {
			t.Errorf("the eval-free slice kept the lowerer's %s", n)
		}
	}
	for _, n := range []string{"shen.dict", "shen.<-dict", "fn"} {
		if !kept[n] {
			t.Errorf("the port's %s replaces the slice's, but is not in jit-port-kept.txt", n)
		}
	}
	if kept["shen-jit.eval-kl"] {
		t.Error("the port's eval-kl is kept in an eval-free slice")
	}
	sbc, err := os.ReadFile(filepath.Join(work, "toplevel.sbc"))
	if err != nil {
		t.Fatal(err)
	}
	// The stub in its place raises.
	if !strings.Contains(string(sbc), "carries no compiler") {
		t.Error("the slice's shen-jit.eval-kl is not the stub that raises")
	}

	evalDir, evalArgv := jitBuild(t, "tests/metaeval.shen", shakeOpts{})
	kept = readLines(t, filepath.Join(evalDir, "app-jit.build", "stage0", "jit-port-kept.txt"))
	if !kept["shen-jit.eval-kl"] || !kept["shen-jit.lower.defun"] {
		t.Error("the eval-capable slice lost the port's eval-kl or the lowerer")
	}
	if _, err := exec.Command(evalArgv[0], evalArgv[1:]...).Output(); err != nil {
		t.Errorf("the eval-capable artifact failed: %v", err)
	}
}

// The artifact is a program: its own arguments reach *argv*, it runs from any
// directory, and an uncaught error is a failed exit.
func TestJitArtifactIsAProgram(t *testing.T) {
	src := filepath.Join(t.TempDir(), "args.shen")
	body := `(define args -> (tl (value *argv*)))
(output "~A~%" (args))
(simple-error "boom")
`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, argv := jitBuild(t, src, shakeOpts{})
	cmd := exec.Command(argv[0], "a", "b")
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	if string(out) != "[a b]\n" && string(out) != "[\"a\" \"b\"]\n" {
		t.Errorf("stdout = %q", out)
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Errorf("an uncaught error exited with %v, want status 1", err)
	}
}

// thread is the one name stage 1's port initialiser defines for a whole image.
func TestJitThread(t *testing.T) {
	src := filepath.Join(t.TempDir(), "thread.shen")
	body := `(set done false)
(define work 0 -> (set done true) N -> (work (- N 1)))
(thread (freeze (work 1000)))
(define wait -> (if (value done) finished (wait)))
(output "~A~%" (wait))
`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, argv := jitBuild(t, src, shakeOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil || string(out) != "finished\n" {
		t.Fatalf("stdout = %q, err = %v", out, err)
	}
}

func TestJitPruneInitStillRuns(t *testing.T) {
	want, err := os.ReadFile("tests/fib.expected")
	if err != nil {
		t.Fatal(err)
	}
	_, argv := jitBuild(t, "tests/fib.shen", shakeOpts{pruneInit: true, target: "jit"})
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil || string(out) != string(want) {
		t.Fatalf("stdout = %q, err = %v", out, err)
	}
}

// The traced slice runs its boot at start, so the trace sees the boot phase.
func TestJitTraceCheck(t *testing.T) {
	host := checkHost(t)
	jitCheckout(t)
	bin := filepath.Join(t.TempDir(), "ygg")
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	for _, fixture := range []string{"fib", "prolog", "metaeval"} {
		cmd := exec.Command(bin, "trace-check", "tests/"+fixture+".shen", t.TempDir(), "--target", "jit",
			"--host", strings.Join(host, " "))
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v\n%s", fixture, err, b)
		}
		if !strings.Contains(string(b), "yggdrasil-trace-check: OK") {
			t.Errorf("%s: no OK line\n%s", fixture, b)
		}
		if strings.Contains(string(b), "boot=0") {
			t.Errorf("%s: the trace saw no boot phase\n%s", fixture, b)
		}
	}
}

// A function defined twice cannot behave as written here, as user defuns are all
// in the image before the first toplevel form runs. The builder has to refuse it,
// not print the last definition's answer for both calls.
func TestJitRefusesRedefinition(t *testing.T) {
	jitCheckout(t)
	if defaultHost() == nil {
		t.Skip("no Shen host available")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "redefine.shen")
	src := "(define f -> 1)\n(output \"~A~%\" (f))\n(define f -> 2)\n(output \"~A~%\" (f))\n"
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"build", prog, filepath.Join(dir, "out"), "--target", "jit"}); code != 3 {
		t.Fatalf("build of a program that redefines a function exited %d, want 3", code)
	}
	msg, err := os.ReadFile(filepath.Join(dir, "out", "app-jit.build", "stage0", "jit-refused.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "defined more than once") {
		t.Errorf("refusal does not say why: %q", msg)
	}
}

// A machine with no host cannot check a program, which is not a failure of the
// program: Bifrost reads exit 3 as a SKIP.
func TestNoHostIsACapabilityLimit(t *testing.T) {
	t.Setenv("YGGDRASIL_HOST", "")
	t.Setenv("BIFROST_SHEN_JIT", "")
	t.Chdir(t.TempDir())
	if defaultHost() != nil {
		t.Skip("a sibling host is in reach of the temp directory")
	}
	prog := filepath.Join(t.TempDir(), "p.shen")
	if err := os.WriteFile(prog, []byte("(output \"x\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"shake", prog, filepath.Join(t.TempDir(), "out")}); code != 3 {
		t.Fatalf("shake with no host exited %d, want 3", code)
	}
}
