package main

// Host-gated integration tests for the typecheck gate. They boot a real
// stage-1 host per check/shake, so they skip cleanly when none is around
// (build ../shen-cl or set $YGGDRASIL_HOST).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkHost(t *testing.T) []string {
	t.Helper()
	if h := defaultHost(); h != nil {
		return h
	}
	t.Skip("no Shen host available (build ../shen-cl or set $YGGDRASIL_HOST)")
	return nil
}

func TestCheckTypedOK(t *testing.T) {
	host := checkHost(t)
	ver, err := check("tests/typed-ok.shen", host, "sub")
	if err != nil {
		t.Fatalf("typed-ok must pass the gate: %v", err)
	}
	if ver == "" {
		t.Log("check host reported no kernel version (tolerated)")
	}

	// The flag path is check-then-shake in separate processes; shake output
	// must be byte-identical to a plain shake of the same program.
	plain, gated := t.TempDir(), t.TempDir()
	if _, err := shake("tests/typed-ok.shen", plain, host, "sub", true); err != nil {
		t.Fatalf("plain shake: %v", err)
	}
	if _, err := shake("tests/typed-ok.shen", gated, host, "sub", true); err != nil {
		t.Fatalf("gated-path shake: %v", err)
	}
	for _, name := range []string{"kernel.kl", "typed-ok.kl"} {
		a, err := os.ReadFile(filepath.Join(plain, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(gated, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs between plain and check-gated shakes", name)
		}
	}

	// Manifest recording: appended lines land next to needs-eval.
	if err := appendTypecheckManifest(gated, host, ver); err != nil {
		t.Fatal(err)
	}
	m, err := os.ReadFile(filepath.Join(gated, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"needs-eval=false", "typechecked=true"} {
		if !strings.Contains(string(m), want) {
			t.Errorf("manifest missing %q after gate:\n%s", want, m)
		}
	}
}

func TestCheckTypedBadFailsButShakes(t *testing.T) {
	host := checkHost(t)
	out, _, ok, err := runCheck("tests/typed-bad.shen", host, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("typed-bad (declare contradicts define) must FAIL the gate")
	}
	for _, want := range []string{"yggdrasil-check: FAIL", "file=", "form=", "name=bad"} {
		if !strings.Contains(out, want) {
			t.Errorf("FAIL report missing %q:\n%s", want, out)
		}
	}

	// Today's behavior, preserved: the same contradiction shakes without
	// complaint when the gate is not asked for.
	if _, err := shake("tests/typed-bad.shen", t.TempDir(), host, "sub", true); err != nil {
		t.Errorf("plain shake of typed-bad must still succeed: %v", err)
	}
}

func TestCheckUnsignedDefineFails(t *testing.T) {
	host := checkHost(t)
	out, _, ok, err := runCheck("tests/typed-unsigned.shen", host, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("an unsigned define must FAIL the gate")
	}
	for _, want := range []string{"name=nosig", "no type signature"} {
		if !strings.Contains(out, want) {
			t.Errorf("FAIL report missing %q:\n%s", want, out)
		}
	}
}
