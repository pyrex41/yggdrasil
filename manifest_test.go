package main

// The manifest contract, checked across BOTH shake modes.
//
// yggdrasil.shen carries this confession in ygg.shake-full-h, next to the
// call it is about:
//
//	\\ A full build prunes nothing, so pruned-init is 0.  The
//	\\ argument is not optional: write-manifest took a fifth
//	\\ argument when this was written and a sixth once stage 4
//	\\ landed, and a short call here does not fail - Shen
//	\\ curries it into a closure this `let` then discards, so
//	\\ the full build silently wrote no manifest at all.
//
// That is a class of bug, not one incident: in Shen a call with too few
// arguments is not an error, it is a partial application, and a partial
// application bound by a `let` whose body ignores it is a no-op that costs
// nothing and says nothing. Every writer reached through a `let` in that file
// -- write-manifest, write-kl-file, write-user-files -- can be silenced the
// same way by anyone who adds a parameter and misses a call site. Nothing
// guarded it: the comment WAS the guard.
//
// A golden file would not catch it. The full build's manifest differs from
// the shaken one in nearly every VALUE (it carries every kernel defun's
// primitives, not the slice's), so there is nothing to freeze. What is
// invariant is the SHAPE: both modes run the same write-manifest, so both
// must write both manifest files, and the KEYS must agree.
//
// "Agree" is not quite "be equal", and the difference is itself part of the
// contract. write-manifest-sexp emits exactly one line per key, so a key
// whose list is empty still appears as `("cannot-reach")`. write-manifest-txt
// emits one line per ELEMENT via ygg.mapc, so the same empty list prints
// nothing at all and the key vanishes from the .txt. That is real: a full
// build reaches every capability, so its cannot-reach list is empty and its
// .txt has no cannot-reach= line, while the shaken build's does.
//
// So the test checks three things, in this order of strength:
//
//	WITHIN a build   every sexp key whose line carries values has its .txt
//	                 key, and every .txt key has a sexp line. This is the
//	                 direction that catches a writer which stops writing a key
//	                 in BOTH modes -- symmetric, and still wrong.
//	ACROSS the modes the sexp key sets are identical modulo `shaken`
//	                 (full-build-only by design), and a .txt key missing from
//	                 one mode only is legitimate only when that mode's sexp
//	                 line for it is empty.
//	A FLOOR          the handful of keys this repo's own consumers read by
//	                 name must be present; see requiredTxtKeys. This is what
//	                 catches a key dropped from write-manifest-sexp AND
//	                 write-manifest-txt at once, where there is no surviving
//	                 half to compare against.
//
// which pins the shape without pretending the two files have the same one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// txtToSexpKey maps the line-oriented manifest's key spellings onto the
// s-expression manifest's, for the keys where they differ. The .txt prints
// one line per element and so names its key in the singular; the sexp prints
// one line per key and names it in the plural. Keys not listed here are
// spelled identically in both files.
var txtToSexpKey = map[string]string{
	"manifest-version":   "yggdrasil-manifest",
	"primitive":          "primitives",
	"primitive-optional": "primitives-optional",
	"global":             "globals",
}

// sexpToTxtKey is the inverse, built once so the two directions cannot drift.
var sexpToTxtKey = func() map[string]string {
	inv := make(map[string]string, len(txtToSexpKey))
	for txt, sexp := range txtToSexpKey {
		inv[sexp] = txt
	}
	return inv
}()

// requiredTxtKeys are the manifest keys something in this repo reads BY NAME,
// with the reader named. They are the floor under the two key-set comparisons
// below: a key dropped from write-manifest-sexp and write-manifest-txt in the
// same edit is symmetric in every comparison this file makes, and would sail
// through -- while a stage-2 builder that greps for it gets nothing and builds
// the wrong thing, or nothing, without a word. Adding a reader to this list is
// cheap; removing an entry means the consumer went away, and the commit should
// say so.
var requiredTxtKeys = map[string]string{
	"user":           "builders/{scheme,erlang,swift}/build.sh and builders/joy/lower.py read `user=` to find the program's KL",
	"needs-eval":     "main.go webPreflight refuses a --web build on needs-eval=true, and builders.json gates conditional steps on `needs-eval=`",
	"fn":             "scip.go userDefuns reads `fn=` for the user program's own defuns",
	"init-order":     "initorder_test.go pins `init-order=checked`",
	"computed-names": "analysis_test.go and footprint_test.go read `computed-names=`",
}

