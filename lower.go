package main

// `yggdrasil lower` and `yggdrasil lower-check`: port-specific lowering as a
// separate stage after the canonical shake, and the three-way parity that is
// the evidence for it.
//
// What lowering is here. A port that rebinds a kernel function to a native
// under the SAME name has already performed the substitution the design note
// (docs/lowering.md) describes; what is left for Yggdrasil to do is stop
// shipping the KL body that the rebinding will replace. So "lowering F" is
// deleting F's defun from kernel.kl and nothing else. No call site moves, no
// form is rewritten, no name is introduced. That is the whole pass, and it is
// why its correctness check is one line: every name deleted must be a name the
// target declared (`badLowering` below).
//
// Why it refuses on shen-go today. `native_overrides_installed_after` on the
// `go` entry of builders.json is "shen.initialise", read off shen-go's
// generated main: `shen.initialise` runs BEFORE `InstallKernelFast`, so during
// boot the overridden functions are still their KL bodies and the boot enters
// them. Deleting those defuns would break the boot. The gate therefore refuses
// unless the declared phase is "none" or "before-initialise", and says which
// fact it read. When shen-go moves the install ahead of `shen.initialise` --
// the port's change, not Yggdrasil's -- the declaration changes and this code
// lights up with no edit here.
//
// Why the canonical slice is untouched. A lowered slice is not portable: it
// cannot go through the cross-target byte-identity check or the parity gate,
// because it is only valid for the one port whose table produced it. So it is
// written to OUTDIR/lowered/ and OUTDIR keeps the artifact of record. The
// lowered directory records what was done in yggdrasil.lowering.txt, a sidecar
// rather than a manifest edit, so that "the lowered slice is the canonical
// slice minus exactly these defuns" stays a byte-level property anything can
// re-check.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// loweredDirName is the subdirectory of OUTDIR that a lowered slice is written
// to. It is inside OUTDIR so that the two slices travel together and a reader
// can diff them without being told where the other one went.
const loweredDirName = "lowered"

// loweringReportName is the sidecar that says what was dropped and on whose
// authority. It is not the manifest: the lowered slice's manifest is a byte
// copy of the canonical one, which is what makes "nothing else changed"
// checkable by comparing every other file.
const loweringReportName = "yggdrasil.lowering.txt"

// lowerPhaseOK are the declared install phases under which dropping an
// overridden defun is safe: the natives are in place before any KL body could
// be entered. Anything else -- shen.initialise today -- means some phase of the
// run executes those bodies, and the slice must keep them.
//
// The two spellings are the only ones builders.json and port-contract.md use.
// An -ize alias was here and is gone: a gate that accepts spellings no
// declaration uses is a gate that accepts a typo as a permission.
var lowerPhaseOK = map[string]bool{
	"none":              true,
	"before-initialise": true,
}

// lowerGate is the decision, plus everything needed to explain it. It is a
// value rather than a (bool, error) pair because the refusal has to quote the
// fact and its provenance, and because the tests build one by hand for a port
// that does not exist yet.
type lowerGate struct {
	target      string
	names       []string // the target's native_overrides, sorted
	phase       string   // native_overrides_installed_after, as declared
	source      string   // native_overrides_source
	checkedBy   string   // native_overrides_checked_by
	phaseSource string   // native_overrides_installed_after_source
	ok          bool
	reason      string // a stable token for the SKIP line, "" when ok
}

