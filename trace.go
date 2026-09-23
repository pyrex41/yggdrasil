package main

// Runtime call tracing: a COVERAGE instrument for the shake, not a proof of
// it.
//
// docs/analysis-rules.md derives `reach`, the set of kernel defuns a program
// CAN call, from syntax, and the shake emits exactly `reach`. `--trace` asks
// the shaker to weave advice into the KL it is about to write (yggdrasil.shen,
// the "runtime call trace" section): every defun records its own name on
// entry, every (value V) records the global it reads. The trace is a fact
// relation; `yggdrasil trace-check` runs it against
//
//	 uncoveredCall(F) :- called(F), kernel(F), !reach(F).
//
// Weaving at the KLambda IR rather than in a backend is what makes one
// implementation serve all eight targets: the woven artifact is ordinary KL,
// so every stage-2 builder compiles it unchanged.
//
// WHAT THAT QUERY IS AND IS NOT. It is a tripwire with a narrow blast radius,
// and the earlier version of this comment -- "the soundness obligation [...]
// is discharged on paper by the rules; this discharges it on evidence" -- was
// false. On a port that runs the slice and only the slice, a call to a kernel
// defun outside `reach` is not recorded as an uncovered call: the name is not
// in the artifact, so the call is an undefined-function crash and there is no
// finished run to read facts from. uncoveredCall is EMPTY BY CONSTRUCTION
// there, and its emptiness is not evidence about the rules. The query has
// teeth only where the name resolves anyway -- a port that links a full
// kernel behind the slice (`dispatch: full-kernel` in docs/port-contract.md),
// a host-side facts dump, a relation assembled by hand -- and against a
// `reach` computed from a DIFFERENT program than the one that ran, which is
// the case trace_test.go's TestTraceCheckRules exercises.
//
// What the instrument does produce, which is what the report line says out
// loud rather than implying the sentence above:
//
//   - coverage. Which kernel defuns and globals a real run on a real target
//     entered, split into the boot (shen.initialise) phase and the program
//     phase -- a fib run is 49,076 records of which 20,000 are the property
//     vector's initialiser, so "the program called X" is otherwise
//     unanswerable. reach strictly containing called is expected: that is
//     imprecision, reported, never failed.
//   - completeness. The trace carries an end-of-run record, and the facts
//     carry the writer's declared row counts. Without them a one-line trace
//     and a whole run are the same document, which is how a truncated
//     called.facts once read as OK.
//   - correctness. The run's stdout is the fixture's committed golden,
//     compared in THIS run against THIS artifact. A trace of a run that
//     produced the wrong answer is not evidence for anything.
//
// A run exercises ONE path, so none of this is a proof; and two of the three
// are checks on the instrument itself rather than on the shake.

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// shakeExpr is the host expression shakeMode() evaluates: the entry point
// that --trace and --no-shake choose between, before prune.go's
// wrapShakeExpr wraps it with --prune-init's globals.
//
// --trace and --no-shake are refused together rather than ordered. The two
// ask for contradictory artifacts: --no-shake emits the FULL program as the
// reference A for scip-check, and the trace exists to check a SLICE against
// its own `reach`. Tracing A would compare a run of the unshaken program
// against the shaken program's footprint, which is not a question anyone
// asked; and the full initialiser is eval-capable, so the trace would be
// dominated by machinery the sliced artifact does not contain.
// --prune-init composes with either, and with --trace deliberately: the
// weaver runs after pruning, so a trace describes the artifact as shipped.
func shakeExpr(prog, outdir string, o shakeOpts) (string, error) {
	fn := "yggdrasil.shake"
	switch {
	case o.full && o.trace:
		return "", errors.New("--trace and --no-shake cannot be used together: " +
			"--no-shake emits the full program as scip-check's reference, and the trace " +
			"checks a slice against its own reach. Trace the shaken build instead")
	case o.full:
		fn = "yggdrasil.shake-full"
	case o.trace:
		fn = "yggdrasil.shake-traced"
	}
	return fmt.Sprintf(`(%s ["%s"] "%s")`, fn, prog, outdir), nil
}

