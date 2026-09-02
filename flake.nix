{
  # Optional pinned toolchain for the Go CLI (stage 2 build/vet/test + the
  # `targets` smoke). It deliberately does NOT pin a stage-1 Shen host: the
  # reference host is a locally built sibling `../shen-cl`, not a nixpkgs
  # package, so `yggdrasil shake` still needs $YGGDRASIL_HOST or a sibling
  # checkout as usual. git is included because `go build` stamps VCS info, and
  # the nixpkgs darwin stdenv shadows `xcrun` in a way that breaks the
  # /usr/bin/git shim.
  #
  # Two halves make the pin real. `pkgs.go_1_27` supplies 1.27 -- the locked rev
  # already ships it, only the default `pkgs.go` attr is older (1.26.5). And
  # `env.GOTOOLCHAIN = "local"` stops a go.mod directive above the nix-provided
  # compiler from silently DOWNLOADING a toolchain and building with that
  # instead, which is what made this flake's predecessor decorative.
  description = "yggdrasil development environment (Go toolchain; a shake still needs an external Shen host)";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  outputs = { nixpkgs, ... }: let systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ]; each = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system}); in {
    packages = each (pkgs: { toolchain = pkgs.buildEnv { name = "yggdrasil-toolchain"; paths = [ pkgs.go_1_27 pkgs.git ]; }; default = pkgs.buildEnv { name = "yggdrasil-toolchain"; paths = [ pkgs.go_1_27 pkgs.git ]; }; });
    devShells = each (pkgs: { default = pkgs.mkShell { packages = [ pkgs.go_1_27 pkgs.git ]; env.GOTOOLCHAIN = "local"; }; });
  };
}
