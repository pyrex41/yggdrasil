package main

// Host-gated integration tests for the stage-2 initialisation-order check
// (docs/analysis-rules.md). They boot a real stage-1 host per shake, so they
// skip cleanly when none is around (build ../shen-cl or set $YGGDRASIL_HOST).

import (
	"os"
	"path/filepath"
	"strconv"
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
//
// The value is asserted exactly, not as a prefix: "init-order=checked" is a
// substring of "init-order=checked-weak", and a prefix match here plus one in
// TestInitOrderSetterShakes would both pass if the key went back to being the
// constant it used to be. Between them the two tests pin both spellings.
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
	if got := manifestKey(t, string(txt), "init-order"); got != "checked" {
		t.Fatalf("manifest.txt: init-order = %q, want checked:\n%s", got, txt)
	}
	sexp, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sexp), `("init-order" checked)`) {
		t.Fatalf("sexp manifest must record (\"init-order\" checked):\n%s", sexp)
	}
}

// A global set by a function the program CALLS at toplevel is initialised,
// and the shake must say so rather than refusing the program with a hint
// ("reorder") that cannot be acted on. The write side is closed over the
// call graph, which is an over-approximation, so both manifests must say
// checked-weak rather than checked -- that distinction is the whole point of
// the key, and asserting it here is what stops it collapsing back to the
// constant it used to be.
func TestInitOrderSetterShakes(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/init-order-setter.shen", out, host, "sub", true); err != nil {
		t.Fatalf("a global written by a toplevel call must shake: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(out, "kernel.kl")); err != nil || fi.Size() == 0 {
		t.Fatalf("shake wrote no kernel.kl (err=%v)", err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := manifestKey(t, string(txt), "init-order"); got != "checked-weak" {
		t.Fatalf("a read discharged through the call graph must record init-order=checked-weak, got %q:\n%s", got, txt)
	}
	sexp, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sexp), `("init-order" checked-weak)`) {
		t.Fatalf("sexp manifest must record (\"init-order\" checked-weak):\n%s", sexp)
	}
}

// (let X *a* (print (value X))) reads a KL VARIABLE, not a global named X.
// Guarding the read extractor on symbol? alone reported it as an unwritten
// global; the guard is ygg.cn-literal-sym? now, and a computed name is stage
// 3's business (computed-names=), never an init-order violation.
func TestInitOrderLetVarIsNotAGlobal(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/init-order-letvar.shen", out, host, "sub", true); err != nil {
		t.Fatalf("a (value X) on a let-bound variable must not be an init-order finding: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(out, "kernel.kl")); err != nil || fi.Size() == 0 {
		t.Fatalf("shake wrote no kernel.kl (err=%v)", err)
	}
}

// manifestKey returns the value of one key= line of a yggdrasil.manifest.txt,
// so a test can assert the whole value rather than a prefix of it.
func manifestKey(t *testing.T, manifest, key string) string {
	t.Helper()
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	t.Fatalf("manifest has no %s= key:\n%s", key, manifest)
	return ""
}

// Closing the write side over the call graph must not disarm the check. The
// two programs below differ only in WHERE the call to the setter sits: before
// the read it discharges it, after the read it discharges nothing, because
// the order relation the rules walk is strict: a form's own writes cover its
// own reads, but a LATER form's cover nothing. A transitive write analysis
// that ignored order would accept both.
//
// These live in a temp dir rather than under tests/, because the second one
// must not shake and analysis_test.go's oracle globs tests/*.shen.
func TestInitOrderCallOrderStillDecides(t *testing.T) {
	host := checkHost(t)
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// setup is reached through outer, two edges away from the toplevel form.
	transitive := write("transitive.shen", `(define inner -> (set *z* 7))
(define outer -> (inner))
(outer)
(print (value *z*))
`)
	if _, err := shake(transitive, t.TempDir(), host, "sub", true); err != nil {
		t.Fatalf("a global written two calls deep must shake: %v", err)
	}

	// The same setter, called one form too late.
	late := write("late.shen", `(define setup -> (set *q* 1))
(print (value *q*))
(setup)
`)
	_, err := shake(late, t.TempDir(), host, "sub", true)
	if err == nil {
		t.Fatalf("a setter called AFTER the read must not discharge it")
	}
	if !strings.Contains(err.Error(), "FAIL init-order") || !strings.Contains(err.Error(), "*q*") {
		t.Fatalf("want the init-order sentinel naming *q*, got: %v", err)
	}
}

