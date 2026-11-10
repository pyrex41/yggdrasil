package main

import (
	"os"
	"path/filepath"
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
	if len(rust.Build) != 3 {
		t.Fatalf("rust build steps: %d, want scaffold, rust-aot-boot, cargo build", len(rust.Build))
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
	boot := rust.Build[1].Argv
	if len(boot) != 3 || boot[0] != "{yggdrasil}" || boot[1] != "rust-aot-boot" || boot[2] != "{outdir}/app-rust" {
		t.Fatalf("rust-aot-boot step: %#v", boot)
	}
	link := strings.Join(rust.Build[2].Argv, " ")
	if link != "cargo build --release -q" || rust.Build[2].Cwd != "{outdir}/app-rust" {
		t.Fatalf("link step: %s cwd %s", link, rust.Build[2].Cwd)
	}
	if strings.Contains(link, "--features") || strings.Contains(link, "wasm32") {
		t.Fatalf("native link must not select features or a wasm target: %s", link)
	}
}

func TestPatchRustAotBootInstallsHotOverridesFirst(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "src", "main.rs")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	const generated = "fn run() -> Result<(), String> {\n" +
		"    let mut interp = Interp::new();\n" +
		"    boot::boot_aot_slice(&mut interp, kernel_aot::install, kernel_aot::run_toplevel)\n" +
		"        .map_err(|e| format!(\"boot: {e}\"))?;\n" +
		"    Ok(())\n" +
		"}\n"
	if err := os.WriteFile(main, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rustAotBootStep([]string{dir}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	hot := strings.Index(src, "register_hot_overrides")
	boot := strings.Index(src, "boot::boot_aot_slice")
	if hot < 0 || boot < 0 || hot > boot {
		t.Fatalf("hot overrides must precede boot_aot_slice:\n%s", src)
	}
	if err := rustAotBootStep([]string{dir}); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != src {
		t.Fatal("rust-aot-boot is not idempotent")
	}
	if strings.Count(src, "shen_rust::primitives::register_hot_overrides") != 1 {
		t.Fatalf("override inserted %d times", strings.Count(src, "shen_rust::primitives::register_hot_overrides"))
	}
}

func TestPatchRustAotBootLeavesInterpreterScaffold(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "src", "main.rs")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	const generated = "fn run() -> Result<(), String> {\n" +
		"    let mut interp = Interp::new();\n" +
		"    boot::boot_from_kl_source(&mut interp, KERNEL_KL, Some(kernel_aot::install))\n" +
		"        .map_err(|e| format!(\"boot: {e}\"))?;\n" +
		"    Ok(())\n" +
		"}\n"
	if err := os.WriteFile(main, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rustAotBootStep([]string{dir}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != generated {
		t.Fatalf("interpreter scaffold was rewritten:\n%s", got)
	}
}
