package main

// Host-gated tests for stage 3 of docs/analysis-rules.md: the shake's
// footprint is now the least fixpoint of (value *shake-rules*), evaluated by
// the ygg.dl engine in yggdrasil.shen. The worklist `reach` and the Warshall
// closure stay in the file as differential oracles, and
// (yggdrasil.footprints ...) is the test-only entry point that runs all
// three over one fixture and compares them.
//
// Since the D8 glue came out, the footprint's ORDER is a defined value -
// kernel load order - rather than a second traversal, so the entry point also
// prints the footprint list and kernel load order and TestFootprintOrder...
// checks the definition holds. Both run over every fixture that shakes.
//
// Also here: the computed-name hypothesis, which warns and changes nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// footprintReport is what one (yggdrasil.footprints ["prog"]) run says: the
// three-engine sentinel, the footprint list, and kernel load order.
type footprintReport struct {
	sentinel string
	foot     []string
	kernel   []string
}

// sentinelLine returns the single line of out starting with prefix, without
// the prefix, or "" plus false when it is absent.
func sentinelLine(out, prefix string) (string, bool) {
	i := strings.Index(out, prefix)
	if i < 0 {
		return "", false
	}
	line := out[i+len(prefix):]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return strings.TrimRight(line, "\r"), true
}

// runFootprintsFull evaluates (yggdrasil.footprints ["prog"]) in a host
// process and returns all three of its sentinel lines. Same trust model as
// shake/why/facts: the sentinel decides, never the exit code.
func runFootprintsFull(prog string, host []string) (footprintReport, error) {
	var rep footprintReport
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return rep, fmt.Errorf("no Shen host launcher found")
	}
	prog, _ = filepath.Abs(prog)
	root, err := yggRoot()
	if err != nil {
		return rep, fmt.Errorf("materialising shaker: %w", err)
	}
	expr := fmt.Sprintf(`(yggdrasil.footprints ["%s"])`, prog)
	argv := append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	out, _ := runAt(wrapExecutable(argv), root)

	line, ok := sentinelLine(out, "yggdrasil-footprints:")
	if !ok {
		os.Stderr.WriteString(out)
		return rep, fmt.Errorf("footprints produced no report (host=%s)", strings.Join(host, " "))
	}
	rep.sentinel = "yggdrasil-footprints:" + line
	if l, ok := sentinelLine(out, "yggdrasil-footprint-order:"); ok {
		rep.foot = strings.Fields(l)
	} else {
		return rep, fmt.Errorf("footprints printed no yggdrasil-footprint-order: line")
	}
	if l, ok := sentinelLine(out, "yggdrasil-kernel-order:"); ok {
		rep.kernel = strings.Fields(l)
	} else {
		return rep, fmt.Errorf("footprints printed no yggdrasil-kernel-order: line")
	}
	return rep, nil
}

// runFootprints keeps the original contract - the sentinel line alone - for
// callers that only want the three-engine verdict.
func runFootprints(prog string, host []string) (string, error) {
	rep, err := runFootprintsFull(prog, host)
	return rep.sentinel, err
}

// shakingFixtures is every fixture under tests/ that the shake accepts.
// init-order-bad is refused by the stage-2 init-order check on purpose, so it
// has no footprint to compare - analysis_test.go skips it the same way.
func shakingFixtures(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join("tests", "*.shen"))
	if err != nil || len(all) == 0 {
		t.Fatalf("no fixtures in tests/: %v", err)
	}
	var keep []string
	for _, prog := range all {
		if strings.TrimSuffix(filepath.Base(prog), ".shen") == "init-order-bad" {
			continue
		}
		keep = append(keep, prog)
	}
	return keep
}

// The three engines must agree on EVERY fixture that shakes, not on a
// hand-picked four: the eval-free/eval-capable split is where the footprint
// differs most, and which fixtures fall on which side changes as fixtures are
// added. The Warshall leg only runs under its size limit and says "skipped"
// above it, which is a pass, not a silent one.
func TestFootprintEnginesAgree(t *testing.T) {
	host := checkHost(t)
	for _, prog := range shakingFixtures(t) {
		prog := prog
		t.Run(strings.TrimSuffix(filepath.Base(prog), ".shen"), func(t *testing.T) {
			line, err := runFootprints(prog, host)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(line, "agree=true") {
				t.Fatalf("the three footprint engines disagree: %s", line)
			}
			t.Log(line)
		})
	}
}