// lowerGateFromBlock decides from one builders.json block. Split out from
// lowerGateFor so a test can feed a block for a port whose natives are
// installed before initialisation -- the configuration this code exists for
// and that no real target declares yet.
func lowerGateFromBlock(target string, block map[string]json.RawMessage) lowerGate {
	g := lowerGate{target: target}
	if block == nil {
		g.reason = "no-such-target"
		return g
	}
	if raw, ok := block["native_overrides"]; ok {
		var names []string
		if err := json.Unmarshal(raw, &names); err == nil {
			g.names = append(g.names, names...)
			sort.Strings(g.names)
		}
	}
	g.phase = strings.TrimSpace(jsonString(block["native_overrides_installed_after"]))
	g.source = jsonString(block["native_overrides_source"])
	g.checkedBy = jsonString(block["native_overrides_checked_by"])
	g.phaseSource = jsonString(block["native_overrides_installed_after_source"])
	switch {
	case len(g.names) == 0:
		g.reason = "no-native-overrides"
	case g.phase == "":
		// A list with no phase is the reading the port contract calls out
		// as false-by-omission: "these KL bodies never run". Refuse it.
		g.reason = "native-install-phase-undeclared"
	case !lowerPhaseOK[strings.ToLower(g.phase)]:
		g.reason = "natives-installed-after-initialise"
	case !factChecked(g.checkedBy):
		// The phase decides whether lowering is safe at all; this decides
		// whether the LIST can be trusted, and it is reported second for
		// that reason -- a bad phase cannot be fixed by writing a test.
		//
		// The rationale is on the record in docs/lowering.md: go's
		// hand-kept native_overrides went four symbols short of what
		// InstallKernelFast rebinds (`<-vector`, `==`, `@p`,
		// `shen.hds=?`) while carrying `native_overrides_verified: true`,
		// because nothing read the key and so nothing could contradict
		// it. A declared-but-unchecked list is a list that has drifted
		// and not been told. Deleting defuns on one is deleting on
		// someone's recollection.
		g.reason = "native-overrides-unchecked"
	default:
		g.ok = true
	}
	return g
}

// lowerGateForTarget is the seam the two subcommands read the gate through.
// It exists because everything after the gate -- the shake, the lowering, the
// three builds and the comparison -- is unreachable on every target that
// ships, so without it that path could only ever be exercised by a target
// nobody has. A test swaps in a gate for a port whose natives are installed
// before initialisation and drives the real command. It is never reassigned
// outside tests, and the production value is the line below.
var lowerGateForTarget = lowerGateFor

// lowerGateFor reads the embedded builders.json.
func lowerGateFor(target string) (lowerGate, error) {
	raw, err := rawBuilders()
	if err != nil {
		return lowerGate{target: target}, err
	}
	block, ok := raw[target]
	if !ok {
		return lowerGate{target: target, reason: "no-such-target"}, fmt.Errorf("unknown target %q", target)
	}
	return lowerGateFromBlock(target, block), nil
}

// refusal is the message a refused gate prints: the fact, the phase, and where
// both were read off. It names builders.json because that, not this file, is
// where the decision would have to change.
func (g lowerGate) refusal() string {
	var b strings.Builder
	fmt.Fprintf(&b, "lowering is refused for target %s: %s\n", g.target, g.reason)
	switch g.reason {
	case "no-such-target":
		fmt.Fprintf(&b, "  builders.json has no block for %q; yggdrasil targets lists the targets\n", g.target)
		return b.String()
	case "no-native-overrides":
		fmt.Fprintf(&b, "  builders.json declares no native_overrides for %s, so there is no kernel\n"+
			"  defun this port replaces and nothing for the pass to drop. That is not the same\n"+
			"  claim as \"this port overrides nothing\": an undeclared key means nobody measured it.\n", g.target)
		return b.String()
	case "native-install-phase-undeclared":
		fmt.Fprintf(&b, "  builders.json declares %d native_overrides for %s but no\n"+
			"  native_overrides_installed_after. A list of overridden defuns says nothing until it\n"+
			"  says when the swap happens, because the two readings differ on whether the kernel's\n"+
			"  KL ever runs. Dropping the bodies on the optimistic reading would delete boot code.\n",
			len(g.names), g.target)
	case "native-overrides-unchecked":
		fmt.Fprintf(&b, "  builders.json's native_overrides_checked_by for %s is %q: the list of %d\n"+
			"  overridden defuns is declared and nothing re-derives it from the port's source.\n"+
			"  A list nothing checks is a list that can drift without being told -- go's went four\n"+
			"  symbols short of what InstallKernelFast rebinds while carrying a _verified flag --\n"+
			"  and lowering deletes code on its say-so. Name a test in native_overrides_checked_by.\n",
			g.target, orNone(g.checkedBy), len(g.names))
	default:
		fmt.Fprintf(&b, "  builders.json says native_overrides_installed_after = %q for %s.\n"+
			"  The natives replace these %d KL bodies only after that point, so the bodies DO run\n"+
			"  before it and the slice must keep them. Dropping them would break the boot.\n"+
			"  Lowering is permitted only when the phase is \"none\" or \"before-initialise\".\n",
			g.phase, g.target, len(g.names))
	}
	if g.phaseSource != "" {
		fmt.Fprintf(&b, "  native_overrides_installed_after_source: %s\n", g.phaseSource)
	}
	if g.source != "" {
		fmt.Fprintf(&b, "  native_overrides_source: %s\n", g.source)
	}
	if g.checkedBy != "" {
		fmt.Fprintf(&b, "  native_overrides_checked_by: %s\n", g.checkedBy)
	}
	// The way out differs by reason, so the last line has to. It used to
	// name shen.initialise whatever the refusal was, which told a reader
	// with an undeclared phase or an unchecked list to go and fix something
	// else.
	switch g.reason {
	case "natives-installed-after-initialise":
		fmt.Fprintf(&b, "  Moving the install ahead of shen.initialise is the port's change, not\n"+
			"  Yggdrasil's; when %s declares it, this command works with no edit here.\n", g.target)
	case "native-install-phase-undeclared":
		fmt.Fprintf(&b, "  Read the phase off %s's boot and add native_overrides_installed_after\n"+
			"  (with its _source) to builders.json; nothing here can infer it.\n", g.target)
	case "native-overrides-unchecked":
		fmt.Fprintf(&b, "  Write the test that re-derives %s's list from the port's source and name it\n"+
			"  in native_overrides_checked_by; TestNativeOverridesMatchKernelFast is the pattern.\n", g.target)
	}
	return b.String()
}

