// Command yggdrasil is a friendly CLI around the Yggdrasil Shen tree-shaker.
//
// Yggdrasil itself is a Shen program (yggdrasil.shen) that runs on a host Shen
// port and shakes a Shen program into a minimal, portable KLambda slice;
// per-target builders then compile that slice into a standalone artifact. This
// Go binary makes both stages a one-liner and embeds the shaker source + the
// kernel KLambda slice, so it runs with no install (`go install` / a release
// binary), materialising the embedded tree to a cache dir on first use.
//
// Subcommands:
//
//	shake  PROG OUTDIR             stage 1: emit kernel.kl + <prog>.kl + manifest
//	build  PROG OUTDIR --target T  stage 1 + stage 2 builder for target T
//	                               (--web with --target js: emit a browser module)
//	run    PROG OUTDIR --target T  build, then execute the artifact (prints stdout)
//	check  PROG                    typecheck PROG under (tc +) on a live host;
//	                               also available as --typecheck on shake/build/run,
//	                               which gates the shake and records typechecked=
//	                               in the manifest
//	trace-check PROG OUTDIR --target T
//	                               shake with --trace, run on T, and check that
//	                               every kernel defun the run entered is in reach
//	parity PROG OUTDIR             behavioural parity gate: run the shaken slice on
//	                               every target and diff outputs against a reference
//	scip-check PROG OUTDIR         stage-5 level-2 oracle: build the shaken and the
//	                               full program with one builder and compare their
//	                               indexes node for node (see scip.go)
//	targets                        list available stage-2 targets
package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Embedded shaker source + kernel slice + per-language primitives + in-repo
// builders + the build recipe table, so the binary is self-contained.
//
// The KLambda patterns are deliberately explicit rather than the directory
// `KLambda`. A bare directory pattern embeds whatever is in the WORKING TREE,
// gitignored build artifacts included -- and the derived call-graph cache used
// to land there, so it rode into the binary, into embeddedHash() (which names
// the extracted root), and into every root. That made the shaker's output
// depend on untracked files present at `go build` time, and since a cache is
// keyed by filename alone, a stale one loaded fine and silently under-shook.
// Naming the file types makes that unrepresentable; portable_test.go's
// TestEmbeddedTreeHasNoGeneratedCache covers the same property for the other
// two trees, which have no generator writing into them.
//
//go:embed yggdrasil.shen builders.json
//go:embed KLambda/*.kl KLambda/LICENSE KLambda/PROVENANCE.md
//go:embed Primitives
//go:embed builders
var embedded embed.FS

// ---- cross-platform helpers (kept in sync with bifrost's copies) ----

func isWindows() bool { return runtime.GOOS == "windows" }

func pathext() []string {
	raw := os.Getenv("PATHEXT")
	if raw == "" {
		raw = ".COM;.EXE;.BAT;.CMD"
	}
	var out []string
	for _, e := range strings.Split(raw, ";") {
		if strings.TrimSpace(e) != "" {
			out = append(out, strings.ToLower(e))
		}
	}
	return out
}

func findExecutablePath(path string) string { return findExecutableFor(path, isWindows(), pathext()) }

func findExecutableFor(path string, windows bool, exts []string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if windows {
		for _, ext := range exts {
			if _, err := os.Stat(path + ext); err == nil {
				return path + ext
			}
		}
	}
	return ""
}

func wrapExecutable(argv []string) []string { return wrapExecutableFor(argv, isWindows()) }

func wrapExecutableFor(argv []string, windows bool) []string {
	if windows && len(argv) > 0 {
		low := strings.ToLower(argv[0])
		switch {
		case strings.HasSuffix(low, ".bat"), strings.HasSuffix(low, ".cmd"):
			return append([]string{"cmd", "/c"}, argv...)
		case strings.HasSuffix(low, ".sh"):
			return append([]string{"sh"}, argv...)
		}
	}
	return argv
}

// embeddedHash returns a short content hash over the entire embedded tree, so
// the materialised cache is keyed by exactly what would be extracted.
func embeddedHash() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := embedded.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// ---- materialised root ----

// yggRoot extracts the embedded tree to a versioned cache dir (once) and returns
// its path. yggdrasil.shen + KLambda + builders must live on disk for the host
// and the stage-2 builders. The cache key hashes the WHOLE embedded tree, so any
// change to the shaker, kernel, or a builder invalidates a stale cache.
func yggRoot() (string, error) {
	ver, err := embeddedHash()
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		cache = os.TempDir()
	}
	root := filepath.Join(cache, "yggdrasil-go", ver)
	sentinel := filepath.Join(root, ".ok")
	if _, err := os.Stat(sentinel); err == nil {
		return root, nil // already extracted
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	err = fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		dst := filepath.Join(root, p)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := embedded.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(dst, b, mode)
	})
	if err != nil {
		return "", err
	}
	os.WriteFile(sentinel, []byte(ver), 0o644)
	return root, nil
}

// ---- host (stage 1) ----

// defaultHost resolves the stage-1 host launcher argv, or nil. Preference:
// $YGGDRASIL_HOST / $BIFROST_SHEN_CL, then sibling shen-cl, then sibling
// shen-go. Stage-1 is Shen (not C); shen-c cannot yet load yggdrasil.shen.
func defaultHost() []string {
	for _, env := range []string{"YGGDRASIL_HOST", "BIFROST_SHEN_CL"} {
		if v := os.Getenv(env); v != "" {
			parts := strings.Fields(v)
			if hit := findExecutablePath(parts[0]); hit != "" {
				return append([]string{hit}, parts[1:]...)
			}
		}
	}
	cwd, _ := os.Getwd()
	// Stage-1 host is Shen, not C. shen-c cannot yet load yggdrasil.shen;
	// fall back to sibling shen-go when shen-cl is missing.
	for _, rel := range []string{
		filepath.Join("..", "shen-cl", "bin", "sbcl", "shen"),
		filepath.Join("..", "shen-go", "bin", "shen"),
		filepath.Join("..", "shen-go", "shen"),
	} {
		cand, _ := filepath.Abs(filepath.Join(cwd, rel))
		if hit := findExecutablePath(cand); hit != "" {
			return []string{hit}
		}
	}
	return nil
}

// shakeOpts is everything the command line can say about WHICH shake to run:
// which Shen entry point (trace.go's shakeExpr) and which stage-4 globals to
// set first (prune.go's wrapShakeExpr). One value, passed down the call chain,
// rather than the package-level flags this used to be -- a mode that is a
// global has to be saved and restored by every caller that sets it, is left
// on by any panic in between, and cannot be read off a call site.
//
// The zero value is the default shake, and the default shake's host
// expression is byte-for-byte the one yggdrasil has always sent.
type shakeOpts struct {
	full      bool   // --no-shake: emit the full program A, not the slice
	trace     bool   // --trace: weave the runtime call trace
	pruneInit bool   // --prune-init: stage-4 dead-initialisation pruning
	target    string // "" = no target: the union over all builders
	// --prune-init-unverified: proceed even where --prune-init is refused --
	// a target whose port_reads in builders.json is unknown or declared
	// with nothing checking it, and a target-agnostic shake, whose union is
	// over the declared lists and so covers only the ports in it. See
	// wrapShakeExpr.
	allowUnverifiedPortReads bool
}

// only collapses a variadic shakeOpts to the single value it is allowed to
// carry. The variadic is a default argument, not a list: shake(...) and
// facts(...) keep their old five-argument form for the many callers that want
// the default, and take one shakeOpts when a caller wants something else.
func only(opts []shakeOpts) (shakeOpts, error) {
	switch len(opts) {
	case 0:
		return shakeOpts{}, nil
	case 1:
		return opts[0], nil
	default:
		return shakeOpts{}, fmt.Errorf("internal error: %d shakeOpts passed where at most one is meaningful", len(opts))
	}
}

// shake runs stage 1: shake prog into outdir. Returns outdir.
func shake(prog, outdir string, host []string, evalStyle string, quiet bool, opts ...shakeOpts) (string, error) {
	o, err := only(opts)
	if err != nil {
		return "", err
	}
	return shakeMode(prog, outdir, host, evalStyle, quiet, o)
}

