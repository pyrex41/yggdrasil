//go:build shen_rust

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shen-rust v0.1.1, the commit .github/shen-rust.ref names.
const shenRustV011 = "a653699c2f3c60efbce3d63e4045779b5e2777b3"

func TestRustPinIsV011(t *testing.T) {
	dir := rustCheckout(t)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	head := strings.TrimSpace(string(out))
	pin := rustPinSHA(t)
	if head != pin || head != shenRustV011 {
		t.Fatalf("checkout %s, pin file %s, v0.1.1 %s", head, pin, shenRustV011)
	}
	cargo, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cargo), `version = "0.1.1"`) {
		t.Fatal("workspace Cargo.toml is not version 0.1.1")
	}
	boot, err := os.ReadFile(filepath.Join(dir, "crates", "shen-rust", "src", "interp", "boot.rs"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(boot)
	if !strings.Contains(src, `("*port*", env!("CARGO_PKG_VERSION"))`) ||
		!strings.Contains(src, `("*release*", env!("CARGO_PKG_VERSION"))`) {
		t.Fatal("(*port*) and (*release*) no longer follow CARGO_PKG_VERSION")
	}
}

func TestRustEvalFreeFibNativeAndWasm(t *testing.T) {
	host := checkHost(t)
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Fatal("cargo is not on PATH")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("node is not on PATH")
	}
	if _, err := exec.LookPath("wasm-opt"); err != nil {
		t.Fatal("wasm-opt is not on PATH")
	}
	rustCheckout(t)

	out := t.TempDir()
	if _, err := shake("tests/fib.shen", out, host, "sub", true); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(out, "yggdrasil.manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "\nneeds-eval=false\n") ||
		!strings.Contains(string(manifest), "\ncannot-reach=eval\n") {
		t.Fatalf("fib is not eval-free:\n%s", manifest)
	}
	argv, err := build("rust", out, false)
	if err != nil || argv == nil {
		t.Fatalf("rust build: argv=%v err=%v", argv, err)
	}
	scaffold := filepath.Join(out, "app-rust")
	cargo := mustRead(t, filepath.Join(scaffold, "Cargo.toml"))
	if !strings.Contains(cargo, `default-features = false`) || strings.Contains(cargo, `features = ["interpreter"]`) {
		t.Fatalf("eval-free Cargo.toml should drop default features and the interpreter:\n%s", cargo)
	}
	mainSrc := mustRead(t, filepath.Join(scaffold, "src", "main.rs"))
	if strings.Index(mainSrc, "register_hot_overrides") < 0 ||
		strings.Index(mainSrc, "register_hot_overrides") > strings.Index(mainSrc, "boot::boot_aot_slice") {
		t.Fatal("generated main does not install hot overrides before boot_aot_slice")
	}
	if !strings.Contains(mainSrc, `#[cfg(target_arch = "wasm32")]`) ||
		!strings.Contains(mainSrc, `#[cfg(not(target_arch = "wasm32"))]`) {
		t.Fatal("generated main does not split the wasm entry from the native 1 GB thread")
	}
	stdout := runCmd(t, argv[0], argv[1:]...)
	if stdout != "fib 20 = 6765\n" {
		t.Fatalf("native stdout = %q", stdout)
	}
	nativeSize := fileSize(t, argv[0])
	t.Logf("native bytes %d", nativeSize)
	// The pre-0.1.1 native fib was about 9.2 MB. A default-features link of
	// this slice is back in that range; the lean link is about 1.2 MB.
	if nativeSize > 2_500_000 {
		t.Fatalf("native fib is %d bytes, want the lean link under 2.5 MB", nativeSize)
	}

	if err := exec.Command("rustup", "target", "add", "wasm32-wasip1").Run(); err != nil {
		t.Fatalf("wasm32-wasip1 target: %v", err)
	}
	wasmBuild := exec.Command("cargo", "build", "--release", "--target", "wasm32-wasip1", "-q")
	wasmBuild.Dir = scaffold
	wasmBuild.Stdout, wasmBuild.Stderr = os.Stderr, os.Stderr
	if err := wasmBuild.Run(); err != nil {
		t.Fatalf("wasm32 build: %v", err)
	}
	rawPath := filepath.Join(scaffold, "target", "wasm32-wasip1", "release", "app-rust.wasm")
	rawSize := fileSize(t, rawPath)
	t.Logf("wasm raw bytes %d", rawSize)
	if rawSize > 1_200_000 {
		t.Fatalf("raw wasm is %d bytes; a full-features link is about 4.8 MB", rawSize)
	}
	optPath := filepath.Join(scaffold, "fib.wasm")
	opt := exec.Command("wasm-opt", "-Oz",
		"--enable-bulk-memory", "--enable-multivalue", "--enable-simd",
		"--enable-reference-types", "--enable-nontrapping-float-to-int",
		rawPath, "-o", optPath)
	opt.Stdout, opt.Stderr = os.Stderr, os.Stderr
	if err := opt.Run(); err != nil {
		t.Fatalf("wasm-opt: %v", err)
	}
	optSize := fileSize(t, optPath)
	t.Logf("wasm-opt bytes %d", optSize)
	if optSize > 700_000 {
		t.Fatalf("wasm-opt fib is %d bytes, want the lean module under 700 KB", optSize)
	}
	boot := writeWasmBoot(t)
	outPath := filepath.Join(scaffold, "wasm-stdout.txt")
	node := exec.Command("node", boot, optPath, outPath)
	node.Stdout, node.Stderr = os.Stderr, os.Stderr
	if err := node.Run(); err != nil {
		t.Fatalf("node wasi: %v", err)
	}
	got := mustRead(t, outPath)
	if got != "fib 20 = 6765\n" {
		t.Fatalf("wasm stdout = %q", got)
	}
}

func TestRustEvalCapableScaffoldKeepsInterpreterOnly(t *testing.T) {
	host := checkHost(t)
	dir := rustCheckout(t)
	out := t.TempDir()
	if _, err := shake("tests/metaeval.shen", out, host, "sub", true); err != nil {
		t.Fatal(err)
	}
	scaffold := filepath.Join(out, "app-rust")
	cmd := exec.Command("cargo", "run", "-q", "--release", "-p", "yggdrasil-build", "--", out, scaffold)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("yggdrasil-build: %v", err)
	}
	if err := rustAotBootStep([]string{scaffold}); err != nil {
		t.Fatal(err)
	}
	cargo := mustRead(t, filepath.Join(scaffold, "Cargo.toml"))
	if !strings.Contains(cargo, `default-features = false, features = ["interpreter"]`) {
		t.Fatalf("eval-capable dependency line:\n%s", cargo)
	}
	mainSrc := mustRead(t, filepath.Join(scaffold, "src", "main.rs"))
	if strings.Contains(mainSrc, "boot::boot_aot_slice") {
		t.Fatal("needs-eval=true scaffold used the aot-only boot")
	}
	if strings.Contains(mainSrc, "register_hot_overrides") {
		t.Fatal("rust-aot-boot rewrote an interpreter scaffold")
	}
}

