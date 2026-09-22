package main

// The fact-relation list exists three times, by hand:
//
//	analysis/analysis.dl   `.decl R(...)` + `.input R` (+ an optional
//	                       filename= override) -- what Souffle reads
//	analysis/refeval.py    INPUTS (name -> arity) + FILENAMES -- what the
//	                       Python reference evaluator reads
//	main.go                factRelations -- the files `yggdrasil facts`
//	                       insists the dump produced
//
// main.go's comment said "keep the two in step". There are three, and until
// this file nothing checked any pair of them. The failure mode is quiet in
// exactly the way that matters: a relation added to analysis.dl but not to
// refeval.py makes the Python engine evaluate the rules against an EMPTY
// relation, which does not error -- it silently shrinks the footprint, and a
// shrunk footprint is a slice missing a function. A relation added to the .dl
// but not to factRelations loses the "the dump is missing R.facts" guard.
//
// So: one test, all three lists, a set comparison, and a message that names
// which list is short and which relation it is short by. No host and no
// Souffle -- it is three text files and a string compare.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A relation has a NAME (what the rules refer to) and a FILE base (what the
// dump writes). They differ for exactly one relation today -- `.input
// readGlobal(filename="readglobal.facts")` -- and that difference is the
// reason a naive name-to-name comparison would report a false drift. Carrying
// both means the test checks the mapping too.
type relation struct {
	name string // the Datalog relation name
	file string // the fact file's base name, without ".facts"
}

func (r relation) String() string {
	if r.name == r.file {
		return r.name
	}
	return r.name + " (in " + r.file + ".facts)"
}

// dlInputRe matches `.input R` and `.input R(filename="R.facts")`, which are
// the only two forms analysis.dl uses.
var dlInputRe = regexp.MustCompile(`^\s*\.input\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(\s*filename\s*=\s*"([^"]*)"\s*\))?\s*$`)

// pyInputRe matches one `"name": arity,` entry of refeval.py's INPUTS dict.
var pyInputRe = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*\d+\s*,?\s*$`)

// pyFilenameRe matches one `"name": "file"` pair of refeval.py's FILENAMES.
var pyFilenameRe = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]+)"`)

