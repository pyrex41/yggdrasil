package main

import (
	"strings"
	"testing"
)

func TestRustRecipeLeavesFeaturesToTheBuilder(t *testing.T) {
	b, err := loadBuilders()
	if err != nil {
		t.Fatal(err)
	}
	rust, ok := b["rust"]
	if !ok {
		t.Fatal("no rust target")
	}
	if rust.DirEnv != "YGGDRASIL_SHEN_RUST_DIR" || rust.RunImpl != "shen-rust" {
		t.Fatalf("rust builder identity: dir_env=%s run_impl=%s", rust.DirEnv, rust.RunImpl)
	}
	if len(rust.Build) != 2 {
		t.Fatalf("rust build steps: %d, want scaffold then cargo build", len(rust.Build))
	}
	scaffold := strings.Join(rust.Build[0].Argv, " ")
	if !strings.Contains(scaffold, "cargo run -q --release -p yggdrasil-build") {
		t.Fatalf("scaffold step: %s", scaffold)
	}
	if strings.Contains(scaffold, "--full") || strings.Contains(scaffold, "--features") {
		t.Fatalf("scaffold must leave features to yggdrasil-build: %s", scaffold)
	}
	if rust.Build[0].Cwd != "{shen_rust}" {
		t.Fatalf("scaffold cwd: %s", rust.Build[0].Cwd)
	}
	for _, st := range rust.Build {
		joined := strings.Join(st.Argv, " ")
		if strings.Contains(joined, "rust-aot-boot") || strings.Contains(joined, "register_hot_overrides") {
			t.Fatalf("v0.1.2 boots without a generated override patch: %s", joined)
		}
	}
	link := strings.Join(rust.Build[1].Argv, " ")
	if link != "cargo build --release -q" || rust.Build[1].Cwd != "{outdir}/app-rust" {
		t.Fatalf("link step: %s cwd %s", link, rust.Build[1].Cwd)
	}
	if strings.Contains(link, "--features") || strings.Contains(link, "wasm32") {
		t.Fatalf("native link must not select features or a wasm target: %s", link)
	}
}
