package main

// Runtime call tracing: the empirical half of the reachability claim.
//
// docs/analysis-rules.md derives `reach`, the set of kernel defuns a program
// CAN call, from syntax. The soundness obligation behind the whole shake is
// that nothing outside `reach` ever runs. That obligation is discharged on
// paper by the rules; this discharges it on evidence, per target, on a real
// run.
//
// `--trace` asks the shaker to weave advice into the KL it is about to write
// (yggdrasil.shen, the "runtime call trace" section): every defun records its
// own name on entry, every (value V) records the global it reads. The trace
// is a fact relation; `yggdrasil trace-check` runs it against
//
//     uncoveredCall(F) :- called(F), kernel(F), !reach(F).
//
// which must be empty. Weaving at the KLambda IR rather than in a backend is
// what makes one implementation serve all eight targets: the woven artifact
// is ordinary KL, so every stage-2 builder compiles it unchanged.
//
// What this cannot do: a run exercises ONE path. An empty uncoveredCall is
// evidence for the soundness lemma on the inputs tried, never a proof. The
// interesting failure would be the other direction anyway -- reach ⊋ called
// is expected and is just imprecision.

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

// traceMode makes shake() call (yggdrasil.shake-traced ...) instead of
// (yggdrasil.shake ...). It is a package variable rather than a parameter so
// that the default shake path -- and every existing caller of shake() -- is
// untouched, which is also what keeps untraced output byte-identical.
var traceMode bool

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
func shakeExpr(prog, outdir string, full bool) (string, error) {
	fn := "yggdrasil.shake"
	switch {
	case full && traceMode:
		return "", errors.New("--trace and --no-shake cannot be used together: " +
			"--no-shake emits the full program as scip-check's reference, and the trace " +
			"checks a slice against its own reach. Trace the shaken build instead")
	case full:
		fn = "yggdrasil.shake-full"
	case traceMode:
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
			// Exactly the record ygg.trace-end writes, so that a port
			// interleaving a line of its own that happens to start with
			// an e cannot forge the end of the run.
			tf.complete = tf.complete || name == "end"
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
func runArtifact(argv []string, stdinFile, dir string) (string, error) {
	a := wrapExecutable(argv)
	cmd := exec.Command(a[0], a[1:]...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	if stdinFile != "" {
		f, err := os.Open(stdinFile)
		if err != nil {
			return "", fmt.Errorf("cannot read --stdin file: %w", err)
		}
		defer f.Close()
		cmd.Stdin = f
	}
	err := cmd.Run()
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
	// violation of anything.
	neverEntered []string
	stdout       string       // the artifact's stdout
	golden       goldenResult // what the comparison against tests/<name>.expected did
	factsDir     string
}

// traceCheck is the whole pipeline, factored out of cmdTraceCheck so the
// host-gated test can drive it directly. A missing toolchain returns a nil
// result and a nil error: SKIP, never FAIL, exactly as build() and the parity
// gate treat it.
func traceCheck(prog, outdir, target, stdinFile string, host []string, evalStyle string) (*traceCheckResult, error) {
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

	traceMode = true
	_, err := shake(prog, outdir, host, evalStyle, true)
	traceMode = false
	if err != nil {
		return nil, fmt.Errorf("traced shake: %w", err)
	}

	var runArgv []string
	runStdin := stdinFile
	if target == klTarget {
		var feed string
		runArgv, feed, err = klRunner(outdir, stdinFile)
		if err != nil {
			return nil, err
		}
		if runArgv == nil {
			return nil, nil // no Go toolchain or no sibling shen-go
		}
		runStdin = feed // the VM reads its program, then the fixture bytes
	} else {
		runArgv, err = build(target, outdir, false)
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
	}

	tracePath := filepath.Join(outdir, traceFileName)
	os.Remove(tracePath)
	stdout, runErr := runArtifact(runArgv, runStdin, outdir)
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
	// The trace has to be able to say it ENDED. Everything below reads a
	// prefix of the run as if it were the run, so a check that skips this
	// is a check that cannot fail for the reason it claims to check.
	if !tf.complete {
		return nil, fmt.Errorf("%s has no end-of-run record after %d records: the run did not finish "+
			"(the program errored or exited before its last toplevel form), or the %s port lost the "+
			"buffered tail. Nothing is wrong with the footprint -- the trace is not a whole run, so "+
			"containment was not checked", tracePath, tf.lines, target)
	}
	// The other half of "the process finished": correct output. The end
	// record says the last form ran; the golden says it ran correctly. KL
	// offers nothing else, and doing it HERE rather than in a separate
	// parity invocation is what ties the claim to this run's artifact.
	gold, err := checkGolden(prog, target, stdinFile, stdout)
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
	if err := writeFactsTSV(filepath.Join(factsDir, "calledprogram.facts"), tf.program); err != nil {
		return nil, err
	}

	sentinel, err := traceCheckHost(prog, factsDir, host, evalStyle)
	if err != nil {
		return nil, err
	}
	return &traceCheckResult{
		sentinel:     sentinel,
		ok:           strings.Contains(sentinel, " OK "),
		called:       tf.called,
		reads:        tf.reads,
		boot:         tf.boot,
		program:      tf.program,
		unknown:      tf.unknown,
		records:      tf.lines,
		complete:     tf.complete,
		neverEntered: neverEntered(outdir, tf.called),
		stdout:       stdout,
		golden:       gold,
		factsDir:     factsDir,
	}, nil
}

// goldenResult is what the stdout comparison did, so the report line can say
// which of the three it was rather than implying the strongest one.
type goldenResult struct {
	path    string // tests/<name>.expected, "" when the fixture ships none
	checked bool   // the comparison actually ran
	how     string // "matches", "contained in transcript", or why it did not run
}

// checkGolden compares the traced run's stdout with the fixture's committed
// golden, the same tests/<name>.expected scripts/parity-gate.sh uses.
//
// The kl runner is not a port and its "stdout" is not the program's: it is a
// KLambda REPL transcript -- numbered prompts, echoed values, the VM's own
// panics -- with the program's output embedded in it, so containment is the
// strongest thing assertable there. And when the fixture ships stdin, the kl
// runner cannot deliver it at all: klRunner appends the fixture bytes after
// the driver forms on the one descriptor the VM reads its PROGRAM from, so
// the VM consumes them as toplevel forms and the program reads EOF. That is
// a defect in the runner, not in the run, so the comparison is declined --
// out loud, on the report line -- rather than either failing or pretending.
func checkGolden(prog, target, stdinFile, stdout string) (goldenResult, error) {
	path := strings.TrimSuffix(prog, ".shen") + ".expected"
	want, err := os.ReadFile(path)
	if err != nil {
		return goldenResult{how: "no committed golden for this fixture"}, nil
	}
	if target == klTarget {
		if stdinFile != "" {
			return goldenResult{path: path,
				how: "not checked: the kl runner feeds the program and the fixture's stdin down one descriptor"}, nil
		}
		if !strings.Contains(stdout, string(want)) {
			return goldenResult{path: path}, fmt.Errorf(
				"the traced kl run's transcript does not contain %s:\n  want: %q\n  got:  %q",
				path, string(want), stdout)
		}
		return goldenResult{path: path, checked: true, how: "contained in the kl transcript"}, nil
	}
	if stdout != string(want) {
		return goldenResult{path: path}, fmt.Errorf(
			"the traced %s run's stdout does not match %s:\n  want: %q\n  got:  %q\n"+
				"  the trace describes a run that produced the wrong answer, so it is not evidence for anything",
			target, path, string(want), stdout)
	}
	return goldenResult{path: path, checked: true, how: "matches"}, nil
}

// neverEntered is the manifest's fn= names minus the run's called set: the
// user defuns this input did not exercise. Reported, never fatal.
func neverEntered(outdir string, called []string) []string {
	fns, err := manifestFnNames(outdir)
	if err != nil {
		return nil
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
	return out
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

// ---- the built-in "kl" runner ------------------------------------------
//
// trace-check takes any target in builders.json, plus one that is not in it:
// `kl`, the shaken KL run directly on shen-go's bare KLambda VM
// (cmd/kl in the sibling checkout). It exists because it is the most direct
// reading of the question this check asks. The claim under test is about the
// KL the shake WRITES; a stage-2 builder is a second program that compiles
// that KL, and when its own compiler image will not boot -- which shen-go's
// yggdrasil-build has been observed to do, see the port caveat in
// docs/analysis-rules.md -- the artifact still has a runtime that will
// execute it verbatim. `kl` is therefore the fallback that keeps the check
// runnable, and `--target go` remains the compositional case that also
// checks the backend.
//
// The VM reads its program from stdin, so a fixture's stdin bytes are
// appended after the driver forms and are read by the program from the same
// descriptor.
const klTarget = "kl"

// klRunner builds the sibling shen-go's KL VM and writes the driver: the
// shaken kernel, the initialiser call, then the user files in manifest
// order -- the stage-2 builder contract, executed rather than compiled.
func klRunner(outdir, stdinFile string) (argv []string, feed string, err error) {
	builders, err := loadBuilders()
	if err != nil {
		return nil, "", err
	}
	b, ok := builders["go"]
	if !ok {
		return nil, "", errors.New(`the "kl" runner needs the go builder entry to locate the shen-go checkout`)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return nil, "", nil // no Go toolchain: SKIP
	}
	dir := siblingDir("go", b)
	if fi, err := os.Stat(filepath.Join(dir, "cmd", "kl")); err != nil || !fi.IsDir() {
		return nil, "", nil // no sibling shen-go: SKIP
	}
	bin := filepath.Join(outdir, "klvm")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/kl")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("building the shen-go KL VM in %s: %w", dir, err)
	}

	var body bytes.Buffer
	kern, err := os.ReadFile(filepath.Join(outdir, "kernel.kl"))
	if err != nil {
		return nil, "", err
	}
	body.Write(kern)
	body.WriteString("\n(shen.initialise)\n")
	users, err := manifestUserFiles(outdir)
	if err != nil {
		return nil, "", err
	}
	for _, u := range users {
		src, err := os.ReadFile(filepath.Join(outdir, u))
		if err != nil {
			return nil, "", err
		}
		body.Write(src)
		body.WriteString("\n")
	}
	if stdinFile != "" {
		in, err := os.ReadFile(stdinFile)
		if err != nil {
			return nil, "", err
		}
		body.Write(in)
	}
	feed = filepath.Join(outdir, "_klvm_feed.kl")
	if err := os.WriteFile(feed, body.Bytes(), 0o644); err != nil {
		return nil, "", err
	}
	return []string{bin}, feed, nil
}

// manifestUserFiles reads the user= lines of the txt manifest, in order.
func manifestUserFiles(outdir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(outdir, "yggdrasil.manifest.txt"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "user="); ok && v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no user= line in %s/yggdrasil.manifest.txt", outdir)
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
	// soundness claim.
	fmt.Printf("  phase: boot=%d program=%d unphased-records=%d\n", len(res.boot), len(res.program), res.unknown)
	name := "tests/<fixture>.expected"
	if res.golden.path != "" {
		name = filepath.Base(res.golden.path)
	}
	fmt.Printf("  stdout vs %s: %s\n", name, res.golden.how)
	if len(res.neverEntered) > 0 {
		fmt.Printf("  user defuns never entered on this input: %s\n", strings.Join(res.neverEntered, ", "))
	}
	if !res.ok {
		return 1
	}
	return 0
}