// ---- the KL surgery ----

// klForm is one top-level form of a KL file, as a byte span into the source
// plus the defun name when it is a defun.
type klForm struct {
	start, end int // [start, end) -- end is just past the closing paren
	name       string
}

// klTopLevelForms splits KL source into top-level parenthesised forms. It is
// string-aware because kernel.kl carries string literals containing parens and
// newlines (the vector accessors' error messages are the live example), and a
// paren counter that did not know about them would cut a form in half. Shen
// string literals have no backslash escape -- a double quote ends the string,
// full stop -- which is the same assumption kernelDefunNames in scip.go makes.
func klTopLevelForms(src string) ([]klForm, error) {
	var forms []klForm
	depth, start, inStr := 0, -1, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '(':
			if depth == 0 {
				start = i
			}
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced KL: closing paren at byte %d with nothing open", i)
			}
			if depth == 0 {
				forms = append(forms, klForm{start: start, end: i + 1, name: klDefunName(src[start : i+1])})
				start = -1
			}
		}
	}
	if inStr {
		return nil, fmt.Errorf("unbalanced KL: unterminated string literal")
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced KL: %d unclosed paren(s) at end of file", depth)
	}
	return forms, nil
}

// klDefunName returns the name of a `(defun NAME ...)` form, or "".
func klDefunName(form string) string {
	const prefix = "(defun "
	if !strings.HasPrefix(form, prefix) {
		return ""
	}
	tail := form[len(prefix):]
	n := strings.IndexAny(tail, " \t\r\n()")
	if n <= 0 {
		return ""
	}
	return tail[:n]
}

// dropDefuns deletes the named top-level defuns from a KL source and returns
// the result plus the names actually deleted, in file order. Every byte that
// is not part of a deleted form (or of the blank line that separated it from
// the next) is carried through unchanged: the pass deletes, it never rewrites,
// and the test byte-compares the remainder to hold it to that.
func dropDefuns(src string, drop map[string]bool) (string, []string, error) {
	forms, err := klTopLevelForms(src)
	if err != nil {
		return "", nil, err
	}
	var out strings.Builder
	var dropped []string
	copied := 0
	for _, f := range forms {
		if f.name == "" || !drop[f.name] {
			continue
		}
		// Take the form and the newline run that follows it, so the file
		// does not accumulate blank lines where a defun used to be.
		lo, hi := f.start, f.end
		for hi < len(src) && src[hi] == '\n' {
			hi++
		}
		// A final form has no following separator; drop the blank line
		// before it instead, for the same reason. `lo > copied` is the
		// guard that matters: when the PREVIOUS form was also dropped,
		// `copied` already sits past that blank line, and stepping `lo`
		// back over it would hand src[copied:lo] a reversed range. That
		// case -- the last two forms of a file both dropped -- panicked.
		if hi == len(src) && lo > copied && lo >= 2 && src[lo-2] == '\n' && src[lo-1] == '\n' {
			lo--
		}
		out.WriteString(src[copied:lo])
		copied = hi
		dropped = append(dropped, f.name)
	}
	out.WriteString(src[copied:])
	return out.String(), dropped, nil
}

