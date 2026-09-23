package main

// Host-gated tests for the ygg.dl engine's internals, the two properties a
// Datalog can get silently wrong:
//
//   - a logic variable is the marked term ygg.dl-varify produces, never a
//     symbol that merely happens to be spelled with a leading uppercase.
//     Deciding varness by spelling made any such fact argument a wildcard:
//     it unified with anything and took ygg.dl-cands off the first-argument
//     index onto a full relation scan.
//   - a [not P] is legal only where P is already CLOSED - an EDB relation,
//     or the head of an earlier stratum.  Checking only the current
//     stratum's heads let a [not P] whose P is derived LATER read an empty
//     relation and answer confidently.
//
// (yggdrasil.dl-selftest) is the test-only entry point that asserts both,
// the same shape as (yggdrasil.footprints ...): it prints one sentinel line
// and the sentinel decides, never the exit code.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// runDLSelftest evaluates (yggdrasil.dl-selftest) in a host process and
// returns its sentinel line.
func runDLSelftest(host []string) (string, error) {
	if host == nil {
		host = defaultHost()
	}
	if host == nil {
		return "", fmt.Errorf("no Shen host launcher found")
	}
	root, err := yggRoot()
	if err != nil {
		return "", fmt.Errorf("materialising shaker: %w", err)
	}
	argv := append(append([]string{}, host...),
		"eval", "-q", "-l", "yggdrasil.shen", "-e", "(yggdrasil.dl-selftest)")
	out, _ := runAt(wrapExecutable(argv), root)
	i := strings.Index(out, "yggdrasil-dl-selftest:")
	if i < 0 {
		os.Stderr.WriteString(out)
		return "", fmt.Errorf("dl-selftest produced no report (host=%s)", strings.Join(host, " "))
	}
	line := out[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return strings.TrimRight(line, "\r"), nil
}

// The sentinel names the first failing case, so a FAIL is readable without
// re-running anything by hand. A missing sentinel is a failure too: it means
// the engine did not get as far as reporting.
func TestDLEngineSelfTest(t *testing.T) {
	host := checkHost(t)
	line, err := runDLSelftest(host)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "yggdrasil-dl-selftest: OK ") {
		t.Fatalf("ygg.dl engine selftest did not pass: %s", line)
	}
	// Every case must have run: a selftest that silently shrinks to zero
	// cases would still print OK.
	const want = "cases=9"
	if !strings.Contains(line, want) {
		t.Fatalf("selftest reported %q, expected %s (cases were added or lost)", line, want)
	}
	t.Log(line)
}