// traceFileName is the path the woven ygg.trace-open opens, relative to the
// artifact's working directory. Fixed on both sides (ygg.*trace-file* in
// yggdrasil.shen) and recorded in the manifest as trace-file=.
const traceFileName = "yggdrasil.trace"

// The phase column of a trace record. `b` is everything shen.initialise
// does, `p` everything after it returns: without the split a fib run reads
// as 49,076 records of which 20,000 are shen.fillvector out of the property
// vector's initialiser, and "the program called X" is unanswerable.
const (
	phaseBoot    = "b"
	phaseProgram = "p"
)

// traceFacts is a parsed trace: the two relations, deduplicated, split by
// phase, and whether the run actually ended.
type traceFacts struct {
	called  []string // f<TAB>NAME records, either phase
	reads   []string // v<TAB>NAME records, either phase
	boot    []string // called, seen at least once in the boot phase
	program []string // called, seen at least once in the program phase
	lines   int      // records read, before dedup
	unknown int      // records with no phase column (a pre-phase artifact)
	// complete reports the e<TAB>end record. It is the ONLY thing that
	// distinguishes a finished run from a trace cut short -- by a crash, by
	// an early exit, or by a port that buffered the tail and never flushed
	// it. Without it a one-line trace and a 49,076-line one are the same
	// document, which is how a truncated called.facts used to read as OK.
	complete bool
}

// parseTrace reads the woven artifact's trace file. Format is one record per
// line:
//
//	f<TAB>NAME<TAB>PHASE   a defun entry
//	v<TAB>NAME<TAB>PHASE   a global read
//	e<TAB>end              the end-of-run record, written once, last
//
// The phase column is optional on read: an artifact woven by an older shaker
// has two columns, and a two-column record counts toward `called` with its
// phase unknown rather than being dropped, so a half-updated tree is not
// silently mis-read as an empty run. Anything else is ignored rather than
// fatal, because a port that interleaves its own output into the file should
// degrade to a smaller witness set, not to a failed check.
func parseTrace(path string) (*traceFacts, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("no trace file at %s: %w\n  the run did not reach the woven initialiser, or the port lost the buffered writes", path, err)
	}
	defer f.Close()
	calls, reads := map[string]bool{}, map[string]bool{}
	boot, program := map[string]bool{}, map[string]bool{}
	tf := &traceFacts{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		cols := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		if len(cols) < 2 {
			continue
		}
		tag, name := cols[0], cols[1]
		if tag == "e" {
			// EXACTLY the record ygg.trace-end writes -- two columns, no
			// more -- so that a port interleaving a line of its own that
			// happens to start with an e cannot forge the end of the run.
			// `e<TAB>end<TAB>anything` is that port's line, not ours.
			tf.complete = tf.complete || (len(cols) == 2 && name == "end")
			continue
		}
		if name == "" {
			continue
		}
		phase := ""
		if len(cols) >= 3 {
			phase = cols[2]
		}
		switch tag {
		case "f":
			calls[name] = true
		case "v":
			reads[name] = true
		default:
			continue
		}
		tf.lines++
		switch phase {
		case phaseBoot:
			if tag == "f" {
				boot[name] = true
			}
		case phaseProgram:
			if tag == "f" {
				program[name] = true
			}
		default:
			tf.unknown++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning %s: %w", path, err)
	}
	tf.called, tf.reads = sortedKeys(calls), sortedKeys(reads)
	tf.boot, tf.program = sortedKeys(boot), sortedKeys(program)
	return tf, nil
}

// phaseDegenerate reports the one combination of phase counts that cannot
// describe a run: the trace ENDED (so the last user toplevel form executed),
// every record carried a phase column, records were tagged boot -- and not
// one was tagged program. The flip to `p` is the last thing the woven
// shen.initialise body does, so this says the flip never executed while the
// program nonetheless ran to its end. That is the instrument failing, not the
// program calling nothing, and it is exactly what shen-go's KLambda VM does
// on the `kl` target: it abandons the initialiser's continuation, the program
// runs anyway, and every record in the file is tagged b.
//
// A trace with no phase column at all (an older shaker) is NOT degenerate: it
// is unphased, tf.unknown says so, and nothing pretends otherwise.
func (tf *traceFacts) phaseDegenerate() bool {
	return tf.complete && tf.unknown == 0 && len(tf.boot) > 0 && len(tf.program) == 0
}

