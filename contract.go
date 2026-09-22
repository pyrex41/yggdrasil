package main

// `yggdrasil contract --target T`: the level-1 half of the conformance report
// docs/port-contract.md describes, over the facts builders.json actually
// carries.
//
// Why this file exists at all. builders.json grew fact keys that no code read:
// `native_overrides` had 54 entries, a provenance string and a `_verified:
// true`, and `grep -rn native_overrides *.go *.shen` returned nothing. A key
// with no consumer is not data, it is a note in a data file -- nothing can
// contradict it, so nothing keeps it true. (It was not true: the list was four
// symbols short of what InstallKernelFast rebinds.) This report is the
// consumer. It reads the facts, it prints where each came from, and it prints
// whether anything checks it.
//
// The three statuses, and the only definitions of them in this repo:
//
//	verified   `<key>_checked_by` names a test. That test failing is what
//	           stops the fact from drifting. Nothing else earns this word.
//	declared   the fact is stated, with or without a source, and no test
//	           checks it. A port's declaration, taken on trust.
//	unknown    the key is absent. Absence looks like absence: the report
//	           prints the row rather than omitting it, because "we never
//	           measured this" and "this port has none" are different claims
//	           and only the first one is true here.
//
// The report deliberately reads the raw JSON rather than the `builder` struct
// for everything but the two facts that have typed consumers. A fact key added
// to builders.json tomorrow shows up here the day it is added, with no edit to
// this file -- which is the opposite of the failure above.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// factChecked reports whether a `_checked_by` string names a test. "none" and
// "" both mean nothing checks the fact; the spelling is "none" in the file so
// that a missing key and a deliberate "nobody checks this" look different to a
// reader, and the same to this code.
func factChecked(checkedBy string) bool {
	s := strings.TrimSpace(checkedBy)
	return s != "" && !strings.EqualFold(s, "none")
}

// factSourced reports whether a `_source` string says anything at all. A bare
// "none" does not; "none: <why there is no source>" does, and prints, because
// the reason a fact was never read off a runtime is the useful half.
func factSourced(source string) bool {
	s := strings.TrimSpace(source)
	return s != "" && !strings.EqualFold(s, "none")
}

// contractKeys are the level-1 self-description facts of docs/port-contract.md,
// in that document's table order. Every one is printed for every target, even
// the four that no target declares yet: the report's job is to say which rungs
// were climbed AND which were declared away, and a silently omitted row says
// neither.
var contractKeys = []string{
	"port_reads",
	"port_writes",
	"special_forms",
	"native_overrides",
	"native_deps",
	"call_style",
	"dispatch",
}

// contractRow is one fact's line in the report.
type contractRow struct {
	key       string
	status    string // verified | declared | unknown
	summary   string // "5 entries", "full-kernel", "not declared in builders.json"
	source    string
	checkedBy string
	notes     []string // extra provenance lines, e.g. the override phase
	inherited bool     // the value came from the _default block, not the target
}

func cmdContract(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil contract", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	target := fs.String("target", "", "the port whose declaration to report on (required)")
	if err := fs.Parse(reorderArgs(rest, "target")); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil contract --target T")
		fmt.Fprintln(os.Stderr, "       (yggdrasil targets lists the targets)")
		return 2
	}

	raw, err := rawBuilders()
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	block, ok := raw[*target]
	if !ok || strings.HasPrefix(*target, "_") {
		fmt.Fprintf(os.Stderr, "yggdrasil: unknown target %q\n", *target)
		return 2
	}
	defaults := raw[builderDefaultsKey]

	builders, err := loadBuilders()
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	b := builders[*target]

	out := os.Stdout
	fmt.Fprintf(out, "yggdrasil-contract: target=%s\n", *target)
	fmt.Fprintln(out, "  verified = a named test fails when the fact drifts;"+
		" declared = stated, nothing checks it; unknown = not declared")

	// Level 0 is not a declaration: it is the recipe, and it either exists
	// or the target would not be in the file.
	fmt.Fprintf(out, "%-7s %-18s %-10s %s\n", "level0", "builder", "ok",
		fmt.Sprintf("runs on %s, %s, needs %s",
			b.RunImpl, plural(len(b.Build), "build step"), joinOr(b.Needs, "nothing")))

	for _, r := range contractRows(block, defaults, b) {
		fmt.Fprintf(out, "%-7s %-18s %-10s %s\n", "level1", r.key, r.status, r.summary)
		if r.inherited {
			fmt.Fprintf(out, "%-38s%s\n", "", "inherited: builders.json "+builderDefaultsKey+
				" (this target declares none of its own)")
		}
		if factSourced(r.source) {
			fmt.Fprintf(out, "%-38s%s\n", "", "source:     "+r.source)
		} else if r.status != "unknown" {
			fmt.Fprintf(out, "%-38s%s\n", "", "source:     none")
		}
		if r.status != "unknown" {
			checked := r.checkedBy
			if !factChecked(checked) {
				checked = "none"
			}
			fmt.Fprintf(out, "%-38s%s\n", "", "checked_by: "+checked)
		}
		for _, n := range r.notes {
			fmt.Fprintf(out, "%-38s%s\n", "", n)
		}
	}

	// Levels 2 to 4 are commands, not declarations. Naming them (and the
	// fact that this report does not run them) is more honest than a table
	// with five rows that only ever says "ok".
	fmt.Fprintln(out, "level2  trace              not-run    yggdrasil trace-check")
	fmt.Fprintln(out, "level3  extractor          not-run    yggdrasil scip-check (go only)")
	fmt.Fprintln(out, "level4  parity             not-run    yggdrasil parity")
	return 0
}