// footprintSubsequence reports whether sub appears inside sup in order, gaps
// allowed, and where it first fell off when it does not. It is the whole
// content of "the footprint is in kernel load order": a list of distinct
// names is a subsequence of another exactly when it is ordered the same way.
// (prune_test.go has a boolean isSubsequence for a different claim.)
func footprintSubsequence(sub, sup []string) (int, bool) {
	i := 0
	for _, s := range sup {
		if i < len(sub) && sub[i] == s {
			i++
		}
	}
	if i == len(sub) {
		return -1, true
	}
	return i, false
}

// The footprint's order is DEFINED, not walked: the kernel defuns in it come
// in kernel load order, and the non-kernel seeds (primitives, user names,
// data symbols - the names with no row in the call graph) follow as a block.
//
// This is what replaced ygg.dl-walk-order, and it is what that walk could not
// satisfy: a depth-first walk from the seeds emits shen.a before cn before
// shen.string->byte, which is neither kernel load order nor any fixed order
// at all - it is an artefact of where the walk happened to start. Asserting
// the subsequence therefore rejects any re-introduction of a traversal here,
// not merely a changed golden.
func TestFootprintOrderIsKernelLoadOrder(t *testing.T) {
	host := checkHost(t)
	for _, prog := range shakingFixtures(t) {
		prog := prog
		t.Run(strings.TrimSuffix(filepath.Base(prog), ".shen"), func(t *testing.T) {
			rep, err := runFootprintsFull(prog, host)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.foot) == 0 || len(rep.kernel) == 0 {
				t.Fatalf("empty footprint (%d) or kernel order (%d)", len(rep.foot), len(rep.kernel))
			}

			inKernel := map[string]bool{}
			for _, k := range rep.kernel {
				inKernel[k] = true
			}

			// No name twice: the old walk kept a Seen list to guarantee
			// this, so the replacement has to guarantee it too.
			seen := map[string]bool{}
			for _, f := range rep.foot {
				if seen[f] {
					t.Errorf("footprint names %s twice", f)
				}
				seen[f] = true
			}

			var kernelPart, rest []string
			for _, f := range rep.foot {
				if inKernel[f] {
					kernelPart = append(kernelPart, f)
				} else {
					rest = append(rest, f)
				}
			}

			// Half one: the kernel defuns, in kernel load order.
			if at, ok := footprintSubsequence(kernelPart, rep.kernel); !ok {
				t.Errorf("the footprint's kernel defuns are not in kernel load order: "+
					"%s is out of place (%d of %d matched)", kernelPart[at], at, len(kernelPart))
			}

			// Half two: the non-kernel seeds are the tail, not interleaved.
			if len(rep.foot) != len(kernelPart)+len(rest) ||
				!equalStrings(rep.foot[:len(kernelPart)], kernelPart) {
				t.Errorf("the non-kernel seeds are interleaved with the kernel defuns "+
					"instead of following them: %d kernel, %d non-kernel, %d total",
					len(kernelPart), len(rest), len(rep.foot))
			}

			t.Logf("%d kernel defuns in load order + %d non-kernel seeds", len(kernelPart), len(rest))
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The computed-name rule warns, names the containing defun, records the
// same answer in both manifests, and does not refuse the program.
func TestComputedNameWarnsAndRecords(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()

	// shake() swallows host output when quiet; run it loudly so the WARN
	// line lands on this process's stderr and is visible in -v.
	if _, err := shake("tests/computed-name.shen", out, host, "sub", true); err != nil {
		t.Fatalf("a computed name must not refuse the shake: %v", err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txt), "computed-names=computed-call") {
		t.Errorf("manifest.txt must name the defun that computes a name:\n%s", txt)
	}
	sexp, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sexp), `("computed-names" "computed-call")`) {
		t.Errorf("sexp manifest must record the same:\n%s", sexp)
	}
}

// A fixture with no computed name says so explicitly rather than leaving
// the key out: a builder reading computed-names= should never have to guess
// whether the shake looked.
func TestComputedNameNoneIsRecorded(t *testing.T) {
	host := checkHost(t)
	out := t.TempDir()
	if _, err := shake("tests/fib.shen", out, host, "sub", true); err != nil {
		t.Fatal(err)
	}
	txt, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txt), "computed-names=none") {
		t.Errorf("manifest.txt must record computed-names=none:\n%s", txt)
	}
}