// degenerateWarning is the report's own account of that failure. It exists as
// a function so the report cannot say it in one place and the tests assert it
// in another.
func degenerateWarning(target string) string {
	return fmt.Sprintf("  phase: DEGENERATE on target %s -- the run ended (end-of-run record present) "+
		"and every record is tagged boot.\n"+
		"    The woven flip to the program phase never executed on this target, so called-program=0 "+
		"is the instrument failing, not the program calling nothing;\n"+
		"    calledprogram.facts is NOT written, because an empty one would assert something this run "+
		"did not establish. Containment is unaffected: `called` is the whole run either way.", target)
}

// writeTraceMeta records, beside the fact files, what the writer of those
// files believed it wrote: the row counts and whether the trace it read
// carried its end-of-run record.
//
// This is what lets the host half detect a called.facts that is a strict
// PREFIX of the run rather than only one missing by a chosen name -- see the
// note above yggdrasil.trace-check. It is a loss detector, not a forgery
// detector: a hand that edits both files is writing a fiction, and no check
// confined to this directory can say otherwise.
//
// Deliberately not a rule input: analysis/analysis.dl and factRelations know
// nothing about it, and the trace rules read only called.facts and
// readglobal.facts.
func writeTraceMeta(factsDir string, tf *traceFacts) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "complete\t%v\n", tf.complete)
	fmt.Fprintf(&b, "called\t%d\n", len(tf.called))
	fmt.Fprintf(&b, "readglobal\t%d\n", len(tf.reads))
	fmt.Fprintf(&b, "program\t%d\n", len(tf.program))
	fmt.Fprintf(&b, "records\t%d\n", tf.lines)
	return os.WriteFile(filepath.Join(factsDir, traceMetaName), b.Bytes(), 0o644)
}

// traceMetaName is the sidecar's name in the facts dir. Fixed on both sides
// (yggdrasil.shen's yggdrasil.trace-check reads it by this name).
const traceMetaName = "trace.meta"

// sortedKeys lives in scip.go; the two stages want the same thing of a
// string set and there is no reason for two copies of it.