// shakeMode is shake with the mode spelled out. With o.full set it calls
// (yggdrasil.shake-full ...) instead of (yggdrasil.shake ...): every kernel
// defun and the eval-capable initialiser, so the same stage-2 builder can
// build the full program A alongside the shaken A*. Everything else -- the
// host launcher, the driver file, the failure contract -- is shared, so the
// two artifacts differ by the shake and by nothing else.
func shakeMode(prog, outdir string, host []string, evalStyle string, quiet bool, o shakeOpts) (string, error) {
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", fmt.Errorf("no Shen host launcher found. Set $YGGDRASIL_HOST (or $BIFROST_SHEN_CL) to a Shen launcher, e.g.\n  YGGDRASIL_HOST=/path/to/shen-cl/bin/sbcl/shen yggdrasil shake ...")
	}
	prog, _ = filepath.Abs(prog)
	outdir, _ = filepath.Abs(outdir)
	if _, err := os.Stat(prog); err != nil {
		return "", fmt.Errorf("program not found: %s", prog)
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		return "", err
	}
	root, err := yggRoot()
	if err != nil {
		return "", fmt.Errorf("materialising shaker: %w", err)
	}
	// shakeExpr (trace.go) chooses the Shen entry point from the two modes
	// that pick one -- yggdrasil.shake, .shake-full (--no-shake) or
	// .shake-traced (--trace) -- and wrapShakeExpr (prune.go) then wraps
	// whichever it chose with --prune-init's globals. The default path is
	// byte-for-byte the expression this function has always sent.
	expr, err := shakeExpr(prog, outdir, o)
	if err != nil {
		return "", err
	}
	expr, err = wrapShakeExpr(expr, o)
	if err != nil {
		return "", err
	}

	var argv []string
	if evalStyle == "positional" {
		drv := filepath.Join(outdir, "_shake_driver.shen")
		os.WriteFile(drv, []byte("(load \"yggdrasil.shen\")\n"+expr+"\n"), 0o644)
		argv = append(append([]string{}, host...), drv)
	} else {
		argv = append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	}

	out, _ := runAt(wrapExecutable(argv), root)
	kernel := filepath.Join(outdir, "kernel.kl")
	if fi, err := os.Stat(kernel); err != nil || fi.Size() == 0 {
		if sentinel, ok := shakeFailReport(out); ok {
			os.Stderr.WriteString(out)
			return "", fmt.Errorf("%s\n  %s", sentinel, shakeFailHint(sentinel))
		}
		os.Stderr.WriteString(out)
		return "", fmt.Errorf("shake produced no kernel.kl (host=%s)\n  did the program load cleanly on the host?", strings.Join(host, " "))
	}
	if !quiet {
		os.Stderr.WriteString(out)
	}
	return outdir, nil
}

// shakeFailReport picks the shake's own FAIL sentinel out of the host
// output, the way failReport does for the typecheck gate. The shaker
// prints "yggdrasil-shake: FAIL <what> ..." and then aborts before writing
// kernel.kl, so a refused shake reads as a named analysis failure rather
// than as "the program did not load".
func shakeFailReport(out string) (string, bool) {
	i := strings.Index(out, "yggdrasil-shake: FAIL")
	if i < 0 {
		return "", false
	}
	line := out[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return strings.TrimRight(line, "\r"), true
}

// shakeFailHint explains a sentinel in one line; unknown checks get a
// generic line, so a new check in the shaker needs no Go change to be
// reported usefully.
func shakeFailHint(sentinel string) string {
	if strings.Contains(sentinel, "init-order") {
		return "a toplevel form reads a global before any earlier form sets it; reorder the program (see docs/analysis-rules.md)"
	}
	return "the shaker refused this program; no artifacts were written"
}

// check runs the build-time typecheck gate: a separate host process loads
// yggdrasil.shen and evaluates (yggdrasil.check ["prog"]), which loads the
// program's forms under (tc +) with per-form blame. Success is detected by
// the sentinel line, never the exit code (same trust model as shake). The
// separate process is deliberate: the check evaluates the user program into
// the host image, and sharing that image with a shake would let user macros
// leak into bootstrap — two processes make "check passes ⇒ shake output
// unchanged" true by construction. Returns the check host's kernel version
// for manifest recording.
func check(prog string, host []string, evalStyle string) (string, error) {
	out, ver, ok, err := runCheck(prog, host, evalStyle)
	if err != nil {
		return "", err
	}
	if !ok {
		os.Stderr.WriteString(failReport(out))
		if host == nil {
			host = defaultHost()
		}
		return "", fmt.Errorf("typecheck failed: %s (host=%s)", prog, strings.Join(host, " "))
	}
	return ver, nil
}

// failReport trims host output to the check report: everything from the
// first sentinel line onward (progress lines for forms that DID check,
// then the FAIL). Without a sentinel (host crash), the whole output is
// the report.
func failReport(out string) string {
	if i := strings.Index(out, "yggdrasil-check:"); i >= 0 {
		if j := strings.LastIndex(out[:i], "\n"); j >= 0 {
			return out[j+1:]
		}
		return out[i:]
	}
	return out
}

// runCheck drives the host and returns its combined output alongside the
// parsed sentinel, so callers (and tests) can inspect the FAIL report.
func runCheck(prog string, host []string, evalStyle string) (out, ver string, ok bool, err error) {
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", "", false, fmt.Errorf("no Shen host launcher found. Set $YGGDRASIL_HOST (or $BIFROST_SHEN_CL) to a Shen launcher, e.g.\n  YGGDRASIL_HOST=/path/to/shen-cl/bin/sbcl/shen yggdrasil check ...")
	}
	prog, _ = filepath.Abs(prog)
	if _, statErr := os.Stat(prog); statErr != nil {
		return "", "", false, fmt.Errorf("program not found: %s", prog)
	}
	root, err := yggRoot()
	if err != nil {
		return "", "", false, fmt.Errorf("materialising shaker: %w", err)
	}
	expr := fmt.Sprintf(`(yggdrasil.check ["%s"])`, prog)

	var argv []string
	if evalStyle == "positional" {
		tmp, _ := os.MkdirTemp("", "yggdrasil_check_")
		drv := filepath.Join(tmp, "_check_driver.shen")
		os.WriteFile(drv, []byte("(load \"yggdrasil.shen\")\n"+expr+"\n"), 0o644)
		argv = append(append([]string{}, host...), drv)
	} else {
		argv = append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	}

	out, _ = runAt(wrapExecutable(argv), root)
	ver, ok = parseCheckOK(out)
	return out, ver, ok, nil
}

// parseCheckOK scans combined host output for the check sentinel and pulls
// the kernel version out of it. ok is false when no OK sentinel is present
// (a FAIL sentinel, a host crash, or garbage all land here).
func parseCheckOK(out string) (version string, ok bool) {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "yggdrasil-check: OK") {
			continue
		}
		// version= is printed last and may contain spaces ("Shen 41.2"):
		// take the remainder of the line, not a whitespace-split field.
		if i := strings.Index(ln, "version="); i >= 0 {
			version = strings.TrimSpace(ln[i+len("version="):])
		}
		return version, true
	}
	return "", false
}

// appendTypecheckManifest records a passing check in both manifest files.
// Appended from Go rather than threaded into write-manifest because the
// shake host cannot know the check happened (separate process); builders
// must ignore keys they do not recognise, so appending is contract-safe.
// The recorded kernel version is the CHECK host's, which may differ from
// the vendored slice's kernel-version= line — recording both is honest.
func appendTypecheckManifest(outdir string, host []string, kernelVer string) error {
	if host == nil {
		host = defaultHost()
	}
	hostStr := strings.Join(host, " ")
	if kernelVer == "" {
		kernelVer = "?"
	}
	txt := fmt.Sprintf("typechecked=true\ntypecheck-host=%s\ntypecheck-kernel=%s\n", hostStr, kernelVer)
	if err := appendFile(filepath.Join(outdir, "yggdrasil.manifest.txt"), txt); err != nil {
		return err
	}
	sexp := fmt.Sprintf("(\"typechecked\" true)\n(\"typecheck-host\" %q)\n(\"typecheck-kernel\" %q)\n", hostStr, kernelVer)
	return appendFile(filepath.Join(outdir, "yggdrasil.manifest"), sexp)
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}