// txtManifestKeys is the key half of the line-oriented manifest, as a set.
// It reuses manifestPairs (main.go) -- the reader every consumer of this file
// already goes through -- so a manifest this test accepts is one the stage-2
// builders can read.
func txtManifestKeys(t *testing.T, dir string) map[string]bool {
	t.Helper()
	pairs := manifestPairs(dir)
	if pairs == nil {
		t.Fatalf("no readable %s", filepath.Join(dir, "yggdrasil.manifest.txt"))
	}
	keys := map[string]bool{}
	for line := range pairs {
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("yggdrasil.manifest.txt line is not key=value: %q", line)
			continue
		}
		keys[k] = true
	}
	return keys
}

// sexpManifestKeys reads the s-expression manifest, whose lines pr-kl-line
// emits as `("key" v ...)`, and reports for each key whether that line
// carried any values at all. The bool is what lets the .txt's missing keys be
// judged: an empty list is a legitimate absence there, a non-empty one is a
// writer that stopped writing.
func sexpManifestKeys(t *testing.T, dir string) map[string]bool {
	t.Helper()
	path := filepath.Join(dir, "yggdrasil.manifest")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no readable %s: %v", path, err)
	}
	nonEmpty := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, `("`)
		if !ok {
			t.Errorf(`%s line does not start with ("key: %q`, path, line)
			continue
		}
		k, vals, ok := strings.Cut(rest, `"`)
		if !ok {
			t.Errorf("%s line has an unterminated key: %q", path, line)
			continue
		}
		vals = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(vals), ")"))
		// A key repeated across lines (fn) is non-empty if any line was.
		nonEmpty[k] = nonEmpty[k] || vals != ""
	}
	return nonEmpty
}

// sexpKeySet drops the values-present flag, leaving the key set.
func sexpKeySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// checkWithinBuild is the assertion the cross-mode comparisons cannot make.
// They compare one build against the other, so a writer that stops writing a
// key in BOTH modes stays perfectly symmetric and passes. Here the two FILES
// of a single build are checked against each other instead: write-manifest-sexp
// and write-manifest-txt are two renderings of the same data, so a key present
// with values in one must be present in the other.
//
// sexp is the key -> "line carried values" map from sexpManifestKeys; the flag
// is what makes an absent .txt key legitimate (ygg.mapc over [] prints nothing).
func checkWithinBuild(t *testing.T, what string, txt, sexp map[string]bool) {
	t.Helper()
	for _, sk := range sortedKeys(sexp) {
		if !sexp[sk] {
			continue // empty list: the .txt legitimately has no line
		}
		tk := sk
		if mapped, ok := sexpToTxtKey[sk]; ok {
			tk = mapped
		}
		if !txt[tk] {
			t.Errorf("%s build: yggdrasil.manifest has (%q ...) WITH values but "+
				"yggdrasil.manifest.txt has no %s= line -- write-manifest-txt stopped "+
				"writing a key write-manifest-sexp still writes", what, sk, tk)
		}
	}
	for _, tk := range sortedKeys(txt) {
		sk := tk
		if mapped, ok := txtToSexpKey[tk]; ok {
			sk = mapped
		}
		if _, ok := sexp[sk]; !ok {
			t.Errorf("%s build: yggdrasil.manifest.txt has %s= lines but "+
				"yggdrasil.manifest has no (%q ...) line -- either write-manifest-sexp "+
				"stopped writing it, or the key was renamed in one file only "+
				"(txtToSexpKey in this file is the spelling map)", what, tk, sk)
		}
	}
	for _, k := range sortedKeys(mapKeys(requiredTxtKeys)) {
		if !txt[k] {
			t.Errorf("%s build: yggdrasil.manifest.txt has no %s= line at all.\n  %s",
				what, k, requiredTxtKeys[k])
		}
	}
}

