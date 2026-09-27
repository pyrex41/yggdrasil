package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Multi-file programs (yggdrasil.shen, "multi-file programs"): an entry
// file's LEADING (load "lit") forms are hoisted into user files of their
// own, in load order, and the load forms leave the KL. These tests pin the
// three halves of that: the hoist itself, the two refusals, and the loads it
// deliberately does not hoist.

// userLines returns the manifest's user= values in order.
func userLines(manifest string) []string {
	var out []string
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(line, "user=") {
			out = append(out, strings.TrimPrefix(line, "user="))
		}
	}
	return out
}

func writeProgram(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// tests/tla-election.shen loads the tla.shen library and the election spec.
// The shake must emit three user files in load order, stay eval-free (load
// is an eval entry point, so a load left in the KL would flip it), and leave
// no load form in the entry file's KL.
func TestMultiFileLoadsAreHoisted(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/tla-election.shen", out, host, "sub", true); err != nil {
		t.Fatalf("shake: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m := string(b)
	want := []string{"tla.kl", "election.kl", "tla-election.kl"}
	if got := userLines(m); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("user= lines = %v, want %v (load order)", got, want)
	}
	if v := manifestKey(t, m, "needs-eval"); v != "false" {
		t.Fatalf("needs-eval=%s: a hoisted load must not leave the program eval-capable", v)
	}
	entry, err := os.ReadFile(filepath.Join(out, "tla-election.kl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(entry), "(load ") {
		t.Fatalf("the entry file's KL still carries a load form:\n%s", entry)
	}
}

// A load the shake does not hoist stays in the KL and keeps the program
// eval-capable, exactly as before hoisting existed: one after another form
// (its position is an ordering the shake would have to prove it keeps), and
// one whose path is computed.
func TestMultiFileUnhoistedLoadStaysEvalCapable(t *testing.T) {
	host := checkHost(t)
	dir := t.TempDir()
	writeProgram(t, dir, "lib.shen", "(define lib-f X -> X)\n")
	for name, body := range map[string]string{
		"late.shen":     "(print 1)\n(load \"lib.shen\")\n",
		"computed.shen": "(load (cn \"lib\" \".shen\"))\n",
	} {
		prog := writeProgram(t, dir, name, body)
		out := t.TempDir()
		if _, err := shake(prog, out, host, "sub", true); err != nil {
			t.Fatalf("%s: shake: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
		if err != nil {
			t.Fatal(err)
		}
		m := string(b)
		if v := manifestKey(t, m, "needs-eval"); v != "true" {
			t.Errorf("%s: needs-eval=%s, want true: an unhoisted load evaluates a file at run time", name, v)
		}
		if got := userLines(m); len(got) != 1 {
			t.Errorf("%s: user= lines = %v, want the entry file alone", name, got)
		}
	}
}

// The shapes the hoist refuses, each with the FAIL sentinel naming it: a
// file reached twice (the host would load it twice; the shake emits each
// file once), a cycle, and two files whose .kl names would collide in
// OUTDIR.
func TestMultiFileRefusals(t *testing.T) {
	host := checkHost(t)
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"diamond", map[string]string{
			"main.shen": "(load \"b.shen\")\n(load \"c.shen\")\n",
			"b.shen":    "(load \"c.shen\")\n(define b-f -> 1)\n",
			"c.shen":    "(define c-f -> 2)\n",
		}, "FAIL load loaded-twice"},
		{"cycle", map[string]string{
			"main.shen": "(load \"b.shen\")\n",
			"b.shen":    "(load \"main.shen\")\n",
		}, "FAIL load cycle"},
		{"basename", map[string]string{
			"main.shen": "(load \"x/u.shen\")\n(load \"y/u.shen\")\n",
			"x/u.shen":  "(define xu -> 1)\n",
			"y/u.shen":  "(define yu -> 2)\n",
		}, "FAIL load same-basename"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				writeProgram(t, dir, name, body)
			}
			out := t.TempDir()
			_, err := shake(filepath.Join(dir, "main.shen"), out, host, "sub", true)
			if err == nil {
				t.Fatalf("shake accepted a %s of loads", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want the %q sentinel, got: %v", tc.want, err)
			}
			if _, err := os.Stat(filepath.Join(out, "kernel.kl")); err == nil {
				t.Fatalf("a refused shake wrote kernel.kl")
			}
		})
	}
}