// runAt runs argv at cwd, returning combined output.
func runAt(argv []string, cwd string) (string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// ---- stage 2 builders ----

type step struct {
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
	// When, if set, is a "key=value" predicate over the shake manifest: the
	// step runs only when the manifest carries that pair.  It exists because a
	// stage-2 builder's correct invocation can depend on what the shake
	// produced -- ShenScript needs --linked for needs-eval=true and must NOT
	// have it otherwise (that mode imports from the ShenScript checkout, so an
	// eval-free artifact built with it silently stops being self-contained).
	// Two mutually exclusive steps express that without inventing per-flag
	// conditional-argument syntax.
	When string `json:"when"`
}

type builder struct {
	RunImpl string   `json:"run_impl"`
	DirEnv  string   `json:"dir_env"`
	Needs   []string `json:"needs"`
	Build   []step   `json:"build"`
	Run     []string `json:"run"`
	// DirDefault is the sibling checkout this target lives in when its
	// DirEnv is unset, e.g. "shen-go". It is here rather than in
	// siblingDir's table because a target whose name is not its checkout's
	// name would otherwise need a line of Go naming the target -- which is
	// the thing `kl` was promoted out of. An empty value falls back to the
	// table, which every older target is still in.
	DirDefault string `json:"dir_default"`
	// ProgramFile, when set, is the file the run's stdin must carry before
	// anything else: a runtime that reads its PROGRAM from stdin (shen-go's
	// cmd/kl) instead of from its build output. Substituted into the build
	// steps as {program_file} so one string names it. Declaring it without
	// the `stdin` fact below would hide the consequence, which is that the
	// caller's stdin is appended to the program rather than delivered to it.
	ProgramFile string `json:"program_file"`
	// Stdin and Stdout are facts about the RUN, in the same three-key shape
	// as the port facts below, and are consulted by trace-check's
	// evidencePossible/checkGolden and by the parity gate instead of any
	// target name. `_default` declares the ordinary contract, so every
	// target states these (by inheritance) rather than being silent:
	//
	//	stdin  "delivered"             the artifact receives the caller's bytes
	//	stdin  "appended-to-program"   the runtime reads its program from stdin,
	//	                               so the caller's bytes land after it
	//	stdout "program"               stdout is the program's output
	//	stdout "repl-transcript"       the program's output is embedded in the
	//	                               runtime's own transcript
	Stdin           string `json:"stdin"`
	StdinSource     string `json:"stdin_source"`
	StdinCheckedBy  string `json:"stdin_checked_by"`
	Stdout          string `json:"stdout"`
	StdoutSource    string `json:"stdout_source"`
	StdoutCheckedBy string `json:"stdout_checked_by"`
	// Level-1 self-description (docs/port-contract.md): facts about the
	// PORT that the Datalog rules and the conformance report consume. Each
	// fact is three flat keys -- the value, `_source` (where it was read
	// off, or "none"), and `_checked_by` (the test that fails when it
	// drifts, or "none"). There is deliberately no `_verified` boolean:
	// one word meant two different predicates on the two facts below, so
	// the word now lives in `yggdrasil contract`'s output and means
	// exactly "_checked_by names a test". See contract.go and prune.go.
	//
	// PortReads: the globals this port's runtime reads natively, with no
	// Shen code mentioning them. Empty means "not declared here"; the
	// `_default` block's value is the literal string "unknown", so a target
	// that declares none resolves to UNKNOWN rather than to a guess. Read it
	// through portReadsFor/effectivePortReads, never directly.
	PortReads          portReadsList `json:"port_reads"`
	PortReadsSource    string        `json:"port_reads_source"`
	PortReadsCheckedBy string        `json:"port_reads_checked_by"`
	// NativeOverrides: the kernel defuns this port rebinds to natives.
	// InstalledAfter records the PHASE, which is the whole content of the
	// fact: on shen-go the generated main runs shen.initialise BEFORE
	// InstallKernelFast, so the kernel's KL bodies do run during boot and
	// the natives replace them only afterwards. An override list with no
	// phase would read as "these KL bodies never run", which is false.
	NativeOverrides               []string `json:"native_overrides"`
	NativeOverridesSource         string   `json:"native_overrides_source"`
	NativeOverridesCheckedBy      string   `json:"native_overrides_checked_by"`
	NativeOverridesInstalledAfter string   `json:"native_overrides_installed_after"`
	// Stage 5 (docs/analysis-rules.md): the KL names this port lowers
	// syntactically, with no symbol lookup, so the generated code never
	// names them and the graph recovered from its output has no edge to
	// them. scip-check subtracts these and whatever only they reach from
	// the shake's footprint, audits the declaration itself, and fails on
	// any other residue. See scip.go. A port that declares none subtracts
	// none.
	SpecialForms       []string `json:"special_forms"`
	SpecialFormsSource string   `json:"special_forms_source"`
}

type capabilityError struct{ message string }

func (e capabilityError) Error() string { return e.message }

// portReadsUnknown is the only string builders.json's port_reads may hold. It
// is a VALUE, not a missing key, because "nobody has measured this port's
// native reads" is a claim the file has to be able to make out loud -- see
// prune.go, and the `_default` block's own comment.
const portReadsUnknown = "unknown"

// portReadsList is a port_reads value: a declared list of globals, or the
// literal "unknown", which unmarshals to the empty list. Any other string is
// an error rather than a quiet unknown, so that a typo ("unkown") cannot turn
// a declaration into a silence -- which is the precise failure mode the
// unknown value exists to make visible.
type portReadsList []string

func (p *portReadsList) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if !strings.EqualFold(strings.TrimSpace(s), portReadsUnknown) {
			return fmt.Errorf("port_reads: the only string value is %q, got %q", portReadsUnknown, s)
		}
		*p = nil
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("port_reads must be a list of globals or the string %q: %w", portReadsUnknown, err)
	}
	*p = list
	return nil
}

// ---- build steps that are logic, not a command line ----

// yggdrasilStep is the argv[0] a build step uses to call one of yggdrasil's
// own helpers instead of a subprocess. It exists for the steps that cannot
// honestly be a shell recipe -- concatenating a KL program in manifest order
// -- so that a target needing one is still an ENTRY in builders.json rather
// than a runner hard-coded in Go and reachable from one subcommand.
const yggdrasilStep = "{yggdrasil}"

// internalSteps are those helpers, by the name a recipe calls them by. Each is
// also reachable as `yggdrasil <name> ARGS...` (see run()), which is how a
// recipe step can be reproduced by hand when it misbehaves.
var internalSteps = map[string]func(args []string) error{
	"program-file": programFileStep,
}

// programFileStep writes OUTDIR's shaken slice as ONE KL stream: the kernel,
// the initialiser call, then the user files in manifest order. That is the
// stage-2 builder contract executed rather than compiled, and it is what a
// runtime that reads its program from stdin has to be handed.
//
// Manifest ORDER is the whole of the difficulty and the reason this is Go: the
// user files must be fed in the order the manifest lists them, and
// (shen.initialise) must come between the kernel and the first of them.
func programFileStep(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: yggdrasil program-file OUTDIR OUTFILE")
	}
	outdir, out := args[0], args[1]
	var body bytes.Buffer
	kern, err := os.ReadFile(filepath.Join(outdir, "kernel.kl"))
	if err != nil {
		return err
	}
	body.Write(kern)
	body.WriteString("\n(shen.initialise)\n")
	users, err := manifestUserFiles(outdir)
	if err != nil {
		return err
	}
	for _, u := range users {
		src, err := os.ReadFile(filepath.Join(outdir, u))
		if err != nil {
			return err
		}
		body.Write(src)
		body.WriteString("\n")
	}
	return os.WriteFile(out, body.Bytes(), 0o644)
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

// builderDefaultsKey is the builders.json block holding the facts a target
// inherits when it states none of its own. It is `_`-prefixed so that every
// loop over the targets skips it; the code that wants it asks by name.
const builderDefaultsKey = "_default"

// parseBuilders returns the per-target blocks and, separately, the `_default`
// block. Splitting them is what lets a fact be absent from a target and still
// resolve: absence is then visible as absence (an empty field) rather than as
// a copy of the default pasted into every entry.
func parseBuilders() (map[string]builder, builder, error) {
	var defaults builder
	b, err := embedded.ReadFile("builders.json")
	if err != nil {
		return nil, defaults, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, defaults, err
	}
	if v, ok := raw[builderDefaultsKey]; ok {
		if err := json.Unmarshal(v, &defaults); err != nil {
			return nil, defaults, fmt.Errorf("builder %s: %w", builderDefaultsKey, err)
		}
	}
	out := map[string]builder{}
	for k, v := range raw {
		if strings.HasPrefix(k, "_") {
			continue
		}
		var bd builder
		if err := json.Unmarshal(v, &bd); err != nil {
			return nil, defaults, fmt.Errorf("builder %s: %w", k, err)
		}
		out[k] = bd
	}
	return out, defaults, nil
}

func loadBuilders() (map[string]builder, error) {
	builders, _, err := parseBuilders()
	return builders, err
}