func readRelationsFromDL(t *testing.T) []relation {
	t.Helper()
	b, err := os.ReadFile("analysis/analysis.dl")
	if err != nil {
		t.Fatalf("reading analysis/analysis.dl: %v", err)
	}
	var out []relation
	for _, line := range strings.Split(string(b), "\n") {
		m := dlInputRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		r := relation{name: m[1], file: m[1]}
		if m[2] != "" {
			r.file = strings.TrimSuffix(m[2], ".facts")
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		t.Fatal("analysis/analysis.dl: found no `.input` declarations at all -- " +
			"either the file moved or dlInputRe no longer matches its syntax")
	}
	return out
}

func readRelationsFromRefeval(t *testing.T) []relation {
	t.Helper()
	b, err := os.ReadFile("analysis/refeval.py")
	if err != nil {
		t.Fatalf("reading analysis/refeval.py: %v", err)
	}
	src := string(b)

	body, ok := sliceBetween(src, "INPUTS = {", "}")
	if !ok {
		t.Fatal("analysis/refeval.py: no `INPUTS = {...}` dict found")
	}
	files := map[string]string{}
	if fb, ok := sliceBetween(src, "FILENAMES = {", "}"); ok {
		for _, m := range pyFilenameRe.FindAllStringSubmatch(fb, -1) {
			files[m[1]] = m[2]
		}
	}

	var out []relation
	for _, line := range strings.Split(body, "\n") {
		m := pyInputRe.FindStringSubmatch(line)
		if m == nil {
			continue // a comment or a blank line
		}
		r := relation{name: m[1], file: m[1]}
		if f, ok := files[m[1]]; ok {
			r.file = strings.TrimSuffix(f, ".facts")
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		t.Fatal("analysis/refeval.py: INPUTS parsed as empty -- either the dict moved " +
			"or its entries no longer look like `\"name\": arity,`")
	}
	return out
}

// sliceBetween returns the text between the first occurrence of open and the
// next occurrence of close after it.
func sliceBetween(src, open, shut string) (string, bool) {
	i := strings.Index(src, open)
	if i < 0 {
		return "", false
	}
	rest := src[i+len(open):]
	j := strings.Index(rest, shut)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// TestRelationListsAgree is the drift test. Every relation analysis.dl
// declares an `.input` for must appear in refeval.py's INPUTS and in main.go's
// factRelations, and neither of those may carry a relation the .dl does not.
func TestRelationListsAgree(t *testing.T) {
	dl := readRelationsFromDL(t)
	py := readRelationsFromRefeval(t)

	// analysis.dl is the authority: it is the file the rules live in, and
	// the other two exist to follow it.
	dlByName := map[string]relation{}
	for _, r := range dl {
		if prev, dup := dlByName[r.name]; dup {
			t.Errorf("analysis/analysis.dl declares .input %s twice (%s and %s)", r.name, prev, r)
		}
		dlByName[r.name] = r
	}

	pyByName := map[string]relation{}
	for _, r := range py {
		pyByName[r.name] = r
	}
	for name, want := range dlByName {
		got, ok := pyByName[name]
		if !ok {
			t.Errorf("analysis/refeval.py INPUTS is missing %q: analysis.dl declares "+
				"`.input %s`, so refeval.py evaluates the rules against an EMPTY %s "+
				"and silently returns a smaller answer. Add %q to INPUTS with its arity.",
				name, name, name, name)
			continue
		}
		if got.file != want.file {
			t.Errorf("relation %s: analysis.dl reads %s.facts, refeval.py reads %s.facts. "+
				"Fix FILENAMES in analysis/refeval.py or the filename= on the .input.",
				name, want.file, got.file)
		}
	}
	for name := range pyByName {
		if _, ok := dlByName[name]; !ok {
			t.Errorf("analysis/refeval.py INPUTS carries %q, which analysis/analysis.dl "+
				"declares no `.input` for. One of the two is stale.", name)
		}
	}

	// main.go's list is of FILE bases: it stats <entry>.facts after a dump.
	wantFiles := map[string]relation{}
	for _, r := range dl {
		wantFiles[r.file] = r
	}
	gotFiles := map[string]bool{}
	for _, f := range factRelations {
		if gotFiles[f] {
			t.Errorf("main.go factRelations lists %q twice", f)
		}
		gotFiles[f] = true
	}
	for file, r := range wantFiles {
		if !gotFiles[file] {
			t.Errorf("main.go factRelations is missing %q: analysis.dl declares "+
				"`.input %s`, so a dump that failed to write %s.facts would go "+
				"unnoticed and Souffle would then error on the missing file. "+
				"Add %q to factRelations.", file, r.name, file, file)
		}
	}
	for file := range gotFiles {
		if _, ok := wantFiles[file]; !ok {
			t.Errorf("main.go factRelations carries %q, which no `.input` in "+
				"analysis/analysis.dl corresponds to. One of the two is stale.", file)
		}
	}

	if !t.Failed() {
		t.Logf("%d relations, identical across analysis.dl, refeval.py and factRelations",
			len(dl))
	}
}

// TestRelationListsAgree is only a drift test if it can actually see drift.
// This runs the same comparison over a deliberately short copy of each list
// and asserts it complains -- otherwise a parser that silently matched nothing
// (a renamed file, a changed `.input` syntax) would read as a pass forever.
func TestRelationListsDriftIsDetected(t *testing.T) {
	dl := readRelationsFromDL(t)
	py := readRelationsFromRefeval(t)
	if len(dl) < 2 {
		t.Fatalf("analysis.dl parsed as %d relations; the parser is broken", len(dl))
	}

	names := func(rs []relation) map[string]bool {
		m := map[string]bool{}
		for _, r := range rs {
			m[r.name] = true
		}
		return m
	}
	dlNames, pyNames := names(dl), names(py)

	// Drop one relation from the Python side and check the comparison the
	// real test performs would notice.
	dropped := dl[len(dl)-1].name
	short := map[string]bool{}
	for n := range pyNames {
		if n != dropped {
			short[n] = true
		}
	}
	missing := 0
	for n := range dlNames {
		if !short[n] {
			missing++
		}
	}
	if missing != 1 {
		t.Errorf("dropping %q from the refeval.py side should leave exactly one relation "+
			"unmatched, got %d -- the comparison in TestRelationListsAgree would not "+
			"catch a missed relation", dropped, missing)
	}

	// And that the parsers agree on a real, non-empty list rather than both
	// returning nothing.
	if len(dl) != len(py) {
		var only []string
		for n := range dlNames {
			if !pyNames[n] {
				only = append(only, n)
			}
		}
		sort.Strings(only)
		t.Errorf("analysis.dl has %d relations, refeval.py has %d (only in the .dl: %v)",
			len(dl), len(py), only)
	}
}