// writeFactsTSV writes one symbol per line, which is both Soufflé's default
// .input format for a unary relation and the shape docs/analysis-rules.md
// stage 4 wants readglobal.facts in.
func writeFactsTSV(path string, rows []string) error {
	var b bytes.Buffer
	for _, r := range rows {
		b.WriteString(r)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// runArtifact runs a built artifact with its working directory set to dir, so
// the trace file lands beside the artifact rather than in the caller's cwd.
// Otherwise it is runCapture: stdin is the fixture's bytes or nothing, never
// the parent's, and stderr passes through.
func runArtifact(argv []string, programFile, stdinFile, dir string) (string, error) {
	a := wrapExecutable(argv)
	cmd := exec.Command(a[0], a[1:]...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	in, closeIn, err := openRunStdin(programFile, stdinFile, nil)
	if err != nil {
		return "", err
	}
	defer closeIn()
	cmd.Stdin = in
	err = cmd.Run()
	return out.String(), err
}

// traceCheckHost evaluates (yggdrasil.trace-check ["prog"] "factsdir") on the
// stage-1 host and returns its sentinel line. Success is the sentinel, never
// the exit code -- the same trust model as shake() and check().
func traceCheckHost(prog, factsDir string, host []string, evalStyle string) (string, error) {
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", errors.New("no Shen host launcher found. Set $YGGDRASIL_HOST (or $BIFROST_SHEN_CL) to a Shen launcher")
	}
	prog, _ = filepath.Abs(prog)
	factsDir, _ = filepath.Abs(factsDir)
	root, err := yggRoot()
	if err != nil {
		return "", fmt.Errorf("materialising shaker: %w", err)
	}
	expr := fmt.Sprintf(`(yggdrasil.trace-check ["%s"] "%s")`, prog, factsDir)
	var argv []string
	if evalStyle == "positional" {
		drv := filepath.Join(factsDir, "_tracecheck_driver.shen")
		os.WriteFile(drv, []byte("(load \"yggdrasil.shen\")\n"+expr+"\n"), 0o644)
		argv = append(append([]string{}, host...), drv)
	} else {
		argv = append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	}
	out, _ := runAt(wrapExecutable(argv), root)
	i := strings.Index(out, "yggdrasil-trace-check:")
	if i < 0 {
		os.Stderr.WriteString(out)
		return "", fmt.Errorf("the host printed no yggdrasil-trace-check: sentinel (host=%s)", strings.Join(host, " "))
	}
	line := out[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return strings.TrimRight(line, "\r"), nil
}

// traceCheckResult is what a trace-check run produced, so a test can assert on
// it without re-parsing stdout.
type traceCheckResult struct {
	sentinel string   // the host's yggdrasil-trace-check: line
	ok       bool     // the sentinel says OK
	called   []string // deduplicated called(F)
	reads    []string // deduplicated readGlobal(V)
	boot     []string // called, entered during shen.initialise
	program  []string // called, entered after shen.initialise returned
	unknown  int      // records that carried no phase column
	records  int      // trace records before dedup
	complete bool     // the trace carried its end-of-run record
	// neverEntered are the manifest's fn= names -- the user's own defuns --
	// that the run did not enter. REPORTED, never fatal: a defun that this
	// input does not exercise is information about the input, not a
	// violation of anything. neverEnteredErr is why the list could not be
	// produced, when it could not: an unreadable manifest must say so
	// rather than disappear into an empty report.
	neverEntered    []string
	neverEnteredErr error
	// phaseBroken is tf.phaseDegenerate(): the run ended but nothing is
	// tagged program. Reported loudly, never fatal -- see degenerateWarning.
	phaseBroken bool
	stdout      string       // the artifact's stdout
	golden      goldenResult // what the comparison against tests/<name>.expected did
	factsDir    string
}

// traceFailure is a check that FAILED, as against a check that could not run.
// It carries the yggdrasil-trace-check: line the failure should be reported
// on, because every consumer of this command -- traceCheckHost itself, the
// parity-gate-style scripts that grep for the sentinel -- locates the result
// by that prefix. A failure that prints only to stderr is invisible to all of
// them, which is the same defect as a check that cannot fail.
type traceFailure struct {
	sentinel string // "yggdrasil-trace-check: FAIL ..."
	detail   error
}

func (f *traceFailure) Error() string { return f.detail.Error() }
func (f *traceFailure) Unwrap() error { return f.detail }

// failSentinel is the sentinel line to print for err, or "" when err is not a
// check failure (a missing toolchain, an unreadable file: those are not
// verdicts and must not be reported as one).
func failSentinel(err error) string {
	var f *traceFailure
	if errors.As(err, &f) {
		return f.sentinel
	}
	return ""
}

// traceSkip is a check that could not be RUN as evidence, as against one
// that ran and failed. It carries a NAME, because an unnamed skip is how a
// suite goes green on a case it never exercised: the report line and the
// host-gated tests both print it.
//
// The distinction matters more here than elsewhere. Torvalds's premise for
// the whole stdout leg is that a correct stdout is the only "the process
// finished" signal KL offers. Where that signal cannot be obtained, the
// honest answer is neither OK (a trace of a run nobody checked) nor FAIL (the
// run is not what is broken) but a named skip.
type traceSkip struct{ reason string }

func (s *traceSkip) Error() string { return "not evidence: " + s.reason }

// skipName is the reason err is a named skip, or "" when it is not one.
func skipName(err error) string {
	var s *traceSkip
	if errors.As(err, &s) {
		return s.reason
	}
	return ""
}

// evidencePossible refuses, up front, the target/fixture combinations on which
// a trace cannot be evidence about anything.
//
// There is one, and it is read off a DECLARED FACT rather than off a target's
// name: a target whose builders.json entry says `stdin: appended-to-program`
// has a runtime that reads its program from stdin (shen-go's cmd/kl does), so
// one descriptor does two jobs. The fixture's stdin bytes arrive after the KL
// forms, the runtime consumes them as further toplevel forms, and the program
// itself reads EOF. tests/stdin-sum then answers "bytes: 0 digest: 0" against
// a golden of "bytes: 15 digest: 12410", and the transcript carries a
// recovered panic out of the VM. Harvesting a called set from that run and
// printing OK is exactly the drift this file is about: the run demonstrably
// did not do what the fixture asks.
//
// Declining the golden comparison and proceeding was the earlier answer and
// was wrong -- it left the OK line and a green fixture test standing over a
// wrong run. Delivering stdin separately needs a file argument in shen-go's
// cmd/kl, which is another repository.
//
// Keying this on the fact is what lets a second such runtime be added as an
// entry in builders.json and be refused here the day it is added, with no edit
// to this file -- and what keeps the refusal from outliving its cause, since
// deleting the fact deletes the skip.
func evidencePossible(target, stdinFile string) error {
	if stdinFact, _ := runFacts(target); stdinFact == stdinAppendedToProgram && stdinFile != "" {
		return &traceSkip{reason: skipStdinAppended}
	}
	return nil
}

// skipStdinAppended is the NAME of that skip, on the sentinel line and in the
// host-gated tests. It names the fact, not the target: the old spelling
// (kl-runner-cannot-deliver-stdin) named a runner in this file that no longer
// exists.
const skipStdinAppended = "stdin-appended-to-program"

// requireComplete is the end-of-run gate, a pure function of a parsed trace so
// that the failure it exists to produce is testable without a stage-2 runtime.
// Everything downstream reads whatever records it finds as if they were the
// whole run, so a check that skips this is a check that cannot fail for the
// reason it claims to check.
func requireComplete(tf *traceFacts, path, target string) error {
	if tf.complete {
		return nil
	}
	return &traceFailure{
		sentinel: "yggdrasil-trace-check: FAIL truncated=no-end-record",
		detail: fmt.Errorf("%s has no end-of-run record after %d records: the run did not finish "+
			"(the program errored or exited before its last toplevel form), or the %s port lost the "+
			"buffered tail. Nothing is wrong with the footprint -- the trace is not a whole run, so "+
			"containment was not checked", path, tf.lines, target),
	}
}

// traceCheck is the whole pipeline, factored out of cmdTraceCheck so the
// host-gated test can drive it directly. A missing toolchain returns a nil
// result and a nil error: SKIP, never FAIL, exactly as build() and the parity
// gate treat it.
func traceCheck(prog, outdir, target, stdinFile string, host []string, evalStyle string) (*traceCheckResult, error) {
	// Before anything is built: can a run on this target/fixture pair be
	// evidence at all? A named skip here, not an OK over a run whose
	// stdout nobody could compare.
	if err := evidencePossible(target, stdinFile); err != nil {
		return nil, err
	}
	prog, _ = filepath.Abs(prog)
	outdir, _ = filepath.Abs(outdir)
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		return nil, err
	}
	factsDir := filepath.Join(outdir, "facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		return nil, err
	}

	// The untraced fact dump first: it is what `reach` is computed from, so
	// the check compares a traced RUN against the footprint of the program
	// as the user would actually ship it.
	if _, err := facts(prog, factsDir, host, evalStyle, true); err != nil {
		return nil, fmt.Errorf("fact dump: %w", err)
	}

	// The traced shake is a shake with trace set, not a global flipped
	// around the call: there is nothing to restore, so no error path and no
	// panic between here and the next line can leave tracing on.
	if _, err := shake(prog, outdir, host, evalStyle, true, shakeOpts{trace: true}); err != nil {
		return nil, fmt.Errorf("traced shake: %w", err)
	}

	// Every target is built the same way, through builders.json. `kl` used
	// to be the exception -- a runner in this file, reachable from this
	// subcommand and nowhere else -- and what was peculiar about it is now
	// declared on its entry instead (program_file, stdin, stdout).
	runArgv, err := build(target, outdir, false)
	if err != nil {
		var unsupported capabilityError
		if errors.As(err, &unsupported) {
			return nil, nil
		}
		return nil, err
	}
	if runArgv == nil {
		return nil, nil // a required tool is not on PATH
	}
	// Non-empty only for a runtime that reads its program from stdin, where
	// it is fed before the fixture's bytes -- which is why the pair above is
	// refused rather than run.
	progFile, err := programFileFor(target, outdir)
	if err != nil {
		return nil, err
	}

	tracePath := filepath.Join(outdir, traceFileName)
	os.Remove(tracePath)
	stdout, runErr := runArtifact(runArgv, progFile, stdinFile, outdir)
	if runErr != nil {
		return nil, fmt.Errorf("the traced %s artifact failed: %w", target, runErr)
	}

	tf, err := parseTrace(tracePath)
	if err != nil {
		return nil, err
	}
	if len(tf.called) == 0 {
		return nil, fmt.Errorf("%s is empty: the %s port wrote no trace records", tracePath, target)
	}
	if err := requireComplete(tf, tracePath, target); err != nil {
		return nil, err
	}
	// The other half of "the process finished": correct output. The end
	// record says the last form ran; the golden says it ran correctly. KL
	// offers nothing else, and doing it HERE rather than in a separate
	// parity invocation is what ties the claim to this run's artifact.
	gold, err := checkGolden(prog, target, stdout)
	if err != nil {
		return nil, err
	}

	if err := writeFactsTSV(filepath.Join(factsDir, "called.facts"), tf.called); err != nil {
		return nil, err
	}
	if err := writeFactsTSV(filepath.Join(factsDir, "readglobal.facts"), tf.reads); err != nil {
		return nil, err
	}
	// A report input, not a rule input: analysis.dl and factRelations know
	// nothing about it, and the phase split changes no rule's semantics.
	// When the phase instrument is degenerate on this target there is no
	// program-phase relation to write, and an empty file asserting that the
	// program called nothing would be worse than none: any stale one is
	// removed, and the report says why.
	programFacts := filepath.Join(factsDir, "calledprogram.facts")
	if tf.phaseDegenerate() {
		if err := os.Remove(programFacts); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	} else if err := writeFactsTSV(programFacts, tf.program); err != nil {
		return nil, err
	}
	// Last, so that a facts dir carrying a declared count is one whose fact
	// files were all written.
	if err := writeTraceMeta(factsDir, tf); err != nil {
		return nil, err
	}

	sentinel, err := traceCheckHost(prog, factsDir, host, evalStyle)
	if err != nil {
		return nil, err
	}
	res := &traceCheckResult{
		sentinel:    sentinel,
		ok:          strings.Contains(sentinel, " OK "),
		called:      tf.called,
		reads:       tf.reads,
		boot:        tf.boot,
		program:     tf.program,
		unknown:     tf.unknown,
		records:     tf.lines,
		complete:    tf.complete,
		phaseBroken: tf.phaseDegenerate(),
		stdout:      stdout,
		golden:      gold,
		factsDir:    factsDir,
	}
	res.neverEntered, res.neverEnteredErr = neverEntered(outdir, tf.called)
	return res, nil
}

// goldenResult is what the stdout comparison did, so the report line can say
// which of the three it was rather than implying the strongest one.
type goldenResult struct {
	path    string // tests/<name>.expected, "" when the fixture ships none
	checked bool   // the comparison actually ran
	how     string // "matches", "contained in transcript", or why it did not run
}

// checkGolden compares the traced run's stdout with the fixture's committed
// golden, the same tests/<name>.expected scripts/parity-gate.sh uses -- and
// with the SAME comparison the gate uses, canon() on both sides (main.go:
// CRLF folded, trailing newlines stripped). Two comparison semantics on one
// golden would mean a target that differs by a trailing newline fails
// trace-check and passes the parity gate against the same file, with nothing
// to say which was authoritative.
//
// A target that DECLARES `stdout: repl-transcript` has a runtime whose stdout
// is not the program's: shen-go's cmd/kl prints numbered prompts, echoes each
// form's value and reports its own panics, with the program's output embedded
// in that, so containment is the strongest thing assertable there. It is keyed
// on the declared fact rather than on a target's name, and it is a fact about
// the transcript rather than about the run.
//
// Containment alone is NOT a verdict, and reporting it as one is the defect
// this paragraph was written over: `kl` printed OK on every fixture while
// every boot panicked in `(shen.initialise)` and the VM recovered and carried
// on, so the fixture's answer was in the transcript and the comparison passed.
// So the target's declared `transcript_error_markers` are checked first, and
// so is an empty golden, which containment satisfies unconditionally. Only
// then does containment decide anything. The
// case such a runtime genuinely cannot serve -- a fixture stdin, where the VM
// eats the bytes as toplevel forms and the program reads EOF -- never reaches
// here: evidencePossible refuses it as a named skip before anything is shaken,
// because a trace of a run that answered the wrong thing is not evidence and
// must not be reported as OK.
//
// A mismatch is a traceFailure, not a bare error: it is a verdict on the run
// and has to reach the consumers that find verdicts by the sentinel line.
func checkGolden(prog, target, stdout string) (goldenResult, error) {
	path := strings.TrimSuffix(prog, ".shen") + ".expected"
	got := canon(stdout)
	_, stdoutFact := runFacts(target)
	transcript := stdoutFact == stdoutTranscript

	// The error markers come FIRST, before the golden is even looked for.
	// A marker is a fact about the RUN -- this runtime printed a panic and
	// carried on -- and not about the comparison, so it must not depend on
	// a fixture having committed a golden. It did, and tests/partial.shen
	// ships none: kl/partial reported OK over the same panicking boot that
	// failed kl/fib, because the comparison returned early and the markers
	// were checked inside it.
	if transcript {
		if m, line := transcriptError(transcriptErrorMarkers(target), got); m != "" {
			return goldenResult{}, &traceFailure{
				sentinel: "yggdrasil-trace-check: FAIL stdout=transcript-error",
				detail: fmt.Errorf("the traced %s run's transcript carries the declared error "+
					"marker %q (builders.json transcript_error_markers) at\n    %s\n"+
					"  The fixture's output may still appear later in the transcript -- this "+
					"runtime recovers and runs the next toplevel form -- so containment would "+
					"have passed over a run that failed. A trace of such a run is not evidence",
					target, m, line)}
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		return goldenResult{how: "no committed golden for this fixture"}, nil
	}
	wanted := canon(string(want))
	sentinel := "yggdrasil-trace-check: FAIL stdout=mismatch-vs-" + filepath.Base(path)
	if transcript {
		// The other way containment concludes nothing, checked before it
		// runs: a comparison that cannot fail must not be reported as one
		// that passed.
		if wanted == "" {
			return goldenResult{path: path}, &traceFailure{
				sentinel: "yggdrasil-trace-check: FAIL stdout=golden-empty",
				detail: fmt.Errorf("%s is empty, and this target's stdout is compared by "+
					"containment (builders.json declares stdout=%s), so the comparison holds "+
					"against any transcript whatsoever. Commit the fixture's real output or "+
					"delete the file; an empty golden is a check that cannot fail",
					path, stdoutTranscript)}
		}
		if !strings.Contains(got, wanted) {
			return goldenResult{path: path}, &traceFailure{sentinel: sentinel, detail: fmt.Errorf(
				"the traced %s run's transcript does not contain %s:\n  want: %q\n  got:  %q",
				target, path, wanted, got)}
		}
		return goldenResult{path: path, checked: true, how: "contained in the " + target + " transcript"}, nil
	}
	if got != wanted {
		return goldenResult{path: path}, &traceFailure{sentinel: sentinel, detail: fmt.Errorf(
			"the traced %s run's stdout does not match %s:\n  want: %q\n  got:  %q\n"+
				"  the trace describes a run that produced the wrong answer, so it is not evidence for anything",
			target, path, wanted, got)}
	}
	return goldenResult{path: path, checked: true, how: "matches"}, nil
}

// neverEntered is the manifest's fn= names minus the run's called set: the
// user defuns this input did not exercise. Reported, never fatal -- a defun
// an input does not reach is information about the input.
//
// The error is returned rather than swallowed. Returning nil for both would
// make an unreadable or renamed manifest look exactly like a run that entered
// everything, and "the report silently became empty" is the failure mode this
// whole commit is about.
func neverEntered(outdir string, called []string) ([]string, error) {
	fns, err := manifestFnNames(outdir)
	if err != nil {
		return nil, fmt.Errorf("the user defuns never entered cannot be reported: %w", err)
	}
	seen := map[string]bool{}
	for _, c := range called {
		seen[c] = true
	}
	var out []string
	for _, f := range fns {
		if !seen[f] {
			out = append(out, f)
		}
	}
	return out, nil
}

// manifestFnNames reads the fn=<name> <arity> lines of the txt manifest.
func manifestFnNames(outdir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(outdir, "yggdrasil.manifest.txt"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "fn=")
		if !ok || v == "" {
			continue
		}
		name, _, _ := strings.Cut(v, " ")
		if name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// defaultStdin returns tests/<name>.stdin when the fixture ships one, the way
// scripts/parity-gate.sh picks one up, so `yggdrasil trace-check
// tests/stdin-sum.shen out --target go` does the right thing unasked.
func defaultStdin(prog string) string {
	cand := strings.TrimSuffix(prog, ".shen") + ".stdin"
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return ""
}

func cmdTraceCheck(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil trace-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	target := fs.String("target", "", "stage-2 target to build and run the traced artifact on")
	stdinFile := fs.String("stdin", "", "file fed to the artifact's stdin (default: tests/<name>.stdin when it exists)")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target", "stdin")); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil trace-check PROG OUTDIR --target T [--stdin FILE]")
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	if *target == "" {
		fmt.Fprintln(os.Stderr, "yggdrasil trace-check: --target is required")
		return 2
	}
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}
	in := *stdinFile
	if in == "" {
		in = defaultStdin(prog)
	}
	start := time.Now()
	res, err := traceCheck(prog, outdir, *target, in, host, *evalStyle)
	if err != nil {
		// A check that could not be run as evidence is a SKIP with a
		// name, on the sentinel line and at the skip exit code -- never a
		// pass, and never a failure of the run.
		if name := skipName(err); name != "" {
			fmt.Println("yggdrasil-trace-check: SKIP " + name)
			fmt.Fprintln(os.Stderr, "yggdrasil:", err)
			return 3
		}
		// A FAILED check reports itself on the sentinel line, like the
		// host half's own FAIL forms: every consumer locates the verdict
		// by that prefix, so a verdict that appears only on stderr is one
		// none of them can see. An error that is not a verdict -- a
		// missing tool, an unreadable file -- stays stderr-only.
		if line := failSentinel(err); line != "" {
			fmt.Println(line)
		}
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	if res == nil {
		fmt.Fprintf(os.Stderr, "yggdrasil: target %q skipped (a required tool is not on PATH)\n", *target)
		return 3
	}
	fmt.Println(res.sentinel)
	fmt.Printf("  target=%s records=%d called=%d called-program=%d readGlobal=%d complete=%v facts=%s elapsed=%s\n",
		*target, res.records, len(res.called), len(res.program), len(res.reads), res.complete,
		res.factsDir, time.Since(start).Truncate(time.Millisecond))
	// Reported, not enforced: a coverage instrument is a coverage
	// instrument, and saying so is what keeps it from being read as a
	// soundness claim. A phase split that came out impossible says so here
	// too -- an instrument that cannot report its own failure is the defect
	// one level up from a check that cannot fail.
	if res.phaseBroken {
		fmt.Println(degenerateWarning(*target))
	} else {
		fmt.Printf("  phase: boot=%d program=%d unphased-records=%d\n",
			len(res.boot), len(res.program), res.unknown)
	}
	name := "tests/<fixture>.expected"
	if res.golden.path != "" {
		name = filepath.Base(res.golden.path)
	}
	fmt.Printf("  stdout vs %s: %s\n", name, res.golden.how)
	switch {
	case res.neverEnteredErr != nil:
		fmt.Printf("  user defuns never entered on this input: UNKNOWN (%v)\n", res.neverEnteredErr)
	case len(res.neverEntered) > 0:
		fmt.Printf("  user defuns never entered on this input: %s\n", strings.Join(res.neverEntered, ", "))
	default:
		fmt.Println("  user defuns never entered on this input: none")
	}
	// Last line, and the one hickey-1 is about: what an empty uncoveredCall
	// on this target does and does not establish. The file header says it at
	// length; the tool has to say it where the number is read.
	fmt.Println("  scope: coverage, not soundness -- on a port that runs the slice and only " +
		"the slice, uncoveredCall is empty by construction (a call outside reach is an " +
		"undefined-function crash, not a record).")
	if !res.ok {
		return 1
	}
	return 0
}
