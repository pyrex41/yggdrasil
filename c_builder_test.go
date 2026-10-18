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

// Exercise the real builder, manifest-selected files, explicit initialisation
// and native executable. No stage-1 host is needed.
func TestCBuilderNativeRC(t *testing.T) {
	home := os.Getenv("YGGDRASIL_SHEN_C_DIR")
	if home == "" {
		t.Skip("set YGGDRASIL_SHEN_C_DIR to a Shen-C native RC checkout")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	input, output := filepath.Join(work, "shaken input"), filepath.Join(work, "native output")
	if err := os.Mkdir(input, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"yggdrasil.manifest.txt": "kernel=bootstrap.kl\ninit=fixture.init\nfn=fixture.add 1\nuser=first.kl\nuser=second.kl\n",
		"bootstrap.kl":           "(defun fixture.init () (do (set *property-vector* (absvector 1)) (set *fixture-base* 40)))\n(defun put (Name Key Arity Properties) (set *fixture-arity* Arity))\n",
		"first.kl":               "(defun fixture.add (X) (+ (value *fixture-base*) X))\n",
		"second.kl":              "(pr (str (+ (fixture.add 1) (value *fixture-arity*))) (value *stoutput*))\n",
	} {
		if err := os.WriteFile(filepath.Join(input, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "SHEN_C=") && !strings.HasPrefix(entry, "SHEN_RC_PRINT=") {
			env = append(env, entry)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "builders/c/build.sh", input, output)
	cmd.Env = append(env, "SHEN_C="+home, "PKG_CONFIG=/nonexistent/yggdrasil-no-pkg-config")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native RC build: %v\n%s", err, b)
	}
	code, err := os.ReadFile(filepath.Join(output, "app.c"))
	if err != nil || !strings.Contains(string(code), "rc_bootstrap_kernel") {
		t.Fatalf("missing native RC program: %v", err)
	}
	cmd = exec.CommandContext(ctx, filepath.Join(output, "app"))
	cmd.Dir, cmd.Env = work, env
	if b, err := cmd.CombinedOutput(); err != nil || string(b) != "42" {
		t.Fatalf("native RC output = %q, err = %v; want 42", b, err)
	}
}