// evalEntryPoints mirrors *eval-entry-points* in yggdrasil.shen: the calls that
// make a program eval-capable, and so force the compiler into the artifact.
// Kept in sync by hand; only used to explain a failure, never to cause one.
var evalEntryPoints = []string{
	"eval", "eval-kl", "load", "tc", "spy", "track", "step", "it",
	"read", "read-from-string", "lineread", "input", "input+", "bootstrap",
}

// manifestPairs reads the shake manifest as a set of "key=value" lines.  Only
// membership is needed (does the manifest say needs-eval=true?), and repeated
// keys like fn= are common, so a set of whole lines is the honest shape --
// a map[key]value would silently drop all but the last fn=.
func manifestPairs(outdir string) map[string]bool {
	b, err := os.ReadFile(filepath.Join(outdir, "yggdrasil.manifest.txt"))
	if err != nil {
		return nil // no manifest: callers treat this as "cannot tell"
	}
	pairs := map[string]bool{}
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			pairs[ln] = true
		}
	}
	return pairs
}

// webPreflight reports why a --web build cannot proceed, before handing the
// outdir to ShenScript's stage-2 builder. That builder's own message advises
// "re-run with --linked", which is not an option here: --web and --linked are
// mutually exclusive, so a browser target has no valid resolution. Name the
// user-code calls that actually reach eval instead, since those are what the
// author has to remove.
func webPreflight(outdir string) error {
	pairs := manifestPairs(outdir)
	if pairs == nil {
		return nil // no manifest to read: let the builder speak for itself
	}
	if !pairs["needs-eval=true"] {
		return nil
	}
	msg := fmt.Sprintf("--web cannot be built from this program: the manifest reports needs-eval=true.\n"+
		"  An eval-capable program needs the Shen compiler in the artifact, which only --linked\n"+
		"  provides, and --web/--linked are mutually exclusive.\n"+
		"  Manifest: %s", filepath.Join(outdir, "yggdrasil.manifest.txt"))
	if hits := evalCallsInUserKL(outdir); len(hits) > 0 {
		msg += fmt.Sprintf("\n  Reaching eval from your code: %s", strings.Join(hits, ", "))
	}
	return errors.New(msg)
}