// The WARN sentinel is what a builder greps for; check the shaker really
// emits it, not just the manifest key.
func TestComputedNameWarnSentinel(t *testing.T) {
	host := checkHost(t)
	if host == nil {
		host = defaultHost()
	}
	root, err := yggRoot()
	if err != nil {
		t.Fatal(err)
	}
	prog, _ := filepath.Abs("tests/computed-name.shen")
	dir := t.TempDir()
	expr := fmt.Sprintf(`(yggdrasil.shake ["%s"] "%s")`, prog, dir)
	argv := append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	out, _ := runAt(wrapExecutable(argv), root)
	if !strings.Contains(out, "yggdrasil-shake: WARN computed-name in computed-call") {
		t.Errorf("shake must warn about the computed name:\n%s", out)
	}
}

// ---------------------------------------------------------------------
// D8's prose, not just D8's code.
//
// The set-versus-list glue was two reachability implementations wired
// together, and deleting it left four dead names: ygg.dl-walk-order,
// ygg.dl-covered?, ygg.dl-succs and ygg.dl-edge?. The failure this guards
// against is the one that actually happened - the code went, and the guide
// and the rules note went on describing the walk in the present tense, so a
// reader learned a mechanism that no longer exists. The same defect,
// relocated from code into prose.
//
// So: nothing in the Shen source may name them, and every paragraph of
// documentation that does must frame them as history. No host needed.

var deletedShakeInternals = []string{
	"ygg.dl-walk-order",
	"ygg.dl-covered?",
	"ygg.dl-succs",
	"ygg.dl-edge?",
}

// A paragraph naming a deleted function has to say, in the same breath, that
// it is gone. These are the ways the current text says it.
var historyMarkers = []string{
	"deleted",
	"no longer exist",
	"replaced",
	"Until this commit",
}

func TestDeletedShakeInternalsAreGoneFromTheSource(t *testing.T) {
	src, err := os.ReadFile("yggdrasil.shen")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range deletedShakeInternals {
		if strings.Contains(string(src), name) {
			t.Errorf("yggdrasil.shen still names %s: the D8 walk was deleted, "+
				"and ygg.rule-footprint's order is a value, not a traversal", name)
		}
	}
}

func TestDocsDoNotDescribeTheDeletedWalkAsLive(t *testing.T) {
	for _, doc := range docFiles(t) {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, para := range strings.Split(string(body), "\n\n") {
			for _, name := range deletedShakeInternals {
				if !strings.Contains(para, name) {
					continue
				}
				if !anyOf(para, historyMarkers) {
					t.Errorf("%s describes %s as if it still existed; it was "+
						"deleted with the D8 glue. Paragraph:\n%s",
						doc, name, strings.TrimSpace(para))
				}
			}
		}
	}
}

// The walk the guide used to describe did not always name the functions it
// was describing ("renders it as a list using the same depth-first walk over
// the same graph rows the old worklist used"), so naming alone is not enough
// of a net. Any prose about a depth-first walk in the footprint's ordering
// has to be historical too.
func TestDocsDoNotDescribeADepthFirstFootprintWalk(t *testing.T) {
	for _, doc := range docFiles(t) {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, para := range strings.Split(string(body), "\n\n") {
			if !strings.Contains(para, "depth-first walk") {
				continue
			}
			if !anyOf(para, historyMarkers) {
				t.Errorf("%s describes a depth-first walk as live; the footprint's "+
					"order is kernel load order, a value. Paragraph:\n%s",
					doc, strings.TrimSpace(para))
			}
		}
	}
}

// Section 12 of the guide is a claims table, and the row about the shake's
// output used to read "did not change under any of this work | checked".
// Deleting the D8 glue moved one line of kernel.kl on the eval-free
// fixtures, so that row has to carry the exception or it is a false claim
// with the word "checked" next to it.
func TestGuideByteIdentityClaimCarriesTheD8Exception(t *testing.T) {
	body, err := os.ReadFile("docs/verification-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "|") || !strings.Contains(line, "The shake's output") {
			continue
		}
		rows++
		if !strings.Contains(line, "D8") {
			t.Errorf("the claims-table row about the shake's output does not name "+
				"the D8 ordering exception:\n%s", line)
		}
	}
	if rows != 1 {
		t.Errorf("expected exactly one claims-table row about the shake's output, found %d", rows)
	}
}

// docFiles is the prose this package holds to the code: the guide, the design
// notes beside it, and the README.
func docFiles(t *testing.T) []string {
	t.Helper()
	docs, err := filepath.Glob("docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	return append(docs, "README.md")
}

func anyOf(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