// ---- the rule ----

// badLowering is the Datalog rule of docs/lowering.md, evaluated in Go over
// the two relations this pass has:
//
//	badLowering(F) :- lowered(F), !equiv(F).
//
// It is the whole correctness check of the pass, and it is syntactic: a name
// the port never declared must never be deleted. Holding it in a function of
// its own rather than inlining the set membership is deliberate -- the rule is
// the thing under test, so it has to be callable by a test that feeds it a
// drop list the pass would not have produced.
func badLowering(lowered []string, equiv map[string]bool) []string {
	var bad []string
	for _, f := range lowered {
		if !equiv[f] {
			bad = append(bad, f)
		}
	}
	sort.Strings(bad)
	return bad
}

// ---- the pass ----

// lowerResult is what one lowering did, for the caller to print.
type lowerResult struct {
	dir     string   // OUTDIR/lowered
	dropped []string // in kernel.kl order
	kept    int      // kernel defuns remaining
	// skippedDirs are the subdirectories of OUTDIR the copy did not
	// descend into -- a stage-2 build tree left behind by an earlier
	// `yggdrasil build` into the same OUTDIR, typically. The lowered slice
	// is deliberately source-only, but "the copy is missing app-go/" has
	// to be something the caller said rather than something the user
	// discovers when the next build behaves differently.
	skippedDirs []string
}

