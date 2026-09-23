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
	"fmt"
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

// compareRelations is the comparison itself, as a pure function over three
// already-parsed lists. It exists as a function so that the drift test and the
// negative control below run THE SAME code: a control that re-derives its own
// set difference proves something about arithmetic, not about the comparison
// that ships.
//
// analysis.dl is the authority: it is the file the rules live in, and the
// other two exist to follow it. factFiles is main.go's factRelations, which is
// a list of FILE bases (it stats <entry>.facts after a dump), not of relation
// names -- the two differ for `readGlobal`, and carrying both halves is how
// this checks the mapping instead of reporting it as drift.
func compareRelations(dl, py []relation, factFiles []string) []string {
	var problems []string
	say := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	dlByName := map[string]relation{}
	for _, r := range dl {
		if prev, dup := dlByName[r.name]; dup {
			say("analysis/analysis.dl declares .input %s twice (%s and %s)", r.name, prev, r)
		}
		dlByName[r.name] = r
	}

	pyByName := map[string]relation{}
	for _, r := range py {
		pyByName[r.name] = r
	}
	for _, want := range sortedRelations(dlByName) {
		got, ok := pyByName[want.name]
		if !ok {
			say("analysis/refeval.py INPUTS is missing %q: analysis.dl declares "+
				"`.input %s`, so refeval.py evaluates the rules against an EMPTY %s "+
				"and silently returns a smaller answer. Add %q to INPUTS with its arity.",
				want.name, want.name, want.name, want.name)
			continue
		}
		if got.file != want.file {
			say("relation %s: analysis.dl reads %s.facts, refeval.py reads %s.facts. "+
				"Fix FILENAMES in analysis/refeval.py or the filename= on the .input.",
				want.name, want.file, got.file)
		}
	}
	for _, r := range sortedRelations(pyByName) {
		if _, ok := dlByName[r.name]; !ok {
			say("analysis/refeval.py INPUTS carries %q, which analysis/analysis.dl "+
				"declares no `.input` for. One of the two is stale.", r.name)
		}
	}

	wantFiles := map[string]relation{}
	for _, r := range dl {
		wantFiles[r.file] = r
	}
	gotFiles := map[string]bool{}
	for _, f := range factFiles {
		if gotFiles[f] {
			say("main.go factRelations lists %q twice", f)
		}
		gotFiles[f] = true
	}
	for _, r := range sortedRelations(wantFiles) {
		if !gotFiles[r.file] {
			say("main.go factRelations is missing %q: analysis.dl declares "+
				"`.input %s`, so a dump that failed to write %s.facts would go "+
				"unnoticed and Souffle would then error on the missing file. "+
				"Add %q to factRelations.", r.file, r.name, r.file, r.file)
		}
	}
	var extraFiles []string
	for file := range gotFiles {
		if _, ok := wantFiles[file]; !ok {
			extraFiles = append(extraFiles, file)
		}
	}
	sort.Strings(extraFiles)
	for _, file := range extraFiles {
		say("main.go factRelations carries %q, which no `.input` in "+
			"analysis/analysis.dl corresponds to. One of the two is stale.", file)
	}
	return problems
}

// sortedRelations gives the map a stable order, so a failing run reports the
// same problems in the same sequence every time.
func sortedRelations(m map[string]relation) []relation {
	var out []relation
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// TestRelationListsAgree is the drift test. Every relation analysis.dl
// declares an `.input` for must appear in refeval.py's INPUTS and in main.go's
// factRelations, and neither of those may carry a relation the .dl does not.
func TestRelationListsAgree(t *testing.T) {
	dl := readRelationsFromDL(t)
	py := readRelationsFromRefeval(t)

	problems := compareRelations(dl, py, factRelations)
	for _, p := range problems {
		t.Error(p)
	}
	if len(problems) == 0 {
		t.Logf("%d relations, identical across analysis.dl, refeval.py and factRelations",
			len(dl))
	}
}

// TestRelationListsAgree is only a drift test if it can actually see drift.
// This is the negative control, and it runs compareRelations -- the function
// the real test runs -- over copies of the real lists with one relation
// removed from each follower in turn. If a parser stopped matching, or the
// comparison stopped comparing, the lists would be empty or equal-by-vacuity
// and these injected omissions would go unreported.
func TestRelationListsDriftIsDetected(t *testing.T) {
	dl := readRelationsFromDL(t)
	py := readRelationsFromRefeval(t)
	if len(dl) < 2 {
		t.Fatalf("analysis.dl parsed as %d relations; the parser is broken", len(dl))
	}
	if problems := compareRelations(dl, py, factRelations); len(problems) != 0 {
		t.Skipf("the lists already disagree; TestRelationListsAgree is the test that "+
			"reports that, and this control cannot inject drift into a broken baseline: %v",
			problems)
	}

	// Case 1: a relation reaches analysis.dl but not refeval.py -- the quiet
	// failure, an empty relation and a silently smaller footprint.
	drop := dl[len(dl)-1]
	shortPy := make([]relation, 0, len(py))
	for _, r := range py {
		if r.name != drop.name {
			shortPy = append(shortPy, r)
		}
	}
	if len(shortPy) != len(py)-1 {
		t.Fatalf("removing %q from the refeval.py list removed %d entries, not 1",
			drop.name, len(py)-len(shortPy))
	}
	problems := compareRelations(dl, shortPy, factRelations)
	if !anyContains(problems, drop.name) || !anyContains(problems, "refeval.py") {
		t.Errorf("dropping %q from refeval.py's INPUTS produced %v; the comparison must "+
			"name the missing relation and the list it is missing from", drop.name, problems)
	}

	// Case 2: the same relation missing from main.go's factRelations.
	shortFacts := make([]string, 0, len(factRelations))
	for _, f := range factRelations {
		if f != drop.file {
			shortFacts = append(shortFacts, f)
		}
	}
	if len(shortFacts) != len(factRelations)-1 {
		t.Fatalf("removing %q from factRelations removed %d entries, not 1",
			drop.file, len(factRelations)-len(shortFacts))
	}
	problems = compareRelations(dl, py, shortFacts)
	if !anyContains(problems, drop.file) || !anyContains(problems, "factRelations") {
		t.Errorf("dropping %q from factRelations produced %v; the comparison must name the "+
			"missing fact file and the list it is missing from", drop.file, problems)
	}

	// Case 3: a stale entry in a follower that the .dl never declared. Drift
	// has two directions and a one-directional control would miss one.
	problems = compareRelations(dl, append(append([]relation(nil), py...),
		relation{name: "ghostRelation", file: "ghostRelation"}), factRelations)
	if !anyContains(problems, "ghostRelation") {
		t.Errorf("an INPUTS entry with no `.input` in analysis.dl went unreported: %v", problems)
	}
}

func anyContains(xs []string, sub string) bool {
	for _, x := range xs {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}