// contractRows builds the level-1 rows: the documented keys in table order,
// then any other `<key>`/`<key>_source` pair the file has grown since.
func contractRows(block, defaults map[string]json.RawMessage, b builder) []contractRow {
	var rows []contractRow
	seen := map[string]bool{}
	for _, k := range contractKeys {
		seen[k] = true
		rows = append(rows, contractFactRow(k, block, defaults, b))
	}
	var extra []string
	for k := range block {
		if strings.HasPrefix(k, "_") || seen[k] || isFactAttribute(k, seen) {
			continue
		}
		if _, ok := block[k+"_source"]; !ok {
			continue // a recipe key (run, build, needs), not a declared fact
		}
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		rows = append(rows, contractFactRow(k, block, defaults, b))
	}
	return rows
}

// isFactAttribute reports whether a key is provenance ABOUT another fact
// rather than a fact of its own: `port_reads_source`, `native_overrides_
// installed_after` and the like. They are printed under their fact's row, so
// listing them again as facts would double-count the declaration.
func isFactAttribute(key string, facts map[string]bool) bool {
	if strings.HasSuffix(key, "_source") || strings.HasSuffix(key, "_checked_by") {
		return true
	}
	for f := range facts {
		if strings.HasPrefix(key, f+"_") {
			return true
		}
	}
	return false
}

func contractFactRow(key string, block, defaults map[string]json.RawMessage, b builder) contractRow {
	r := contractRow{key: key, status: "unknown", summary: "not declared in builders.json"}

	src := block
	v, ok := block[key]
	if !ok && defaults != nil {
		if v, ok = defaults[key]; ok {
			r.inherited, src = true, defaults
		}
	}
	if !ok {
		return r
	}
	r.summary = describeFact(v)
	r.source = jsonString(src[key+"_source"])
	r.checkedBy = jsonString(src[key+"_checked_by"])
	if factChecked(r.checkedBy) {
		r.status = "verified"
	} else {
		r.status = "declared"
	}

	// The phase is the content of the native_overrides fact, not a footnote:
	// shen-go installs the natives AFTER shen.initialise, so the kernel's KL
	// bodies do run during boot. Printing the list without the phase would
	// read as "these defuns never execute", which is false.
	if key == "native_overrides" {
		phase := b.NativeOverridesInstalledAfter
		if phase == "" {
			phase = jsonString(src["native_overrides_installed_after"])
		}
		if phase == "" {
			r.notes = append(r.notes, "installed_after: UNKNOWN -- the declaration does not say "+
				"when the natives replace the KL bodies, so it does not say whether the KL runs")
		} else {
			r.summary += ", installed_after=" + phase
			r.notes = append(r.notes, "phase:      the natives replace these KL bodies only after "+
				phase+", so whatever the boot reached before that point ran as KL")
			if s := jsonString(src["native_overrides_installed_after_source"]); s != "" {
				r.notes = append(r.notes, "phase src:  "+s)
			}
		}
	}
	return r
}

// describeFact summarises a fact value for the status line: a count for a
// list, the value itself for a scalar.
func describeFact(v json.RawMessage) string {
	var list []string
	if err := json.Unmarshal(v, &list); err == nil {
		if len(list) == 0 {
			return "0 entries (declared empty)"
		}
		if len(list) <= 4 {
			return fmt.Sprintf("%s (%s)", plural(len(list), "entry"), strings.Join(list, ", "))
		}
		return plural(len(list), "entry")
	}
	if s := jsonString(v); s != "" {
		return s
	}
	return strings.TrimSpace(string(v))
}

func jsonString(v json.RawMessage) string {
	var s string
	if len(v) == 0 || json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

// plural renders a count with its noun, because "1 build steps" in a report
// about honesty is a small lie about how much care went into the report.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	if strings.HasSuffix(noun, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(noun, "y"))
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func joinOr(xs []string, empty string) string {
	if len(xs) == 0 {
		return empty
	}
	return strings.Join(xs, ", ")
}

// rawBuilders returns builders.json as unparsed per-key blocks, `_`-prefixed
// keys included. The report needs the `_default` block and needs to tell an
// absent key from a zero value, neither of which survives unmarshalling into
// the `builder` struct.
func rawBuilders() (map[string]map[string]json.RawMessage, error) {
	b, err := embedded.ReadFile("builders.json")
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, err
	}
	out := map[string]map[string]json.RawMessage{}
	for k, v := range top {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(v, &block); err != nil {
			continue // _comment is a list of strings, not a block
		}
		out[k] = block
	}
	return out, nil
}
