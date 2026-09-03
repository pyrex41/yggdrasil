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
	out, _, err := runCapture(argv, "")
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
	out, _, err = runCapture(argv, f)
	if err != nil {
		t.Fatalf("helper failed: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "got:hello yggdrasil") {
		t.Errorf("stdin file not delivered, got %q", out)
	}

	// A missing file is an error, not a silent empty stdin: the latter would
	// pass the gate against a golden minted with real input only if the
	// program happened to print the same thing either way.
	if _, _, err = runCapture(argv, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing --stdin file must error")
	}
}
