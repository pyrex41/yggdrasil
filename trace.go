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
	"sort"
	"strings"
	"time"
)

// traceMode makes shake() call (yggdrasil.shake-traced ...) instead of
// (yggdrasil.shake ...). It is a package variable rather than a parameter so
// that the default shake path -- and every existing caller of shake() -- is
// untouched, which is also what keeps untraced output byte-identical.
var traceMode bool

// shakeExpr is the host expression shake() evaluates.
func shakeExpr(prog, outdir string) string {
	fn := "yggdrasil.shake"
	if traceMode {
		fn = "yggdrasil.shake-traced"
	}
	return fmt.Sprintf(`(%s ["%s"] "%s")`, fn, prog, outdir)
}

// traceFileName is the path the woven ygg.trace-open opens, relative to the
// artifact's working directory. Fixed on both sides (ygg.*trace-file* in
// yggdrasil.shen) and recorded in the manifest as trace-file=.
const traceFileName = "yggdrasil.trace"

// traceFacts is a parsed trace: the two relations, deduplicated.
type traceFacts struct {
	called []string // f<TAB>NAME records
	reads  []string // v<TAB>NAME records
	lines  int      // records read, before dedup
}

// parseTrace reads the woven artifact's trace file. Format is one record per
// line, "f\tNAME" for a defun entry and "v\tNAME" for a global read; anything
// else is ignored rather than fatal, because a port that interleaves its own
// output into the file should degrade to a smaller witness set, not to a
// failed check.
func parseTrace(path string) (*traceFacts, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("no trace file at %s: %w\n  the run did not reach the woven initialiser, or the port lost the buffered writes", path, err)
	}
	defer f.Close()
	calls, reads := map[string]bool{}, map[string]bool{}
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		tag, name, ok := strings.Cut(line, "\t")
		if !ok || name == "" {
			continue
		}
		switch tag {
		case "f":
			calls[name] = true
			n++
		case "v":
			reads[name] = true
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning %s: %w", path, err)
	}
	return &traceFacts{called: sortedKeys(calls), reads: sortedKeys(reads), lines: n}, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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
	records  int      // trace records before dedup
	stdout   string   // the artifact's stdout
	factsDir string
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
	if err := writeFactsTSV(filepath.Join(factsDir, "called.facts"), tf.called); err != nil {
		return nil, err
	}
	if err := writeFactsTSV(filepath.Join(factsDir, "readglobal.facts"), tf.reads); err != nil {
		return nil, err
	}

	sentinel, err := traceCheckHost(prog, factsDir, host, evalStyle)
	if err != nil {
		return nil, err
	}
	return &traceCheckResult{
		sentinel: sentinel,
		ok:       strings.Contains(sentinel, " OK "),
		called:   tf.called,
		reads:    tf.reads,
		records:  tf.lines,
		stdout:   stdout,
		factsDir: factsDir,
	}, nil
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
	fmt.Printf("  target=%s records=%d called=%d readGlobal=%d facts=%s elapsed=%s\n",
		*target, res.records, len(res.called), len(res.reads), res.factsDir,
		time.Since(start).Truncate(time.Millisecond))
	if !res.ok {
		return 1
	}
	return 0
}