// mapKeys adapts a documented key list to sortedKeys' map[string]bool.
func mapKeys(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func TestManifestKeySetsMatch(t *testing.T) {
	host := checkHost(t)

	const prog = "tests/fib.shen"
	shakenDir, fullDir := t.TempDir(), t.TempDir()

	// The two modes that share write-manifest: yggdrasil.shake, and with
	// full=true yggdrasil.shake-full (the CLI's --no-shake).
	if _, err := shakeMode(prog, shakenDir, host, "sub", true, false); err != nil {
		t.Fatalf("shaken build of %s: %v", prog, err)
	}
	if _, err := shakeMode(prog, fullDir, host, "sub", true, true); err != nil {
		t.Fatalf("full (--no-shake) build of %s: %v", prog, err)
	}

	// Both files, both modes, non-empty. This is the assertion a curried
	// write-manifest fails outright: the full build writes kernel.kl and
	// fib.kl and then, silently, no manifest at all.
	for _, b := range []struct{ what, dir string }{
		{"shaken", shakenDir},
		{"full (--no-shake)", fullDir},
	} {
		for _, name := range []string{"yggdrasil.manifest.txt", "yggdrasil.manifest"} {
			fi, err := os.Stat(filepath.Join(b.dir, name))
			if err != nil {
				t.Fatalf("%s build wrote no %s: %v\n"+
					"  A short call to write-manifest does not fail in Shen; it curries into a\n"+
					"  closure the enclosing `let` discards. Check the arity of every\n"+
					"  write-manifest call site in yggdrasil.shen.",
					b.what, name, err)
			}
			if fi.Size() == 0 {
				t.Fatalf("%s build wrote an EMPTY %s", b.what, name)
			}
		}
	}

	// `shaken` is written only by ygg.shaken-line-*, i.e. only in full mode;
	// its absence is what every pre-stage-5 builder reads as "shaken".
	const shakenKey = "shaken"

	shakenSexp, fullSexp := sexpManifestKeys(t, shakenDir), sexpManifestKeys(t, fullDir)
	shakenSexpKeys, fullSexpKeys := sexpKeySet(shakenSexp), sexpKeySet(fullSexp)
	if len(shakenSexpKeys) == 0 || len(fullSexpKeys) == 0 {
		t.Fatalf("an s-expression manifest has no keys at all (shaken=%d full=%d)",
			len(shakenSexpKeys), len(fullSexpKeys))
	}
	for _, k := range sortedKeys(shakenSexpKeys) {
		if !fullSexpKeys[k] {
			t.Errorf("yggdrasil.manifest: key %q is in the shaken build and missing from the full one", k)
		}
	}
	for _, k := range sortedKeys(fullSexpKeys) {
		if !shakenSexpKeys[k] && k != shakenKey {
			t.Errorf("yggdrasil.manifest: key %q is in the full build and missing from the shaken one", k)
		}
	}

	shakenTxt, fullTxt := txtManifestKeys(t, shakenDir), txtManifestKeys(t, fullDir)
	if len(shakenTxt) == 0 || len(fullTxt) == 0 {
		t.Fatalf("a .txt manifest has no keys at all (shaken=%d full=%d)", len(shakenTxt), len(fullTxt))
	}
	// A .txt key may be absent from a build only when that build's sexp line
	// for the same key is empty -- ygg.mapc over [] prints nothing.
	checkTxt := func(missingFrom string, have, want map[string]bool, sexpOfMissing map[string]bool) {
		t.Helper()
		for _, k := range sortedKeys(want) {
			if have[k] || k == shakenKey {
				continue
			}
			sk := k
			if mapped, ok := txtToSexpKey[k]; ok {
				sk = mapped
			}
			if _, known := sexpOfMissing[sk]; !known {
				t.Errorf("yggdrasil.manifest.txt: key %q missing from the %s build, and its "+
					"s-expression manifest has no %q line either -- the key is simply gone",
					k, missingFrom, sk)
				continue
			}
			if sexpOfMissing[sk] {
				t.Errorf("yggdrasil.manifest.txt: key %q missing from the %s build although that "+
					"build's (%q ...) line carries values -- write-manifest-txt stopped writing it",
					k, missingFrom, sk)
			}
		}
	}
	checkTxt("full", fullTxt, shakenTxt, fullSexp)
	checkTxt("shaken", shakenTxt, fullTxt, shakenSexp)

	// And the direction the two calls above cannot see: each build's own two
	// files against each other, plus the floor of keys this repo reads by name.
	checkWithinBuild(t, "shaken", shakenTxt, shakenSexp)
	checkWithinBuild(t, "full (--no-shake)", fullTxt, fullSexp)

	// The one key only the full build may carry must actually be there, in
	// both files: a full build that forgot to say shaken=false is a full
	// build every stage-2 builder will mistake for a shaken one.
	if !fullTxt[shakenKey] {
		t.Errorf("full build's yggdrasil.manifest.txt has no shaken= line")
	}
	if !fullSexpKeys[shakenKey] {
		t.Errorf(`full build's yggdrasil.manifest has no ("shaken" ...) line`)
	}
	if shakenTxt[shakenKey] || shakenSexpKeys[shakenKey] {
		t.Errorf("a shaken build must not write the shaken key at all (absence is the default)")
	}

	if t.Failed() {
		t.Logf("txt  shaken keys: %v", sortedKeys(shakenTxt))
		t.Logf("txt  full   keys: %v", sortedKeys(fullTxt))
		t.Logf("sexp shaken keys: %v", sortedKeys(shakenSexpKeys))
		t.Logf("sexp full   keys: %v", sortedKeys(fullSexpKeys))
	}
}
