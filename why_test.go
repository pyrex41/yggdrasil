package main

import (
	"strings"
	"testing"
)

// whyReport is pure string work, so it is tested without a host.
func TestWhyReportCutsToSentinel(t *testing.T) {
	out := "(fn ygg.chain)\nrun time: 0.1 secs\nyggdrasil-why: mode=eval-free floor=48 total=53 kernel=686\nseed pr adds=4 exclusive=4\ntrace read: not in footprint\ndone\n"
	got, ok := whyReport(out)
	if !ok {
		t.Fatal("sentinel not found")
	}
	want := "yggdrasil-why: mode=eval-free floor=48 total=53 kernel=686\nseed pr adds=4 exclusive=4\ntrace read: not in footprint\n"
	if got != want {
		t.Errorf("report:\n%q\nwant:\n%q", got, want)
	}
	if _, ok := whyReport("host crashed\n"); ok {
		t.Error("no sentinel must report !ok")
	}
}

// Host-gated: the two partial-function fixtures are the same program with
// and without one stray mention of eval. The report must show the
// eval-free one paying nothing for its partial function and the other
// sitting on the eval floor, with a chain to read that goes through
// shen.f-error - the creep the fixtures document.
func TestWhyPartialFixtures(t *testing.T) {
	host := checkHost(t)

	free, err := runWhy("tests/partial.shen", "read", host, "sub")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"yggdrasil-why: mode=eval-free ",
		"defun f adds=0 exclusive=0",
		"trace read: not in footprint",
	} {
		if !strings.Contains(free, want) {
			t.Errorf("partial.shen report missing %q:\n%s", want, free)
		}
	}
	if strings.Contains(free, "seed shen.f-error") {
		t.Errorf("bootstrap should have rewritten shen.f-error away (shen.partial):\n%s", free)
	}

	capable, err := runWhy("tests/partial-eval.shen", "read", host, "sub")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"yggdrasil-why: mode=eval-capable ",
		"eval-capable because the program mentions: eval",
		"shen.f-error -> y-or-n? -> read",
	} {
		if !strings.Contains(capable, want) {
			t.Errorf("partial-eval.shen report missing %q:\n%s", want, capable)
		}
	}
}