func TestRustPortAndRelease(t *testing.T) {
	host := checkHost(t)
	rustCheckout(t)
	prog := filepath.Join(t.TempDir(), "port.shen")
	body := "(output \"port ~A~%\" (port))\n(output \"release ~A~%\" (release))\n"
	if err := os.WriteFile(prog, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := shake(prog, out, host, "sub", true); err != nil {
		t.Fatal(err)
	}
	argv, err := build("rust", out, false)
	if err != nil || argv == nil {
		t.Fatalf("rust build: argv=%v err=%v", argv, err)
	}
	stdout := runCmd(t, argv[0], argv[1:]...)
	if stdout != "port 0.1.1\nrelease 0.1.1\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func rustCheckout(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("YGGDRASIL_SHEN_RUST_DIR")
	if dir == "" {
		cwd, _ := os.Getwd()
		dir = filepath.Join(cwd, "..", "shen-rust")
	}
	if st, err := os.Stat(filepath.Join(dir, "crates", "yggdrasil-build")); err != nil || !st.IsDir() {
		t.Fatalf("shen-rust checkout missing at %s (set YGGDRASIL_SHEN_RUST_DIR)", dir)
	}
	return dir
}

func rustPinSHA(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(".github/shen-rust.ref")
	if err != nil {
		t.Fatal(err)
	}
	var sha string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 40 && strings.Trim(line, "0123456789abcdef") == "" {
			sha = line
			break
		}
	}
	if sha == "" {
		t.Fatal(".github/shen-rust.ref has no SHA")
	}
	return sha
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func runCmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeWasmBoot(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wasm-boot.mjs")
	const src = `import { WASI } from 'node:wasi';
import { readFile, open } from 'node:fs/promises';
const wasmPath = process.argv[2];
const outPath = process.argv[3];
const out = await open(outPath, 'w');
const err = await open(outPath + '.err', 'w');
const wasi = new WASI({ version: 'preview1', args: ['fib.wasm'], env: {}, stdout: out.fd, stderr: err.fd, returnOnExit: true });
const bytes = await readFile(wasmPath);
const module = await WebAssembly.compile(bytes);
const instance = await WebAssembly.instantiate(module, wasi.getImportObject());
const exitCode = wasi.start(instance);
await out.close();
await err.close();
if (exitCode !== 0) {
  console.error(await readFile(outPath + '.err', 'utf8'));
  process.exit(exitCode || 1);
}
process.stdout.write('bytes ' + bytes.length + '\n');
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