// lowerSlice copies the canonical slice in outdir to outdir/lowered and
// deletes the gate's names from the copy's kernel.kl. Nothing else in the copy
// differs by a byte. The gate is taken as a parameter rather than read here so
// the caller decides -- and so a test can lower under a declaration no shipped
// target makes.
func lowerSlice(outdir string, g lowerGate) (*lowerResult, error) {
	if !g.ok {
		return nil, fmt.Errorf("%s", strings.TrimRight(g.refusal(), "\n"))
	}
	kernelPath := filepath.Join(outdir, "kernel.kl")
	src, err := os.ReadFile(kernelPath)
	if err != nil {
		return nil, fmt.Errorf("reading the canonical slice's kernel: %w", err)
	}
	equiv := map[string]bool{}
	for _, n := range g.names {
		equiv[n] = true
	}
	before, err := klTopLevelForms(string(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kernelPath, err)
	}
	originalDefuns := 0
	for _, f := range before {
		if f.name != "" {
			originalDefuns++
		}
	}
	out, dropped, err := dropDefuns(string(src), equiv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kernelPath, err)
	}
	// The rule, before anything is written. A pass that has already emitted
	// a wrong artifact and then reports it is a pass whose check is a log
	// line.
	if bad := badLowering(dropped, equiv); len(bad) > 0 {
		return nil, fmt.Errorf("badLowering is not empty: %s dropped without an equiv row in "+
			"%s's native_overrides. This is a defect in the pass, not in the declaration",
			strings.Join(bad, ", "), g.target)
	}

	lowDir := filepath.Join(outdir, loweredDirName)
	if err := os.RemoveAll(lowDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(lowDir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(outdir)
	if err != nil {
		return nil, err
	}
	var skipped []string
	for _, e := range entries {
		if e.Name() == loweredDirName {
			continue
		}
		if e.IsDir() {
			skipped = append(skipped, e.Name())
			continue
		}
		if e.Name() == "kernel.kl" {
			if err := os.WriteFile(filepath.Join(lowDir, "kernel.kl"), []byte(out), 0o644); err != nil {
				return nil, err
			}
			continue
		}
		if err := copyFileBytes(filepath.Join(outdir, e.Name()), filepath.Join(lowDir, e.Name())); err != nil {
			return nil, err
		}
	}
	if err := writeLoweringReport(filepath.Join(lowDir, loweringReportName), g, dropped); err != nil {
		return nil, err
	}
	sort.Strings(skipped)
	return &lowerResult{dir: lowDir, dropped: dropped, kept: originalDefuns - len(dropped), skippedDirs: skipped}, nil
}

func copyFileBytes(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	outf, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(outf, in); err != nil {
		outf.Close()
		return err
	}
	return outf.Close()
}

// writeLoweringReport records one line per dropped name with the builders.json
// citation the drop rests on. The citation is repeated per line rather than
// printed once in a header so that a single grepped line carries its own
// authority; a reader who finds `lowered map` in a log should not have to open
// the file to learn on whose say-so.
func writeLoweringReport(path string, g lowerGate, dropped []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# yggdrasil lowering report\n")
	fmt.Fprintf(&b, "lowered-for=%s\n", g.target)
	fmt.Fprintf(&b, "installed-after=%s\n", g.phase)
	fmt.Fprintf(&b, "dropped=%d of declared=%d\n", len(dropped), len(g.names))
	fmt.Fprintf(&b, "checked-by=%s\n", orNone(g.checkedBy))
	fmt.Fprintf(&b, "phase-source=%s\n", orNone(g.phaseSource))
	fmt.Fprintf(&b, "# one line per dropped kernel defun: the name, and the builders.json fact it rests on.\n")
	fmt.Fprintf(&b, "# Dropping F means deleting F's KL body; %s rebinds F to a native under the same\n", g.target)
	fmt.Fprintf(&b, "# name, so no call site changes and no name is introduced.\n")
	for _, name := range dropped {
		fmt.Fprintf(&b, "lowered %s\tequiv=builders.json:%s.native_overrides\tsource=%s\n",
			name, g.target, orNone(g.source))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// ---- the measurement ----

// lowerReport is the --report-only answer: of the kernel defuns this slice
// kept, which ones the target declares a native for. It is deliberately not
// gated on the install phase -- the number is a fact about the slice and the
// declaration, true whether or not lowering is permitted, and docs/lowering.md
// quotes it, so it has to be computable on the port as it is today.
type lowerReport struct {
	keptKernelDefuns int
	overridden       []string
}

func reportLowering(outdir string, g lowerGate) (*lowerReport, error) {
	kernelPath := filepath.Join(outdir, "kernel.kl")
	names, err := kernelDefunNames(kernelPath)
	if err != nil {
		return nil, err
	}
	rep := &lowerReport{keptKernelDefuns: len(names)}
	for _, n := range g.names {
		if names[n] {
			rep.overridden = append(rep.overridden, n)
		}
	}
	sort.Strings(rep.overridden)
	return rep, nil
}

// ---- CLI: lower ----

func cmdLower(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil lower", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	target := fs.String("target", "", "the port whose native_overrides table to lower against (required)")
	reportOnly := fs.Bool("report-only", false, "shake and measure only: print how many kept kernel defuns the target declares a native for, and which. Never writes lowered/ and is never refused by the install-phase gate")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target")); err != nil {
		return 2
	}
	if fs.NArg() < 2 || *target == "" {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil lower PROG OUTDIR --target T [--report-only]")
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	host := hostFields(*hostFlag)

	g, err := lowerGateForTarget(*target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil lower:", err)
		return 2
	}
	// The gate first, and before the shake: a refusal is a fact about
	// builders.json, not about the program, so it should not cost a host
	// boot to learn. --report-only is exempt by construction.
	if !*reportOnly && !g.ok {
		fmt.Fprint(os.Stderr, "yggdrasil lower: "+g.refusal())
		return 1
	}

	// Stage 1 is the existing shake, unchanged and unaware of lowering.
	out, err := shake(prog, outdir, host, *evalStyle, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}

	if *reportOnly {
		rep, err := reportLowering(out, g)
		if err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil lower:", err)
			return 1
		}
		fmt.Printf("yggdrasil-lower: report-only target=%s\n", *target)
		fmt.Printf("  kept-kernel-defuns=%d native-overridden=%d declared=%d\n",
			rep.keptKernelDefuns, len(rep.overridden), len(g.names))
		fmt.Printf("  names: %s\n", joinOrNone(rep.overridden))
		fmt.Printf("  installed-after=%s lowering=%s\n", orNone(g.phase), gateWord(g))
		return 0
	}

	res, err := lowerSlice(out, g)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil lower:", err)
		return 1
	}
	fmt.Printf("yggdrasil-lower: OK target=%s dropped=%d kept-kernel-defuns=%d\n",
		*target, len(res.dropped), res.kept)
	fmt.Printf("  canonical (artifact of record, untouched): %s\n", out)
	fmt.Printf("  lowered:                                   %s\n", res.dir)
	fmt.Printf("  report:                                    %s\n", filepath.Join(res.dir, loweringReportName))
	// A lowered slice is source only. Saying which subdirectories were left
	// behind beats letting the user find out from a build that behaves
	// differently -- the usual one is an app-<target>/ tree an earlier
	// `yggdrasil build` dropped into the same OUTDIR.
	if len(res.skippedDirs) > 0 {
		fmt.Printf("  subdirectories not copied (the lowered slice is source only): %s\n",
			strings.Join(res.skippedDirs, ", "))
	}
	return 0
}

func gateWord(g lowerGate) string {
	if g.ok {
		return "permitted"
	}
	return "refused(" + g.reason + ")"
}

// hostFields is the --host flag's decoding: split the launcher line, then
// resolve argv[0] on PATH. main.go repeats this inline five times (cmdStage,
// cmdCheck, cmdWhy, cmdFacts, cmdParity) and scip.go and trace.go once each,
// with no shared helper to call; this is the extraction those seven should
// become, not an eighth copy.
func hostFields(flagVal string) []string {
	if flagVal == "" {
		return nil
	}
	host := strings.Fields(flagVal)
	if hit := findExecutablePath(host[0]); hit != "" {
		host[0] = hit
	}
	return host
}

// ---- CLI: lower-check ----

// lowerCheckPair is one comparison of the three-way parity, named so the
// sentinel can say which pair disagreed rather than only that something did.
type lowerCheckPair struct {
	name       string
	aLbl, bLbl string
	a, b       string
}

// The three-way parity of docs/lowering.md. The evidence for a lowered slice
// is not a lemma about each substitution; it is that the lowered slice answers
// what the canonical slice answers, on the same port, on the fixture's own
// golden. Three runs, because two of the three comparisons localise a failure:
//
//	canonical on the reference host   vs golden   -- the slice is right at all
//	canonical on target T             vs the above -- the port agrees
//	lowered   on target T             vs the above -- the lowering changed nothing
//
// The third differing from the second is a wrong drop, and the sentinel names
// that pair. The first two are the existing parity arrangement; lowering adds
// only the third.
func cmdLowerCheck(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil lower-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	target := fs.String("target", "", "the port to lower for and run both slices on (required)")
	reference := fs.String("reference", "lisp", "reference target whose canonical run is compared against the golden")
	stdinFile := fs.String("stdin", "", "file fed to every run's stdin (default: tests/<name>.stdin when it exists)")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target", "reference", "stdin")); err != nil {
		return 2
	}
	if fs.NArg() < 2 || *target == "" {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil lower-check PROG OUTDIR --target T [--reference R] [--stdin FILE]")
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	host := hostFields(*hostFlag)
	in := *stdinFile
	if in == "" {
		in = defaultStdin(prog)
	}

	g, err := lowerGateForTarget(*target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil lower-check:", err)
		return 2
	}
	// A refused gate is a declared fact about the port, not a failure of
	// this check. It exits 0 and says why, on the sentinel line, so a gate
	// script can tell "this port does not admit lowering yet" from "the
	// lowered slice disagreed".
	if !g.ok {
		fmt.Printf("yggdrasil-lower-check: SKIP reason=%s target=%s\n", g.reason, *target)
		fmt.Print("  " + strings.ReplaceAll(strings.TrimRight(g.refusal(), "\n"), "\n", "\n  ") + "\n")
		return 0
	}

	out, err := shake(prog, outdir, host, *evalStyle, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	res, err := lowerSlice(out, g)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil lower-check:", err)
		return 1
	}

	pairs, labels, goldenWord, cerr := lowerCheckRuns(prog, *reference, *target, out, res.dir, in)
	if cerr != nil {
		var skip *lowerCheckSkip
		if errors.As(cerr, &skip) {
			fmt.Printf("yggdrasil-lower-check: SKIP reason=%s target=%s run=%s\n", skip.reason, *target, skip.run)
			fmt.Fprintln(os.Stderr, "yggdrasil lower-check:", cerr)
			return 3
		}
		var rf *lowerCheckRunErr
		if errors.As(cerr, &rf) {
			fmt.Println(rf.sentinel)
		}
		fmt.Fprintln(os.Stderr, "yggdrasil lower-check:", cerr)
		return 1
	}
	lines, ok := lowerCheckVerdict(pairs, *target, *reference, goldenWord, len(res.dropped), labels)
	for _, ln := range lines {
		fmt.Println(ln)
	}
	if !ok {
		return 1
	}
	fmt.Printf("  dropped: %s\n", joinOrNone(res.dropped))
	return 0
}

// lowerCheckSkip is a named reason the three-way parity could not be run at
// all -- a toolchain that is not here. It is not a disagreement and must not
// be reported as one.
type lowerCheckSkip struct{ reason, run string }

func (e *lowerCheckSkip) Error() string {
	return fmt.Sprintf("cannot run the %s leg: %s", e.run, e.reason)
}

// lowerCheckRunErr is a build or a run that failed. That IS a verdict on the
// arrangement, so it carries the sentinel line the consumers look for.
type lowerCheckRunErr struct {
	sentinel string
	err      error
}

func (e *lowerCheckRunErr) Error() string { return e.err.Error() }
func (e *lowerCheckRunErr) Unwrap() error { return e.err }

// lowerCheckRuns builds and runs the three configurations and returns the
// comparisons, the run labels, and the golden's basename ("none" when the
// fixture ships none).
//
// It takes the two directories rather than doing the shake and the lowering
// itself so that a test can hand it a canonical slice and a deliberately wrong
// "lowered" one and watch the comparison fail. Without that split the only way
// to reach this code would be a port that declares its natives installed
// before initialisation, and none does.
func lowerCheckRuns(prog, reference, target, canonicalDir, loweredDir, stdinFile string) ([]lowerCheckPair, []string, string, error) {
	type run struct {
		label string
		dir   string
		tgt   string
	}
	runs := []run{
		{"canonical@" + reference, canonicalDir, reference},
		{"canonical@" + target, canonicalDir, target},
		{"lowered@" + target, loweredDir, target},
	}
	labels := make([]string, len(runs))
	outs := make([]string, len(runs))
	for i, r := range runs {
		labels[i] = r.label
		argv, berr := build(r.tgt, r.dir, false)
		if berr != nil {
			return nil, labels, "", &lowerCheckRunErr{
				sentinel: "yggdrasil-lower-check: FAIL pair=none build=" + r.label,
				err:      berr}
		}
		if argv == nil {
			return nil, labels, "", &lowerCheckSkip{reason: "toolchain-missing", run: r.label}
		}
		out, _, rerr := runCapture(argv, stdinFile)
		if rerr != nil {
			return nil, labels, "", &lowerCheckRunErr{
				sentinel: "yggdrasil-lower-check: FAIL pair=none run=" + r.label,
				err:      rerr}
		}
		outs[i] = canon(out)
	}

	// The golden, when the fixture ships one. Its absence weakens the first
	// comparison to "the three agree", which is said rather than implied.
	goldenPath := strings.TrimSuffix(prog, ".shen") + ".expected"
	golden, gerr := os.ReadFile(goldenPath)
	goldenWord := "none"
	var pairs []lowerCheckPair
	if gerr == nil {
		goldenWord = filepath.Base(goldenPath)
		pairs = append(pairs, lowerCheckPair{
			name: "golden-vs-canonical-reference",
			aLbl: goldenWord, a: canon(string(golden)),
			bLbl: labels[0], b: outs[0]})
	}
	pairs = append(pairs,
		lowerCheckPair{
			name: "canonical-reference-vs-canonical-target",
			aLbl: labels[0], a: outs[0],
			bLbl: labels[1], b: outs[1]},
		lowerCheckPair{
			name: "canonical-target-vs-lowered-target",
			aLbl: labels[1], a: outs[1],
			bLbl: labels[2], b: outs[2]})
	return pairs, labels, goldenWord, nil
}

// lowerCheckVerdict turns the comparisons into the lines the command prints.
// The FIRST disagreeing pair is the verdict: the pairs are ordered upstream to
// downstream, and reporting a later disagreement would say "the lowering
// differs" about a slice that was already wrong before it was lowered.
func lowerCheckVerdict(pairs []lowerCheckPair, target, reference, goldenWord string, dropped int, labels []string) ([]string, bool) {
	for _, p := range pairs {
		if p.a != p.b {
			return []string{
				fmt.Sprintf("yggdrasil-lower-check: FAIL pair=%s target=%s dropped=%d", p.name, target, dropped),
				fmt.Sprintf("  %s: %q", p.aLbl, p.a),
				fmt.Sprintf("  %s: %q", p.bLbl, p.b),
			}, false
		}
	}
	return []string{
		fmt.Sprintf("yggdrasil-lower-check: OK target=%s reference=%s dropped=%d golden=%s",
			target, reference, dropped, goldenWord),
		fmt.Sprintf("  pairs checked: %d (three runs: %s)", len(pairs), strings.Join(labels, ", ")),
	}, true
}
