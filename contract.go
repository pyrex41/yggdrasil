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
//	unknown    the key is absent, OR it is present with the literal value
//	           "unknown" (which is what builders.json's `_default.port_reads`
//	           now holds). Absence looks like absence: the report prints the
//	           row rather than omitting it, because "we never measured this"
//	           and "this port has none" are different claims and only the
//	           first one is true here. An unknown fact may still carry a
//	           `_source` saying why it was never measured, and that prints.
//
// The report deliberately reads the raw JSON rather than the `builder` struct
// for everything but `port_reads`, which has a second reader and therefore may
// not have a second rule (see contractFactRow). A `<key>`/`<key>_source` pair
// added to builders.json tomorrow -- to a target block or to `_default` --
// shows up here the day it is added, with no edit to this file, which is the
// opposite of the failure above. contractRows scans BOTH blocks for exactly
// that reason: `_default` is now the canonical home of a fact no target states,
// so a generic reader that only walked the target's own keys would be blind to
// the half of the file this commit created.

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

// contractLegend is the report's key. It is a package-level value so a test
// can hold it to the same standard as the rows: it says what the three words
// mean and no more. It used to say "a named test fails when the fact drifts",
// which is a claim about the DIRECTION of the check, and the two checked_by
// strings in builders.json today do not have the same direction --
// native_overrides is compared against its source both ways, while go's
// port_reads is checked by building the pruned slice, where a wrong name added
// breaks the build but a name removed only prunes more conservatively and
// nothing fails. One word cannot carry that, so the word stops trying and
// points at the string that can.
var contractLegend = []string{
	"verified = checked_by names a test; read it for what the test catches and in which direction",
	"declared = stated, nothing checks it",
	"unknown = not declared, or declared as the literal \"unknown\" -- nobody measured it; read the source for why",
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

	builders, defaultsB, err := parseBuilders()
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	b := builders[*target]

	out := os.Stdout
	fmt.Fprintf(out, "yggdrasil-contract: target=%s\n", *target)
	for _, line := range contractLegend {
		fmt.Fprintln(out, "  "+line)
	}

	// Level 0 is not a declaration: it is the recipe, and it either exists
	// or the target would not be in the file.
	fmt.Fprintf(out, "%-7s %-18s %-10s %s\n", "level0", "builder", "ok",
		fmt.Sprintf("runs on %s, %s, needs %s",
			b.RunImpl, plural(len(b.Build), "build step"), joinOr(b.Needs, "nothing")))

	for _, r := range contractRows(block, defaults, b, defaultsB) {
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
		if r.status != "unknown" || r.checkedBy != "" {
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
// then any other `<key>`/`<key>_source` pair the file has grown since -- in
// the target's own block OR in `_default`. Scanning only the target's block
// would miss every fact stated once for everybody, which is the shape this
// commit just gave the file: a new `_default` fact would be inherited by all
// fourteen targets and reported by none of them.
func contractRows(block, defaults map[string]json.RawMessage, b, defaultsB builder) []contractRow {
	var rows []contractRow
	seen := map[string]bool{}
	for _, k := range contractKeys {
		seen[k] = true
		rows = append(rows, contractFactRow(k, block, defaults, b, defaultsB))
	}
	extraSeen := map[string]bool{}
	var extra []string
	for _, src := range []map[string]json.RawMessage{block, defaults} {
		for k := range src {
			if strings.HasPrefix(k, "_") || seen[k] || extraSeen[k] || isFactAttribute(k, seen) {
				continue
			}
			if _, ok := src[k+"_source"]; !ok {
				continue // a recipe key (run, build, needs), not a declared fact
			}
			extraSeen[k] = true
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		rows = append(rows, contractFactRow(k, block, defaults, b, defaultsB))
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

// contractFactRow resolves one fact for one target and renders its row.
//
// The inheritance rule is NOT re-implemented here. `port_reads` has a second
// reader -- effectivePortReads in prune.go, the function that decides what
// --prune-init actually drops -- and a report that resolved it by its own rule
// could print the opposite of what the shaker will do. It did: key-presence
// and `len(b.PortReads) > 0` differ on a declared-empty list, so
// `"port_reads": []` read as "declared, 0 entries" here and as "inherits the
// 35-name default" there, from one key. So this function calls
// effectivePortReads and reports what it returns. Every other fact has exactly
// one reader, this report, and for those presence is the rule and a declared
// empty list is a real declaration ("this port has none") rather than a
// silence.
func contractFactRow(key string, block, defaults map[string]json.RawMessage, b, defaultsB builder) contractRow {
	r := contractRow{key: key, status: "unknown", summary: "not declared in builders.json"}

	src := block
	v, ok := block[key]
	if !ok && defaults != nil {
		if v, ok = defaults[key]; ok {
			r.inherited, src = true, defaults
		}
	}
	if key == "port_reads" {
		// One rule, one place. Note the empty-but-present case explicitly
		// rather than letting the row read as if the target said nothing at
		// all: it said something, and the shaker declined to hear it.
		own := len(b.PortReads) > 0
		if _, present := block[key]; present && !own {
			r.notes = append(r.notes, "note:       this target's own port_reads is an empty list, "+
				"which effectivePortReads (prune.go) reads as declaring none -- the default applies")
		}
		eff, known := effectivePortReads(b, defaultsB)
		if !known {
			// UNKNOWN is a value here, not a missing row. The `_default`
			// block says so in as many words ("unknown"), and its source
			// says why, so both are printed: "we never measured this port"
			// and "this port reads nothing natively" are different claims
			// and only the first is being made.
			r.inherited = !own
			r.summary = portReadsUnknown + ": nobody has read this port's native global reads off its runtime"
			if r.inherited {
				src = defaults
			}
			r.source = jsonString(src[key+"_source"])
			r.checkedBy = jsonString(src[key+"_checked_by"])
			r.notes = append(r.notes, "consequence: --prune-init --target "+
				"<this target> is refused (prune.go); a target-agnostic --prune-init prunes "+
				"against the union over the targets that HAVE declared a list, and says so")
			return r
		}
		r.inherited = !own
		if r.inherited {
			src = defaults
		} else {
			src = block
		}
		r.summary = describeStrings(eff)
		r.source = jsonString(src[key+"_source"])
		r.checkedBy = jsonString(src[key+"_checked_by"])
		if factChecked(r.checkedBy) && own {
			r.status = "verified"
		} else {
			r.status = "declared"
		}
		return r
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
		return describeStrings(list)
	}
	if s := jsonString(v); s != "" {
		return s
	}
	return strings.TrimSpace(string(v))
}

// describeStrings summarises a list fact: a count, plus the names themselves
// when there are few enough to read at a glance.
func describeStrings(list []string) string {
	if len(list) == 0 {
		return "0 entries (declared empty)"
	}
	if len(list) <= 4 {
		return fmt.Sprintf("%s (%s)", plural(len(list), "entry"), strings.Join(list, ", "))
	}
	return plural(len(list), "entry")
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
