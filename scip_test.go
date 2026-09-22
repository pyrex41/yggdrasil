package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- a hand-made SCIP index, so the decoder and the reachability walk are
// tested without a toolchain. The encoder below writes the same wire format
// scip-go writes, for the five fields scip.go reads.

func pbTag(num, wire int) []byte {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], uint64(num<<3|wire))
	return b[:n]
}

func pbVarint(v uint64) []byte {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], v)
	return b[:n]
}

func pbLen(num int, payload []byte) []byte {
	out := pbTag(num, 2)
	out = append(out, pbVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func pbPackedInt32(num int, vs ...int32) []byte {
	var body []byte
	for _, v := range vs {
		body = append(body, pbVarint(uint64(v))...)
	}
	return pbLen(num, body)
}

func pbOccurrence(rng []int32, sym string, roles int32, encl []int32) []byte {
	var b []byte
	b = append(b, pbPackedInt32(1, rng...)...)
	b = append(b, pbLen(2, []byte(sym))...)
	if roles != 0 {
		b = append(b, pbTag(3, 0)...)
		b = append(b, pbVarint(uint64(roles))...)
	}
	if len(encl) > 0 {
		b = append(b, pbPackedInt32(7, encl...)...)
	}
	return b
}

// fixtureIndex is three functions in one document: main calls helper, helper
// calls nothing, and dead is defined but never referenced.
func fixtureIndex() []byte {
	const (
		mainSym   = "scip-go gomod ex . `ex`/main()."
		helperSym = "scip-go gomod ex . `ex`/helper()."
		deadSym   = "scip-go gomod ex . `ex`/dead()."
		otherSym  = "scip-go gomod ex . `ex`/Printf()."
	)
	var doc []byte
	doc = append(doc, pbLen(1, []byte("main.go"))...)
	add := func(o []byte) { doc = append(doc, pbLen(2, o)...) }
	add(pbOccurrence([]int32{0, 5, 0, 9}, mainSym, scipRoleDefinition, []int32{0, 0, 5, 1}))
	add(pbOccurrence([]int32{2, 2, 2, 8}, helperSym, 0, nil))
	add(pbOccurrence([]int32{3, 2, 3, 8}, otherSym, 0, nil))
	add(pbOccurrence([]int32{7, 5, 7, 11}, helperSym, scipRoleDefinition, []int32{7, 0, 9, 1}))
	add(pbOccurrence([]int32{11, 5, 11, 9}, deadSym, scipRoleDefinition, []int32{11, 0, 13, 1}))
	return pbLen(2, doc)
}

func TestDecodeSCIPIndexAndReachability(t *testing.T) {
	idx, err := decodeSCIPIndex(fixtureIndex())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(idx.Docs) != 1 {
		t.Fatalf("documents = %d, want 1", len(idx.Docs))
	}
	if idx.Docs[0].Path != "main.go" {
		t.Fatalf("path = %q", idx.Docs[0].Path)
	}
	if len(idx.Docs[0].Occs) != 5 {
		t.Fatalf("occurrences = %d, want 5", len(idx.Docs[0].Occs))
	}

	g, err := buildFuncGraph(idx, "")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (main, helper, dead)", len(g.Nodes))
	}
	if !strings.HasSuffix(g.Main, "main().") {
		t.Fatalf("main node = %q", g.Main)
	}
	reach := reachableFrom(g, g.Main)
	if len(reach) != 2 {
		t.Fatalf("reachable = %v, want main and helper only", sortedKeys(reach))
	}
	for _, sym := range sortedKeys(reach) {
		if strings.Contains(sym, "dead") {
			t.Fatalf("dead function is reachable: %v", sortedKeys(reach))
		}
	}
	// A reference to a symbol with no definition in this index (Printf) is an
	// edge the walk must drop rather than invent a node for.
	if _, ok := g.Nodes["scip-go gomod ex . `ex`/Printf()."]; ok {
		t.Fatal("a reference-only symbol became a node")
	}
}

func TestLastDescriptor(t *testing.T) {
	for in, want := range map[string]string{
		"scip-go gomod ex . `ex`/main().":     "main",
		"scip-go gomod ex . `ex`/shen#Foo().": "Foo",
		"main":                                "main",
	} {
		if got := lastDescriptor(in); got != want {
			t.Errorf("lastDescriptor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsFunctionSymbol(t *testing.T) {
	if isFunctionSymbol("local 3") {
		t.Error("a local symbol is not a function")
	}
	if isFunctionSymbol("scip-go gomod ex . `ex`/Thing#") {
		t.Error("a type symbol is not a function")
	}
	if !isFunctionSymbol("scip-go gomod ex . `ex`/main().") {
		t.Error("a method descriptor is a function")
	}
}

// ---- the host-gated end-to-end check ----

// TestScipCheckFixtures runs the real subcommand over two fixtures. It skips
// -- never fails -- when anything it does not own is missing: the Shen host,
// the Go toolchain, scip-go, or a sibling shen-go whose stage-2 builder
// cannot build the module here.
func TestScipCheckFixtures(t *testing.T) {
	if os.Getenv("YGGDRASIL_HOST") == "" && defaultHost() == nil {
		t.Skip("no Shen host: set $YGGDRASIL_HOST")
	}
	for _, tool := range []string{"go", "scip-go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	bin := filepath.Join(t.TempDir(), "yggdrasil")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the CLI: %v\n%s", err, out)
	}
	for _, fixture := range []string{"fib", "hello"} {
		t.Run(fixture, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), fixture)
			cmd := exec.Command(bin, "scip-check", filepath.Join("tests", fixture+".shen"), dir, "--target", "go")
			out, err := cmd.CombinedOutput()
			s := string(out)
			if strings.Contains(s, "stage 2:") || strings.Contains(s, "SKIP") {
				t.Skipf("stage 2 could not build here:\n%s", lastLines(s, 6))
			}
			if err != nil {
				t.Fatalf("scip-check: %v\n%s", err, lastLines(s, 20))
			}
			if !strings.Contains(s, "yggdrasil-scip-check: OK ") {
				t.Fatalf("no OK line:\n%s", lastLines(s, 20))
			}
			// The check must say which path produced the verdict.
			if !strings.Contains(s, "path=scip") && !strings.Contains(s, "path=go-ast") {
				t.Fatalf("no path= line:\n%s", lastLines(s, 20))
			}
		})
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
