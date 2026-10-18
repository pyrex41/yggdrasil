//go:build shen_c

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDynamicLoadClosure exercises the shipped C artifact, rather than only
// checking the shake manifest.  The interpreter is loaded at run time and
// calls the kernel's fix; the exact normal-form result proves that the
// dynamic source was included in the shaker's dependency closure.
func TestDynamicLoadClosure(t *testing.T) {
	host := os.Getenv("YGGDRASIL_DYNAMIC_HOST")
	shenC := os.Getenv("YGGDRASIL_SHEN_C_DIR")
	if host == "" || shenC == "" {
		t.Skip("set YGGDRASIL_DYNAMIC_HOST and YGGDRASIL_SHEN_C_DIR for native dynamic-load coverage")
	}
	work := t.TempDir()
	cli := filepath.Join(work, "yggdrasil")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	buildCLI := exec.CommandContext(ctx, "go", "build", "-o", cli, ".")
	if out, err := buildCLI.CombinedOutput(); err != nil {
		t.Fatalf("build yggdrasil: %v\n%s", err, out)
	}
	outdir := filepath.Join(work, "artifact")
	build := exec.CommandContext(ctx, cli, "build", "tests/tc-interp.shen", outdir,
		"--target", "c", "--host", host)
	build.Env = append(os.Environ(), "YGGDRASIL_SHEN_C_DIR="+shenC)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dynamic-load artifact: %v\n%s", err, out)
	}
	run := exec.CommandContext(ctx, filepath.Join(outdir, "app-c", "app"))
	run.Dir, run.Env = filepath.Dir("tests/interpreter.shen"), os.Environ()
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run dynamic-load artifact: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "normal-form = 7\n") {
		t.Fatalf("dynamic-load output lacks exact normal form: %s", out)
	}

	t.Run("transitive-and-optional", func(t *testing.T) {
		source := filepath.Join(work, "dynamic-source")
		if err := os.MkdirAll(filepath.Join(source, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"main.shen": `(trap-error
  (do (load "lib.shen")
      (if false (load "missing.shen") "unused")
      (pr (cn "nested = " (cn (str (dynamic-fn 3)) "\n")) (stoutput)))
  (/. E (pr (error-to-string E) (stoutput))))
`,
			"lib.shen": `(load "./sub/child.shen")
`,
			"sub/child.shen": `(define dynamic-fn X -> (fix (/. Y X) 0))
(if false (load "./child.shen") "unused")
`,
		}
		for name, text := range files {
			if err := os.WriteFile(filepath.Join(source, name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		nestedOut := filepath.Join(work, "nested-artifact")
		nested := exec.CommandContext(ctx, cli, "build", "main.shen", nestedOut,
			"--target", "c", "--host", host)
		nested.Dir = source
		nested.Env = append(os.Environ(), "YGGDRASIL_SHEN_C_DIR="+shenC)
		if out, err := nested.CombinedOutput(); err != nil {
			t.Fatalf("build transitive dynamic-load artifact: %v\n%s", err, out)
		}
		runNested := exec.CommandContext(ctx, filepath.Join(nestedOut, "app-c", "app"))
		runNested.Dir, runNested.Env = source, os.Environ()
		out, err := runNested.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "nested = 3\\n") {
			t.Fatalf("transitive dynamic-load output = %q, err = %v", out, err)
		}
	})
}
