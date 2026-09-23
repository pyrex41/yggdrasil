package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanon(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\r\nb\r\n", "a\nb"},
		{"a\nb\n\n\n", "a\nb"},
		{"a\nb", "a\nb"},
		{"", ""},
	}
	for _, c := range cases {
		if got := canon(c.in); got != c.want {
			t.Errorf("canon(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitPasses(t *testing.T) {
	// Two identical passes around a "===" line.
	a, b, ok := splitPasses("x => 1\ny => 2\n===\nx => 1\ny => 2\n")
	if !ok {
		t.Fatal("expected marker to be found")
	}
	if a != "x => 1\ny => 2" || b != "x => 1\ny => 2" {
		t.Errorf("halves = %q / %q", a, b)
	}

	// Differing passes are still split (the caller compares them).
	a, b, ok = splitPasses("first\n===\nsecond\n")
	if !ok || a != "first" || b != "second" {
		t.Errorf("split = %q / %q ok=%v", a, b, ok)
	}

	// Whitespace around the marker line is tolerated.
	if _, _, ok := splitPasses("p\n  ===  \nq\n"); !ok {
		t.Error("marker with surrounding whitespace should be found")
	}

	// No marker -> ok=false (two-pass check is reported N/A, not a failure).
	if _, _, ok := splitPasses("no marker here\n"); ok {
		t.Error("expected ok=false when marker is absent")
	}

	// "====" is not the marker (must be exactly "===").
	if _, _, ok := splitPasses("a\n====\nb\n"); ok {
		t.Error("==== must not be treated as the separator")
	}
}

func TestFirstDiff(t *testing.T) {
	if ln, _, _ := firstDiff("a\nb\nc", "a\nb\nc"); ln != 0 {
		t.Errorf("identical strings should report line 0, got %d", ln)
	}
	ln, xs, ys := firstDiff("a\nb\nc", "a\nX\nc")
	if ln != 2 || xs != "b" || ys != "X" {
		t.Errorf("firstDiff = (%d, %q, %q)", ln, xs, ys)
	}
	// Length mismatch: the first extra line is the diff, reported as "".
	ln, xs, ys = firstDiff("a\nb", "a\nb\nc")
	if ln != 3 || xs != "" || ys != "c" {
		t.Errorf("firstDiff len-mismatch = (%d, %q, %q)", ln, xs, ys)
	}
}

// runCaptureHelper is re-executed as a child process by
// TestRunCaptureFeedsStdin: it echoes what it reads on stdin, prefixed, so the
// test can tell "was given the file" from "was given nothing".  Using the test
// binary itself keeps this portable -- `cat` is not on the Windows runner that
// go.yml also builds for.
func TestRunCaptureHelper(t *testing.T) {
	if os.Getenv("YGG_TEST_HELPER") != "1" {
		t.Skip("helper process, driven by TestRunCaptureFeedsStdin")
	}
	b, _ := io.ReadAll(os.Stdin)
	fmt.Printf("got:%s", b)
}

func TestRunCaptureFeedsStdin(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate the test binary")
	}
	argv := []string{self, "-test.run", "^TestRunCaptureHelper$"}
	t.Setenv("YGG_TEST_HELPER", "1")

	// No --stdin: the child must see EOF immediately, NOT the parent's stdin.
	// A program that reads to EOF has to terminate, and both boots have to get
	// the same bytes, or two-boot means nothing.
	out, _, err := runCapture(argv, "", "")
	if err != nil {
		t.Fatalf("helper failed: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "got:") || strings.Contains(out, "got:x") {
		t.Errorf("empty stdin expected, got %q", out)
	}

	f := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(f, []byte("hello yggdrasil"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCapture(argv, "", f)
	if err != nil {
		t.Fatalf("helper failed: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "got:hello yggdrasil") {
		t.Errorf("stdin file not delivered, got %q", out)
	}

	// A missing file is an error, not a silent empty stdin: the latter would
	// pass the gate against a golden minted with real input only if the
	// program happened to print the same thing either way.
	if _, _, err = runCapture(argv, "", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing --stdin file must error")
	}
}

// hickey-13. A target that declares `stdout: repl-transcript` is compared with
// the truth by CONTAINMENT, on every leg, and a target that does not is
// compared by equality. Both halves matter: the softening must reach the
// transcript target (or `kl` is permanently red on a property it cannot have)
// and must NOT reach any other (or a port could pass the gate by printing the
// right answer somewhere inside the wrong output).
//
// This is the test builders.json's kl.stdout_checked_by names.
func TestTranscriptTargetIsComparedByContainment(t *testing.T) {
	const truth = "fib 20 = 6765"
	// A transcript: the truth embedded in the runtime's own lines, and a
	// second boot whose prompts and panic addresses differ.
	bootA := "0- 1+ \nfib 20 = 6765\n1- done 0x1234\n"
	bootB := "0- 1+ \nfib 20 = 6765\n1- done 0xbeef\n"

	plain := &parityResult{outA: bootA, outB: bootB}
	if v := compareParity(plain, truth); v.vsTruth || v.twoBoot {
		t.Errorf("a target that declares stdout=program was compared by containment: %+v", v)
	}
	tr := &parityResult{outA: bootA, outB: bootB, transcript: true}
	v := compareParity(tr, truth)
	if !v.vsTruth || !v.twoBoot {
		t.Errorf("the transcript target's boots both contain the truth and the gate says %+v", v)
	}
	// And it still fails on a boot that answered wrongly.
	wrong := &parityResult{outA: bootA, outB: "0- 1+ \nfib 20 = 6764\n", transcript: true}
	if v := compareParity(wrong, truth); v.twoBoot {
		t.Errorf("a second boot with the wrong answer passed the two-boot leg: %+v", v)
	}
	if v := compareParity(&parityResult{outA: "0- nothing\n", outB: "0- nothing\n", transcript: true},
		truth); v.vsTruth {
		t.Errorf("a transcript without the truth in it passed vs-truth: %+v", v)
	}

	// The two-pass leg: each half of the transcript against the
	// corresponding half of the truth, because the halves are not
	// comparable with each other.
	const twoPassTruth = "a => 1\n===\na => 1"
	pass := &parityResult{transcript: true,
		outA: "0 #> pass\na => 1\n===\n1 #> pass\na => 1\n2 #> done\n"}
	pass.outB = pass.outA
	v = compareParity(pass, twoPassTruth)
	if !v.hasPasses || !v.twoPass {
		t.Errorf("a transcript whose two passes both printed the truth's pass failed two-pass: %+v", v)
	}
	// Byte equality of the halves would have failed it, which is the point.
	if v.pass1 == v.pass2 {
		t.Fatal("this fixture is meant to have halves that differ byte for byte")
	}
	bad := &parityResult{transcript: true,
		outA: "0 #> pass\na => 1\n===\n1 #> pass\na => 2\n2 #> done\n"}
	bad.outB = bad.outA
	if v := compareParity(bad, twoPassTruth); v.twoPass {
		t.Errorf("a second pass that printed something else passed two-pass: %+v", v)
	}
}

// The same hole on the gate's side: `parity --target kl` reported ok/ok on
// every fixture while every boot panicked in (shen.initialise) and the VM
// carried on, because containment found the fixture's answer further down the
// transcript. compareParity checks the target's declared
// transcript_error_markers first, and an empty truth first of all.
//
// This is the test builders.json's kl.transcript_error_markers_checked_by
// names, and it reads the markers off the declaration rather than repeating
// them.
func TestTranscriptErrorMarkerFailsParity(t *testing.T) {
	markers := transcriptErrorMarkers("kl")
	if len(markers) == 0 {
		t.Fatal("kl declares no transcript_error_markers; containment would be the whole gate")
	}
	const truth = "fib 20 = 6765"
	clean := "0- 1+ \nfib 20 = 6765\n1- done 0x1234\n"

	ok := &parityResult{target: "kl", outA: clean, outB: clean, transcript: true}
	if v := compareParity(ok, truth); !v.vsTruth || !v.twoBoot || v.why != "" {
		t.Fatalf("a clean transcript failed the gate: %+v", v)
	}

	for _, m := range markers {
		t.Run(strings.TrimSpace(m), func(t *testing.T) {
			bad := "0- 1+ \n53 #> " + m + "&{22 implementation error in shen.change-pointer-value}\n" +
				"fib 20 = 6765\n1- done\n"
			// Boot A carries it.
			v := compareParity(&parityResult{target: "kl", outA: bad, outB: clean, transcript: true}, truth)
			if v.vsTruth || v.twoBoot || v.twoPass {
				t.Errorf("a transcript carrying %q passed a leg: %+v", m, v)
			}
			if !strings.Contains(v.why, m) || !strings.Contains(v.why, "shen.change-pointer-value") {
				t.Errorf("the verdict does not name the marker and the line: %q", v.why)
			}
			if !strings.Contains(v.why, "transcript-error") {
				t.Errorf("the verdict does not name its reason: %q", v.why)
			}
			// Boot B alone carries it: the second boot is a boot too.
			v = compareParity(&parityResult{target: "kl", outA: clean, outB: bad, transcript: true}, truth)
			if v.vsTruth || v.twoBoot {
				t.Errorf("a marker in bootB passed: %+v", v)
			}
			if !strings.Contains(v.why, "bootB") {
				t.Errorf("the verdict does not say which boot: %q", v.why)
			}
			// A target that declares no markers is unaffected, and an
			// ordinary target compares by equality as before.
			if v := compareParity(&parityResult{target: "go", outA: bad, outB: bad}, truth); v.why != "" {
				t.Errorf("an ordinary target was judged by a transcript's markers: %q", v.why)
			}
		})
	}

	// An empty truth: containment holds against anything, so it is a FAIL
	// with its own reason rather than three quiet oks.
	v := compareParity(ok, "")
	if v.vsTruth || v.twoBoot || v.twoPass {
		t.Errorf("an empty golden passed by containment: %+v", v)
	}
	if !strings.Contains(v.why, "golden-empty") {
		t.Errorf("the verdict does not name its reason: %q", v.why)
	}
}