// The check is a Datalog rule set now, so the other two engines must derive
// the same answer from the dumped facts: the violation for the refused
// fixture, nothing for a program that boots. `yggdrasil facts` runs no
// init-order check, so it dumps the refused program's facts happily.
func TestInitOrderRuleInRefeval(t *testing.T) {
	host := checkHost(t)
	for _, tc := range []struct {
		prog string
		want []string
	}{
		{"tests/init-order-bad.shen", []string{"*greeting*"}},
		{"tests/init-order-setter.shen", nil},
	} {
		dir := t.TempDir()
		if _, err := facts(tc.prog, dir, host, "sub", true); err != nil {
			t.Fatalf("facts %s: %v", tc.prog, err)
		}
		for _, rel := range []string{"defwrite", "fcall", "formcalls", "formwrite", "succ"} {
			if _, err := os.Stat(filepath.Join(dir, rel+".facts")); err != nil {
				t.Fatalf("%s: %s.facts not dumped: %v", tc.prog, rel, err)
			}
		}
		assertSuccIsLinear(t, dir)
		got := relWithRefeval(t, dir, "readBeforeWrite")
		if len(got) != len(tc.want) {
			t.Fatalf("%s: readBeforeWrite = %v, want %d tuple(s)", tc.prog, got, len(tc.want))
		}
		for _, v := range tc.want {
			found := false
			for tuple := range got {
				if strings.Contains(tuple, v) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: readBeforeWrite must name %s, got %v", tc.prog, v, got)
			}
		}
	}
}

// assertSuccIsLinear pins the shape of the order EDB, not just its content.
// succ is the adjacent pairs -- one tuple per form gap -- because the shake's
// own engine loads a quadratic before(M,N) relation in cubic time (D11 in
// analysis/analysis.dl). Going back to a tuple per M < N pair would still
// derive the right answer, so nothing else in the suite would notice.
func assertSuccIsLinear(t *testing.T, dir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "succ.facts"))
	if err != nil {
		t.Fatalf("reading succ.facts: %v", err)
	}
	rows, maxN := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		rows++
		cols := strings.Fields(line)
		if len(cols) != 2 {
			t.Fatalf("succ.facts row %q: want two columns", line)
		}
		n, err := strconv.Atoi(cols[1])
		if err != nil {
			t.Fatalf("succ.facts row %q: %v", line, err)
		}
		if n > maxN {
			maxN = n
		}
	}
	if rows != maxN-1 {
		t.Errorf("succ.facts has %d rows over %d forms; the adjacent pairs are %d "+
			"(a quadratic order EDB would be %d)", rows, maxN, maxN-1, maxN*(maxN-1)/2)
	}
}

// A form's own writes discharge its own reads, and the write side descends
// into freeze and lambda. Both programs in the fixture run correctly under an
// unshaken Shen and under main; refusing them was the other half of the
// regression this check was fixed for. Both relaxations are imprecise, so the
// manifest must say checked-weak.
func TestInitOrderSameFormAndFreezeShake(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/init-order-sameform.shen", out, host, "sub", true); err != nil {
		t.Fatalf("a form that writes and reads the same global must shake: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(out, "kernel.kl")); err != nil || fi.Size() == 0 {
		t.Fatalf("shake wrote no kernel.kl (err=%v)", err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := manifestKey(t, string(txt), "init-order"); got != "checked-weak" {
		t.Fatalf("a same-form or freeze-wrapped write is an over-approximation and must "+
			"record init-order=checked-weak, got %q:\n%s", got, txt)
	}
}

// ...and neither relaxation may disarm the check: a write in a LATER form
// still discharges nothing, whether it is a plain (set V _) or one wrapped in
// a freeze. These live in a temp dir because they must not shake and
// analysis_test.go's oracle globs tests/*.shen.
func TestInitOrderLaterWriteStillRefused(t *testing.T) {
	host := checkHost(t)
	for name, body := range map[string]string{
		"late-do.shen":     "(print (value *lx*))\n(do (set *lx* 1) 0)\n",
		"late-freeze.shen": "(print (value *lf*))\n(thaw (freeze (set *lf* 1)))\n",
	} {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := shake(p, t.TempDir(), host, "sub", true)
		if err == nil {
			t.Fatalf("%s: a write in a later form must not discharge an earlier read", name)
		}
		if !strings.Contains(err.Error(), "FAIL init-order") {
			t.Fatalf("%s: want the init-order sentinel, got: %v", name, err)
		}
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
