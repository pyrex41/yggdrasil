package main

// Host-gated integration tests for the stage-2 initialisation-order check
// (docs/analysis-rules.md). They boot a real stage-1 host per shake, so they
// skip cleanly when none is around (build ../shen-cl or set $YGGDRASIL_HOST).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bad fixture reads *greeting* one form before the form that sets it.
// The shake must refuse it, name the variable, and leave no kernel.kl.
func TestInitOrderBadIsRefused(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	_, err := shake("tests/init-order-bad.shen", out, host, "sub", true)
	if err == nil {
		t.Fatalf("init-order-bad must not shake")
	}
	if !strings.Contains(err.Error(), "FAIL init-order") ||
		!strings.Contains(err.Error(), "*greeting*") {
		t.Fatalf("error must carry the sentinel naming the variable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "reads=") {
		t.Fatalf("sentinel must use the reads= key, got: %v", err)
	}
	if fi, statErr := os.Stat(filepath.Join(out, "kernel.kl")); statErr == nil && fi.Size() > 0 {
		t.Fatalf("a refused shake must write no kernel.kl (found %d bytes)", fi.Size())
	}
}

// The same reads in boot order shake fine, and say so in both manifests.
func TestInitOrderOKShakesAndRecords(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/init-order-ok.shen", out, host, "sub", true); err != nil {
		t.Fatalf("init-order-ok must shake: %v", err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txt), "init-order=checked") {
		t.Fatalf("manifest.txt must record init-order=checked:\n%s", txt)
	}
	sexp, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sexp), `("init-order" checked)`) {
		t.Fatalf("sexp manifest must record (\"init-order\" checked):\n%s", sexp)
	}
}

func TestShakeFailReport(t *testing.T) {
	out := "loading ...\nyggdrasil-shake: FAIL init-order form=38 reads=*greeting*\nERROR: boom\n"
	got, ok := shakeFailReport(out)
	if !ok || got != "yggdrasil-shake: FAIL init-order form=38 reads=*greeting*" {
		t.Fatalf("sentinel not extracted: %q (ok=%v)", got, ok)
	}
	if _, ok := shakeFailReport("no sentinel here\n"); ok {
		t.Fatalf("reported a sentinel that is not there")
	}
	if !strings.Contains(shakeFailHint(got), "reads a global") {
		t.Fatalf("init-order hint missing: %q", shakeFailHint(got))
	}
}