// evalCallsInUserKL scans the shaken user KL (everything but kernel.kl) for
// eval entry points, so the error can name them. Token-level and deliberately
// crude: a false positive costs a slightly wrong hint on an already-failed
// build, never a failure of its own.
func evalCallsInUserKL(outdir string) []string {
	entries, err := os.ReadDir(outdir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var hits []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".kl") || e.Name() == "kernel.kl" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(outdir, e.Name()))
		if err != nil {
			continue
		}
		toks := strings.FieldsFunc(string(src), func(r rune) bool {
			return r == '(' || r == ')' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
		})
		for _, t := range toks {
			for _, ep := range evalEntryPoints {
				if t == ep && !seen[t] {
					seen[t] = true
					hits = append(hits, t)
				}
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func siblingDir(target string, b builder) string {
	if b.DirEnv != "" {
		if v := os.Getenv(b.DirEnv); v != "" {
			abs, _ := filepath.Abs(v)
			return abs
		}
	}
	if b.DirDefault != "" {
		cwd, _ := os.Getwd()
		abs, _ := filepath.Abs(filepath.Join(cwd, "..", b.DirDefault))
		return abs
	}
	name := map[string]string{
		"lua": "shen-lua", "go": "shen-go", "rust": "shen-rust",
		"joy": "shen-joy",
		"js":  "ShenScript", "julia": "shen-julia", "scheme": "shen-scheme",
		"swift": "shen-swift", "erlang": "shen-erl", "lisp": "shen-cl", "hvm": "inets/shen-inets",
		"truffle": "shen-truffle", "truffle-native": "shen-truffle",
		"c": "shen-c", "forth": "shen-forth",
	}[target]
	cwd, _ := os.Getwd()
	abs, _ := filepath.Abs(filepath.Join(cwd, "..", name))
	return abs
}

func subst(s string, subs map[string]string) string {
	for k, v := range subs {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}

func joyBinary(b builder) string {
	if value := os.Getenv("SHEN_JOY_BIN"); value != "" {
		return value
	}
	if value, err := exec.LookPath("shen-joy"); err == nil {
		return value
	}
	root := siblingDir("joy", b)
	for _, candidate := range []string{filepath.Join(root, "build", "shen-joy"), filepath.Join(root, "result", "bin", "shen-joy")} {
		if hit := findExecutablePath(candidate); hit != "" {
			return hit
		}
	}
	return "shen-joy"
}

// build runs a target's stage-2 steps. Returns the run argv, or nil if a needed
// tool is missing. When web is true, "--web" is appended to the ShenScript
// stage-2 builder step so it emits a browser-safe ES module (see the js target).
func build(target, outdir string, web bool) ([]string, error) {
	builders, err := loadBuilders()
	if err != nil {
		return nil, err
	}
	b, ok := builders[target]
	if !ok {
		var names []string
		for k := range builders {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown target %q (have: %s)", target, strings.Join(names, ", "))
	}
	for _, tool := range b.Needs {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, nil // tool missing -> caller treats as SKIP
		}
	}
	outdir, _ = filepath.Abs(outdir)
	root, err := yggRoot()
	if err != nil {
		return nil, err
	}
	tmp, _ := os.MkdirTemp("", "yggdrasil_build_")
	subs := map[string]string{
		"{yggroot}": root, "{outdir}": outdir, "{tmp}": tmp,
		"{shen_joy_bin}": joyBinary(b),
		"{shen_lua}":     siblingDir("lua", b), "{shen_go}": siblingDir("go", b),
		"{shen_rust}": siblingDir("rust", b), "{shenscript}": siblingDir("js", b),
		"{shen_julia}": siblingDir("julia", b), "{shen_scheme}": siblingDir("scheme", b),
		"{shen_swift}": siblingDir("swift", b), "{shen_erl}": siblingDir("erlang", b),
		"{shen_cl}":      siblingDir("lisp", b),
		"{shen_inets}":   siblingDir("hvm", b),
		"{shen_truffle}": siblingDir(target, b),
		"{shen_c}":       siblingDir("c", b),
		"{shen_forth}":   siblingDir("forth", b),
	}
	// {program_file} is resolved BEFORE it joins the table, so that the step
	// which writes it and the run that feeds it name one path once.
	subs["{program_file}"] = subst(b.ProgramFile, subs)
	// Steps can be gated on the manifest (see step.When).  Read it once: if a
	// builder has any conditional step and the manifest is unreadable, that is
	// a hard error rather than a silent "run nothing" -- a stage that quietly
	// skips its only build step would report success and produce no artifact.
	pairs := manifestPairs(outdir)
	for _, st := range b.Build {
		if st.When != "" {
			if pairs == nil {
				return nil, fmt.Errorf("target %s has a conditional build step (when=%q) "+
					"but %s is missing or unreadable", target, st.When,
					filepath.Join(outdir, "yggdrasil.manifest.txt"))
			}
			if !pairs[st.When] {
				continue
			}
		}
		argv := make([]string, len(st.Argv))
		for i, a := range st.Argv {
			argv[i] = subst(a, subs)
		}
		// A step that names one of yggdrasil's own helpers runs here, in
		// process: there is no binary to find and nothing to re-exec, so a
		// recipe step works the same from the CLI, from a test, and from
		// Bifrost.
		if len(st.Argv) > 1 && st.Argv[0] == yggdrasilStep {
			fn, ok := internalSteps[st.Argv[1]]
			if !ok {
				return nil, fmt.Errorf("target %s: build step names no such yggdrasil helper: %q",
					target, st.Argv[1])
			}
			if err := fn(argv[2:]); err != nil {
				return nil, fmt.Errorf("build step failed for target %s: %s %s: %w",
					target, st.Argv[1], strings.Join(argv[2:], " "), err)
			}
			continue
		}
		// --web is a pass-through to ShenScript's stage-2 builder: emit a
		// browser-safe ES module instead of the default Node artifact.
		if web {
			for _, a := range argv {
				if strings.Contains(a, "yggdrasil-build.js") {
					argv = append(argv, "--web")
					break
				}
			}
		}
		cwd := ""
		if st.Cwd != "" {
			cwd = subst(st.Cwd, subs)
		}
		cmd := exec.Command(wrapExecutable(argv)[0], wrapExecutable(argv)[1:]...)
		cmd.Dir = cwd
		cmd.Env = os.Environ()
		for k, v := range st.Env {
			cmd.Env = append(cmd.Env, k+"="+subst(v, subs))
		}
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 3 {
				return nil, capabilityError{message: fmt.Sprintf("target %s does not support this program", target)}
			}
			return nil, fmt.Errorf("build step failed for target %s: %s", target, strings.Join(argv, " "))
		}
	}
	runArgv := make([]string, len(b.Run))
	for i, a := range b.Run {
		runArgv[i] = subst(a, subs)
	}
	// Native-exe run path (e.g. {outdir}/app-go-bin) is app-go-bin.exe on Windows.
	if len(runArgv) > 0 && strings.ContainsAny(runArgv[0], `/\`) {
		if hit := findExecutablePath(runArgv[0]); hit != "" {
			runArgv[0] = hit
		}
	}
	return runArgv, nil
}

// ---- CLI ----

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil <shake|build|run|check|why|facts|trace-check|parity|scip-check|contract|targets> ...")
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "targets":
		builders, err := loadBuilders()
		if err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil:", err)
			return 1
		}
		var names []string
		for k := range builders {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, t := range names {
			b := builders[t]
			fmt.Printf("%-6s runs on %-10s needs %s\n", t, b.RunImpl, strings.Join(b.Needs, ", "))
		}
		return 0
	case "shake", "build", "run":
		return cmdStage(cmd, rest)
	case "check":
		return cmdCheck(rest)
	case "why":
		return cmdWhy(rest)
	case "facts":
		return cmdFacts(rest)
	case "trace-check":
		return cmdTraceCheck(rest)
	case "parity":
		return cmdParity(rest)
	case "scip-check":
		return cmdScipCheck(rest)
	case "contract":
		return cmdContract(rest)
	default:
		// The build helpers a builders.json recipe invokes as
		// {yggdrasil} <name>. Reachable here so that a step which
		// misbehaved can be re-run by hand on the same outdir; the
		// builders run them in process, never through this path.
		if fn, ok := internalSteps[cmd]; ok {
			if err := fn(rest); err != nil {
				fmt.Fprintln(os.Stderr, "yggdrasil:", err)
				return 1
			}
			return 0
		}
		fmt.Fprintf(os.Stderr, "yggdrasil: unknown subcommand %q\n", cmd)
		return 2
	}
}

func cmdStage(cmd string, rest []string) int {
	fs := flag.NewFlagSet("yggdrasil "+cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	target := fs.String("target", "", "stage-2 target (yggdrasil targets lists them: lisp/lua/go/kl/joy/rust/js/julia/scheme/swift/erlang/truffle/truffle-native/c)")
	web := fs.Bool("web", false, "with --target js: emit a browser-safe ES module (passes --web to ShenScript's builder)")
	typecheck := fs.Bool("typecheck", false, "typecheck PROG under (tc +) on the host before shaking; failure aborts with no artifacts, success is recorded as typechecked= in the manifest")
	trace := fs.Bool("trace", false, "weave runtime call tracing into the emitted KL: every defun records its entry and every (value V) its read, to ./"+traceFileName+" at run time (see yggdrasil trace-check)")
	pruneFlag := fs.Bool("prune-init", false, "stage 4: drop toplevel (set V Lit) forms whose global nothing reads, using --target's port_reads from builders.json; refused unless that target's list is checked, and refused with no --target too while any target's reads are unknown (see --prune-init-unverified); recorded as pruned-init= in the manifest")
	pruneUnverified := fs.Bool("prune-init-unverified", false, "allow --prune-init where it is refused -- a target whose builders.json port_reads is unknown (nobody measured that runtime) or declared with no test naming it, or no --target at all; prints a WARN and prunes against the union over the targets that have declared a list")
	noShake := fs.Bool("no-shake", false, "emit the FULL program (every kernel defun, the eval-capable initialiser, no trimming) instead of the shaken slice; the manifest records shaken=false. The reference build for scip-check")
	// Allow flags after the PROG/OUTDIR positionals (Go's flag stops at the
	// first non-flag token otherwise).
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target")); err != nil {
		return 2
	}
	// The whole mode of this stage-1 run, decided once, here, and passed to
	// shakeMode as a value. --trace changes only what shake() asks the host
	// for; every other stage is unaware, because a woven artifact is
	// ordinary KL.
	//
	// The target is carried only when --prune-init asks for it: a shake has
	// no target, so it must use the union of every port's reads; a
	// build/run knows which backend the slice is for and may use just that
	// one. Without --prune-init the list is not consulted at all, and
	// naming it would change the host expression for every plain build.
	opts := shakeOpts{
		full:                     *noShake,
		trace:                    *trace,
		pruneInit:                *pruneFlag,
		allowUnverifiedPortReads: *pruneUnverified,
	}
	if *pruneFlag {
		opts.target = *target
	}
	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "usage: yggdrasil %s PROG OUTDIR%s\n", cmd, map[string]string{"shake": ""}[cmd]+ifTarget(cmd))
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}
	// --typecheck gates the shake: check first in its own host process, so a
	// type failure aborts before any artifact is written.
	var checkedKernel string
	if *typecheck {
		ver, err := check(prog, host, *evalStyle)
		if err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil:", err)
			return 1
		}
		checkedKernel = ver
	}

	if cmd == "shake" {
		out, err := shakeMode(prog, outdir, host, *evalStyle, false, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil:", err)
			return 1
		}
		if *typecheck {
			if err := appendTypecheckManifest(out, host, checkedKernel); err != nil {
				fmt.Fprintln(os.Stderr, "yggdrasil: recording typecheck in manifest:", err)
				return 1
			}
		}
		fmt.Println("shaken ->", out)
		entries, _ := os.ReadDir(out)
		for _, e := range entries {
			fmt.Println("  " + e.Name())
		}
		return 0
	}

	// build / run
	if *target == "" {
		fmt.Fprintf(os.Stderr, "yggdrasil %s: --target is required\n", cmd)
		return 2
	}
	if *web && *target != "js" {
		fmt.Fprintf(os.Stderr, "yggdrasil %s: --web only applies to --target js\n", cmd)
		return 2
	}
	if _, err := shakeMode(prog, outdir, host, *evalStyle, true, opts); err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	if *typecheck {
		abs, _ := filepath.Abs(outdir)
		if err := appendTypecheckManifest(abs, host, checkedKernel); err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil: recording typecheck in manifest:", err)
			return 1
		}
	}
	if *web {
		if err := webPreflight(outdir); err != nil {
			fmt.Fprintf(os.Stderr, "yggdrasil %s: %s\n", cmd, err)
			return 1
		}
	}
	runArgv, err := build(*target, outdir, *web)
	if err != nil {
		var unsupported capabilityError
		if errors.As(err, &unsupported) {
			fmt.Fprintln(os.Stderr, "yggdrasil:", err)
			return 3
		}
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	if runArgv == nil {
		fmt.Fprintf(os.Stderr, "yggdrasil: target %q skipped (a required tool is not on PATH)\n", *target)
		return 3
	}
	if cmd == "build" {
		fmt.Printf("built %s artifact; run with:\n  %s\n", *target, strings.Join(runArgv, " "))
		return 0
	}
	// run
	argv := wrapExecutable(runArgv)
	c := exec.Command(argv[0], argv[1:]...)
	// A target whose runtime reads its program from stdin gets that program
	// first and the user's own stdin after it -- which is the declared
	// `stdin: appended-to-program` fact, and is why what the user types
	// reaches the runtime as further toplevel forms rather than the program.
	progFile, err := programFileFor(*target, outdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	in, closeIn, err := openRunStdin(progFile, "", os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	defer closeIn()
	c.Stdin, c.Stdout, c.Stderr = in, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	return 0
}

// cmdCheck is the standalone form of the typecheck gate: no outdir, no
// shake, no manifest — fast feedback for iterating on typed sources.
func cmdCheck(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the check expr (sub | positional)")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style")); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil check PROG [--host ...] [--eval-style ...]")
		return 2
	}
	prog := fs.Arg(0)
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}
	ver, err := check(prog, host, *evalStyle)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	if ver == "" {
		ver = "?"
	}
	fmt.Printf("typechecked %s (kernel %s)\n", prog, ver)
	return 0
}

// ---- footprint attribution (why) ----

// runWhy drives the host through (yggdrasil.why ["prog"]) - or why-trace
// when target is non-empty - and returns the report: host output from the
// first sentinel line on. Like check it runs in its own host process, and
// like check the sentinel, not the exit code, decides whether it worked.
func runWhy(prog string, target string, host []string, evalStyle string) (string, error) {
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", fmt.Errorf("no Shen host launcher found. Set $YGGDRASIL_HOST (or $BIFROST_SHEN_CL) to a Shen launcher, e.g.\n  YGGDRASIL_HOST=/path/to/shen-cl/bin/sbcl/shen yggdrasil why ...")
	}
	prog, _ = filepath.Abs(prog)
	if _, statErr := os.Stat(prog); statErr != nil {
		return "", fmt.Errorf("program not found: %s", prog)
	}
	root, err := yggRoot()
	if err != nil {
		return "", fmt.Errorf("materialising shaker: %w", err)
	}
	expr := fmt.Sprintf(`(yggdrasil.why ["%s"])`, prog)
	if target != "" {
		expr = fmt.Sprintf(`(yggdrasil.why-trace ["%s"] %s)`, prog, target)
	}

	var argv []string
	if evalStyle == "positional" {
		tmp, _ := os.MkdirTemp("", "yggdrasil_why_")
		drv := filepath.Join(tmp, "_why_driver.shen")
		os.WriteFile(drv, []byte("(load \"yggdrasil.shen\")\n"+expr+"\n"), 0o644)
		argv = append(append([]string{}, host...), drv)
	} else {
		argv = append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	}

	out, _ := runAt(wrapExecutable(argv), root)
	report, ok := whyReport(out)
	if !ok {
		os.Stderr.WriteString(out)
		return "", fmt.Errorf("why produced no report (host=%s)\n  did the program load cleanly on the host?", strings.Join(host, " "))
	}
	return report, nil
}

// whyReport cuts host output down to the report: the sentinel line and
// every line after it, minus the host's trailing "done" echo.
func whyReport(out string) (string, bool) {
	i := strings.Index(out, "yggdrasil-why:")
	if i < 0 {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(out[i:], "\n"), "\n")
	if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) == "done" {
		lines = lines[:n-1]
	}
	return strings.Join(lines, "\n") + "\n", true
}

func cmdWhy(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil why", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the why expr (sub | positional)")
	trace := fs.String("trace", "", "also print the shortest call chain from the program to this kernel function")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "trace")); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil why PROG [--trace FN] [--host ...] [--eval-style ...]")
		return 2
	}
	prog := fs.Arg(0)
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}
	report, err := runWhy(prog, *trace, host, *evalStyle)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	os.Stdout.WriteString(report)
	return 0
}

// ---- facts: the Datalog oracle's input ----
//
// `yggdrasil facts PROG OUTDIR` runs (yggdrasil.facts ...) on the host,
// which writes one TSV file per relation in analysis/analysis.dl. Nothing
// about the shake changes: the fact dump is a sibling entry point that
// reuses the same pipeline and writes no artifact. The relations are the
// input to Souffle (`souffle -F OUTDIR -D out analysis/analysis.dl`) in CI
// and to analysis/refeval.py locally; both must compute a `reach` set equal
// to kernel.kl's defun list minus the synthesised shen.initialise. See
// docs/analysis-rules.md.
//
// Same trust model as shake and why: success is the sentinel line plus the
// fact files existing on disk, never the host's exit code.
func facts(prog, outdir string, host []string, evalStyle string, quiet bool, opts ...shakeOpts) (string, error) {
	o, oerr := only(opts)
	if oerr != nil {
		return "", oerr
	}
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", fmt.Errorf("no Shen host launcher found. Set $YGGDRASIL_HOST (or $BIFROST_SHEN_CL) to a Shen launcher, e.g.\n  YGGDRASIL_HOST=/path/to/shen-cl/bin/sbcl/shen yggdrasil facts ...")
	}
	prog, _ = filepath.Abs(prog)
	outdir, _ = filepath.Abs(outdir)
	if _, err := os.Stat(prog); err != nil {
		return "", fmt.Errorf("program not found: %s", prog)
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		return "", err
	}
	root, err := yggRoot()
	if err != nil {
		return "", fmt.Errorf("materialising shaker: %w", err)
	}
	expr := fmt.Sprintf(`(yggdrasil.facts ["%s"] "%s")`, prog, outdir)
	expr, err = wrapShakeExpr(expr, o)
	if err != nil {
		return "", err
	}

	var argv []string
	if evalStyle == "positional" {
		drv := filepath.Join(outdir, "_facts_driver.shen")
		os.WriteFile(drv, []byte("(load \"yggdrasil.shen\")\n"+expr+"\n"), 0o644)
		argv = append(append([]string{}, host...), drv)
	} else {
		argv = append(append([]string{}, host...), "eval", "-q", "-l", "yggdrasil.shen", "-e", expr)
	}

	out, _ := runAt(wrapExecutable(argv), root)
	if !strings.Contains(out, "yggdrasil-facts:") {
		os.Stderr.WriteString(out)
		return "", fmt.Errorf("facts produced no dump (host=%s)\n  did the program load cleanly on the host?", strings.Join(host, " "))
	}
	// Every relation analysis.dl declares .input for must exist, even when
	// empty: Souffle errors on a missing fact file, and a silently absent
	// relation would quietly shrink reach.
	for _, rel := range factRelations {
		if _, err := os.Stat(filepath.Join(outdir, rel+".facts")); err != nil {
			os.Stderr.WriteString(out)
			return "", fmt.Errorf("facts dump is missing %s.facts", rel)
		}
	}
	if !quiet {
		os.Stderr.WriteString(out)
	}
	return outdir, nil
}

// factRelations is the .input set of analysis/analysis.dl, in declaration
// order. Keep the two in step.
var factRelations = []string{
	"kernel", "callpos", "argpos", "datasym", "mentionsprim",
	"top", "formmentions", "formmentionsef",
	"rawsym", "usersym", "entry", "prim", "cap", "portGlobal", "initprim",
	"userintern", "userglobal",
	"readsIn", "reads", "writes", "portReads",
	"defwrite", "fcall", "formcalls", "formwrite", "succ",
	"defunwrite", "called", "readglobal",
}

func cmdFacts(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil facts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the facts expr (sub | positional)")
	tgt := fs.String("target", "", "dump portReads for this target's runtime instead of the shaker's conservative default")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target")); err != nil {
		return 2
	}
	// --target chooses which port_reads list lands in portReads.facts and
	// asks for nothing else: pruneInit stays false, and yggdrasil.facts
	// never prunes anyway.
	opts := shakeOpts{target: *tgt}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil facts PROG OUTDIR [--host ...] [--eval-style ...] [--target T]")
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}
	if _, err := facts(prog, outdir, host, *evalStyle, false, opts); err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	return 0
}

// ---- parity gate ----
//
// Byte-identical kernel.kl across hosts is necessary but not sufficient: the
// SAME KL can still execute differently per target (integer width, symbol
// interning, hash iteration order, memoisation growth). The parity gate runs a
// shaken slice through each stage-2 target and checks the rendered output
// against a reference (and against itself), catching divergence that the
// byte-identity check cannot. See docs/parity.md and GitHub issue #8.

// passSep is the convention separator: a parity fixture prints two identical
// passes (the same computation run twice in one process) separated by a line
// that is exactly "===". splitPasses lets the gate diff the two passes, which
// catches in-process boot-order / state-dependent nondeterminism.
const passSep = "==="

// canon normalises line endings and strips trailing blank lines so artifacts
// that differ only in CRLF or a trailing newline compare equal.
func canon(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// splitPasses splits output on the first line equal to passSep, returning the
// two canonicalised halves. ok is false when the marker is absent (the
// two-pass check is then reported as N/A, not a failure).
func splitPasses(out string) (a, b string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == passSep {
			return canon(strings.Join(lines[:i], "\n")),
				canon(strings.Join(lines[i+1:], "\n")), true
		}
	}
	return "", "", false
}

// firstDiff returns the 1-based number of the first differing line between x
// and y (already canonicalised by the caller) and the two lines' contents
// ("" past the end). line is 0 when x == y.
func firstDiff(x, y string) (line int, xs, ys string) {
	xl, yl := strings.Split(x, "\n"), strings.Split(y, "\n")
	n := len(xl)
	if len(yl) > n {
		n = len(yl)
	}
	at := func(s []string, i int) string {
		if i < len(s) {
			return s[i]
		}
		return ""
	}
	for i := 0; i < n; i++ {
		if at(xl, i) != at(yl, i) {
			return i + 1, at(xl, i), at(yl, i)
		}
	}
	return 0, "", ""
}

// runCapture runs an artifact and returns its stdout (stderr passes through, so
// runtime errors are visible but never part of the comparison).  stdin is the
// contents of stdinFile, or empty when it is "" -- never the parent's stdin, so
// a run is reproducible and a program that reads to EOF terminates.
//
// Feeding stdin at all is what lets an eval-free CLI be gated: a driver that
// reads BYTES (read-byte) instead of an S-expression (read, an eval entry
// point) keeps needs-eval=false, and tests/stdin-sum.shen exercises exactly
// that.  Both boots get identical bytes, so two-boot and two-pass stay
// meaningful.
// ---- the declared run facts (builders.json: stdin, stdout, program_file) ----

// The two values each run fact may take. They are constants because three
// files consult them and a typo in one of them would silently restore the
// behaviour the fact exists to describe.
const (
	stdinDelivered         = "delivered"           // the artifact gets the caller's bytes
	stdinAppendedToProgram = "appended-to-program" // the runtime reads its program from stdin
	stdoutProgram          = "program"             // stdout is the program's output
	stdoutTranscript       = "repl-transcript"     // the program's output is embedded in one
)

// runFacts resolves a target's stdin/stdout facts, inheriting `_default`'s
// values the way the port facts inherit. An unknown target yields the defaults
// and no error: naming a target that does not exist is build()'s failure to
// report, with the list of targets that do, and duplicating that refusal in
// every caller of this function is how two spellings of "unknown target" get
// into one tool.
func runFacts(target string) (stdin, stdout string) {
	stdin, stdout = stdinDelivered, stdoutProgram
	builders, defaults, err := parseBuilders()
	if err != nil {
		return
	}
	pick := func(own, def string) string {
		if own != "" {
			return own
		}
		if def != "" {
			return def
		}
		return ""
	}
	b := builders[target]
	if v := pick(b.Stdin, defaults.Stdin); v != "" {
		stdin = v
	}
	if v := pick(b.Stdout, defaults.Stdout); v != "" {
		stdout = v
	}
	return
}

// programFileFor is the path a target's run must feed to stdin before anything
// else, or "" for every target whose artifact carries its own program. The
// only placeholder a program_file may use is {outdir}: it names a file in the
// shake output, and a step that wrote it somewhere else would be a file the
// run could not find.
func programFileFor(target, outdir string) (string, error) {
	builders, err := loadBuilders()
	if err != nil {
		return "", err
	}
	b, ok := builders[target]
	if !ok || b.ProgramFile == "" {
		return "", nil
	}
	abs, _ := filepath.Abs(outdir)
	path := subst(b.ProgramFile, map[string]string{"{outdir}": abs})
	if strings.ContainsAny(path, "{}") {
		return "", fmt.Errorf("target %s: program_file %q uses a placeholder other than {outdir}",
			target, b.ProgramFile)
	}
	return path, nil
}

// openRunStdin builds the stdin a run receives: the target's program file
// first when it has one, then the caller's bytes (a --stdin file, or `rest`
// for an interactive `yggdrasil run`). The concatenation IS the
// "appended-to-program" fact: on such a runtime the caller's bytes are read as
// further toplevel forms, not by the program, which is why trace-check refuses
// to call such a run evidence for a fixture that ships stdin.
//
// Returns a nil reader when there is nothing to feed, which exec reads as
// /dev/null -- the same as before either file existed.
func openRunStdin(programFile, stdinFile string, rest io.Reader) (io.Reader, func(), error) {
	var readers []io.Reader
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	open := func(path, what string) error {
		f, err := os.Open(path)
		if err != nil {
			closeAll()
			return fmt.Errorf("cannot read %s file: %w", what, err)
		}
		files = append(files, f)
		readers = append(readers, f)
		return nil
	}
	if programFile != "" {
		if err := open(programFile, "program_file"); err != nil {
			return nil, func() {}, err
		}
	}
	if stdinFile != "" {
		if err := open(stdinFile, "--stdin"); err != nil {
			return nil, func() {}, err
		}
	}
	if rest != nil {
		readers = append(readers, rest)
	}
	switch len(readers) {
	case 0:
		return nil, closeAll, nil
	case 1:
		return readers[0], closeAll, nil
	default:
		return io.MultiReader(readers...), closeAll, nil
	}
}

func runCapture(argv []string, programFile, stdinFile string) (string, int64, error) {
	a := wrapExecutable(argv)
	cmd := exec.Command(a[0], a[1:]...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	in, closeIn, err := openRunStdin(programFile, stdinFile, nil)
	if err != nil {
		return "", 0, err
	}
	defer closeIn()
	cmd.Stdin = in
	start := time.Now()
	err = cmd.Run()
	return out.String(), time.Since(start).Milliseconds(), err
}

type parityResult struct {
	target string
	status string // ok | skip | skipfact | builderr | runerr
	outA   string
	outB   string
	runMs  int64
	err    error
	// skip is the NAME of a declared fact that made this target
	// unrunnable on this fixture (builders.json's `stdin`), so the gate
	// line says which fact rather than "SKIP".
	skip string
	// transcript records the `stdout: repl-transcript` fact: this target's
	// stdout carries the runtime's own prompts and echoes around the
	// program's output, so the golden is checked by CONTAINMENT. The
	// two-boot and two-pass comparisons are unaffected -- a transcript is
	// still required to be identical to itself.
	transcript bool
}

// parityVerdict is the three comparisons the gate makes on one target's two
// boots, extracted from the report loop so that the softenings a declared
// `stdout: repl-transcript` buys can be tested without a toolchain. They were
// inline, which meant the only way to check them was to build shen-go's VM.
type parityVerdict struct {
	vsTruth   bool
	twoBoot   bool
	twoPass   bool
	hasPasses bool
	pass1     string
	pass2     string
}

// compareParity decides those three, for an ordinary target by EQUALITY and
// for one that declares `stdout: repl-transcript` by CONTAINMENT of the truth.
//
// Why containment on every leg there, rather than equality on the legs that
// compare a transcript with itself: shen-go's cmd/kl prints its own prompts
// and echoes, and ends a program by recovering a panic and printing the dump,
// goroutine addresses included. Two boots of an identical program are
// therefore never byte-identical, and the two halves of a two-pass fixture are
// not comparable with each other either -- the runtime's lines are interleaved
// with the program's and differ between the halves even when the program
// printed the same thing twice. Demanding equality makes such a target
// permanently red, which teaches a reader to ignore the column. What the
// fixture actually promises is that each boot, and each pass, printed the
// truth, and that still fails loudly when one of them does not.
func compareParity(r *parityResult, truth string) parityVerdict {
	a, b := canon(r.outA), canon(r.outB)
	p1, p2, hasPasses := splitPasses(r.outA)
	v := parityVerdict{
		vsTruth:   a == truth,
		twoBoot:   a == b,
		twoPass:   !hasPasses || p1 == p2,
		hasPasses: hasPasses,
		pass1:     p1,
		pass2:     p2,
	}
	if !r.transcript {
		return v
	}
	v.vsTruth = strings.Contains(a, truth)
	v.twoBoot = strings.Contains(b, truth)
	if hasPasses {
		t1, t2, truthHasPasses := splitPasses(truth)
		v.hasPasses = truthHasPasses
		v.twoPass = truthHasPasses && strings.Contains(p1, t1) && strings.Contains(p2, t2)
	}
	return v
}

func cmdParity(rest []string) int {
	fs := flag.NewFlagSet("yggdrasil parity", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hostFlag := fs.String("host", "", `stage-1 host launcher (e.g. "node /p/shen.js"); default: shen-cl`)
	evalStyle := fs.String("eval-style", "sub", "how the host evaluates the shake expr (sub | positional)")
	targetFlag := fs.String("target", "", "comma-separated targets to check (default: all whose tools are on PATH)")
	reference := fs.String("reference", "lisp", "reference target whose output is the truth source")
	expect := fs.String("expect", "", "golden stdout file; when given it is the authoritative truth")
	timeFlag := fs.Bool("time", false, "report per-target wall-clock (advisory; never fails the gate)")
	stdinFile := fs.String("stdin", "", "file fed to each artifact's stdin (both boots get the same bytes)")
	pruneFlag := fs.Bool("prune-init", false, "stage 4: shake with dead-initialisation pruning on (the union over the ports that have DECLARED a port_reads list), then gate the pruned slice on every target")
	pruneUnverified := fs.Bool("prune-init-unverified", false, "allow --prune-init where it is refused -- a single target whose builders.json port_reads is unknown or unchecked, or no single --target at all; prints a WARN and prunes against the union over the targets that have declared a list")
	if err := fs.Parse(reorderArgs(rest, "host", "eval-style", "target", "reference", "expect", "stdin")); err != nil {
		return 2
	}
	// One named target means the slice is only ever built for that backend,
	// so its own port_reads is the honest list; several (or none) means the
	// union, the only list sound for all of them.
	opts := shakeOpts{pruneInit: *pruneFlag, allowUnverifiedPortReads: *pruneUnverified}
	if *pruneFlag && *targetFlag != "" && !strings.Contains(*targetFlag, ",") {
		opts.target = strings.TrimSpace(*targetFlag)
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: yggdrasil parity PROG OUTDIR [--target a,b] [--reference R] [--expect FILE]")
		return 2
	}
	prog, outdir := fs.Arg(0), fs.Arg(1)
	var host []string
	if *hostFlag != "" {
		host = strings.Fields(*hostFlag)
		if hit := findExecutablePath(host[0]); hit != "" {
			host[0] = hit
		}
	}

	builders, err := loadBuilders()
	if err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}

	// Selected targets: explicit list, or every known target.
	var targets []string
	if *targetFlag != "" {
		for _, t := range strings.Split(*targetFlag, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if _, ok := builders[t]; !ok {
				fmt.Fprintf(os.Stderr, "yggdrasil: unknown target %q\n", t)
				return 2
			}
			targets = append(targets, t)
		}
	} else {
		for t := range builders {
			targets = append(targets, t)
		}
	}
	sort.Strings(targets)
	// Without a golden, the reference target must be built to supply the truth.
	if *expect == "" && !contains(targets, *reference) {
		if _, ok := builders[*reference]; !ok {
			fmt.Fprintf(os.Stderr, "yggdrasil: unknown reference target %q\n", *reference)
			return 2
		}
		targets = append([]string{*reference}, targets...)
	}

	// Stage 1, once.
	if _, err := shake(prog, outdir, host, *evalStyle, true, opts); err != nil {
		fmt.Fprintln(os.Stderr, "yggdrasil:", err)
		return 1
	}
	outdir, _ = filepath.Abs(outdir)

	// Build + run each target twice (two boots), in deterministic order.
	results := map[string]*parityResult{}
	for _, t := range targets {
		r := &parityResult{target: t}
		results[t] = r
		// The declared run facts, not the target's name: a runtime that
		// reads its program from stdin cannot also be handed the fixture's
		// stdin, so it is skipped BY NAME rather than run against bytes it
		// will parse as toplevel forms. trace-check refuses the same pair
		// for the same reason (trace.go's evidencePossible).
		stdinFact, stdoutFact := runFacts(t)
		if stdinFact == stdinAppendedToProgram && *stdinFile != "" {
			r.status, r.skip = "skipfact", stdinAppendedToProgram
			continue
		}
		r.transcript = stdoutFact == stdoutTranscript
		runArgv, berr := build(t, outdir, false)
		if berr != nil {
			r.status, r.err = "builderr", berr
			continue
		}
		if runArgv == nil {
			r.status = "skip"
			continue
		}
		progFile, perr := programFileFor(t, outdir)
		if perr != nil {
			r.status, r.err = "builderr", perr
			continue
		}
		outA, ms, ea := runCapture(runArgv, progFile, *stdinFile)
		outB, _, eb := runCapture(runArgv, progFile, *stdinFile)
		r.outA, r.outB, r.runMs = outA, outB, ms
		if ea != nil || eb != nil {
			r.status = "runerr"
			if ea != nil {
				r.err = ea
			} else {
				r.err = eb
			}
			continue
		}
		r.status = "ok"
	}

	// Establish the truth output.
	var truth, truthSrc string
	if *expect != "" {
		b, err := os.ReadFile(*expect)
		if err != nil {
			fmt.Fprintln(os.Stderr, "yggdrasil: cannot read --expect file:", err)
			return 1
		}
		truth, truthSrc = canon(string(b)), "expect:"+filepath.Base(*expect)
	} else {
		ref := results[*reference]
		if ref == nil || ref.status != "ok" {
			fmt.Fprintf(os.Stderr, "yggdrasil: cannot establish a reference: target %q is %s "+
				"(install its toolchain or pass --expect FILE)\n", *reference,
				statusOr(ref, "unavailable"))
			return 1
		}
		truth, truthSrc = canon(ref.outA), "reference:"+*reference
	}

	// Report.
	fmt.Printf("parity gate: %s  (truth = %s)\n", filepath.Base(prog), truthSrc)
	fmt.Printf("%-8s %-6s %-9s %-9s %-8s\n", "target", "build", "vs-truth", "two-boot", "two-pass")
	fail := false
	checked := 0
	for _, t := range targets {
		r := results[t]
		switch r.status {
		case "skip":
			fmt.Printf("%-8s %-6s %s\n", t, "SKIP", "(toolchain not on PATH)")
			continue
		case "skipfact":
			fmt.Printf("%-8s %-6s %s\n", t, "SKIP", "(builders.json declares stdin="+r.skip+
				": this runtime reads its program from stdin, so the fixture's stdin bytes "+
				"would be read as toplevel forms and the program would see EOF)")
			continue
		case "builderr":
			fmt.Printf("%-8s %-6s build failed: %v\n", t, "FAIL", r.err)
			fail = true
			continue
		case "runerr":
			fmt.Printf("%-8s %-6s run failed: %v\n", t, "FAIL", r.err)
			fail = true
			continue
		}
		checked++
		a := canon(r.outA)
		v := compareParity(r, truth)
		vsTruth, twoBoot, twoPass, hasPasses := v.vsTruth, v.twoBoot, v.twoPass, v.hasPasses
		p1, p2 := v.pass1, v.pass2
		tp := "N/A"
		if hasPasses {
			if twoPass {
				tp = "ok"
			} else {
				tp = "DIFFER"
			}
		}
		mark := func(b bool) string {
			if b {
				return "ok"
			}
			return "DIFFER"
		}
		extra := ""
		if *timeFlag {
			extra = fmt.Sprintf("  %dms", r.runMs)
		}
		if r.transcript {
			extra += "  (every leg by containment of the truth in the transcript: " +
				"builders.json declares stdout=" + stdoutTranscript + ", which carries the " +
				"runtime's own lines and is not byte-stable)"
		}
		fmt.Printf("%-8s %-6s %-9s %-9s %-8s%s\n", t, "ok", mark(vsTruth), mark(twoBoot), tp, extra)
		if !vsTruth && r.transcript {
			fmt.Printf("    vs-truth: the transcript does not contain the truth output\n"+
				"      want (somewhere in it): %q\n      got the whole transcript:  %q\n", truth, a)
		} else if !vsTruth {
			ln, xs, ys := firstDiff(a, truth)
			fmt.Printf("    vs-truth first diff @ line %d:\n      got:  %q\n      want: %q\n", ln, xs, ys)
		}
		if !twoBoot && r.transcript {
			fmt.Printf("    two-boot: the second boot's transcript does not contain the truth output\n"+
				"      want (somewhere in it): %q\n      got the whole transcript:  %q\n", truth, canon(r.outB))
		} else if !twoBoot {
			ln, xs, ys := firstDiff(a, canon(r.outB))
			fmt.Printf("    two-boot first diff @ line %d:\n      bootA: %q\n      bootB: %q\n", ln, xs, ys)
		}
		if hasPasses && !twoPass && r.transcript {
			t1, t2, _ := splitPasses(truth)
			fmt.Printf("    two-pass: a half of the transcript does not contain the truth's\n"+
				"      pass1 wants: %q\n      pass1 got:   %q\n"+
				"      pass2 wants: %q\n      pass2 got:   %q\n", t1, p1, t2, p2)
		} else if hasPasses && !twoPass {
			ln, xs, ys := firstDiff(p1, p2)
			fmt.Printf("    two-pass first diff @ line %d:\n      pass1: %q\n      pass2: %q\n", ln, xs, ys)
		}
		if !(vsTruth && twoBoot && twoPass) {
			fail = true
		}
	}
	if fail {
		fmt.Println("parity: FAIL")
		return 1
	}
	if checked == 0 {
		fmt.Println("parity: no targets checked (toolchains missing)")
		return 3
	}
	fmt.Printf("parity: PASS (%d target(s) checked)\n", checked)
	return 0
}

func contains(xs []string, x string) bool {
	for _, e := range xs {
		if e == x {
			return true
		}
	}
	return false
}

func statusOr(r *parityResult, dflt string) string {
	if r == nil {
		return dflt
	}
	return r.status
}

func ifTarget(cmd string) string {
	if cmd == "shake" {
		return ""
	}
	return " --target T"
}

// reorderArgs moves flags (and the values of named value-flags) ahead of
// positionals so flags may appear after PROG/OUTDIR. Mirrors bifrost's helper.
func reorderArgs(args []string, valueFlags ...string) []string {
	vf := map[string]bool{}
	for _, f := range valueFlags {
		vf[f] = true
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			} else if vf[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}
