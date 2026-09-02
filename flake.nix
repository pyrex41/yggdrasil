{
  # Optional pinned toolchain for the Go CLI (stage 2 build/vet/test + the
  # `targets` smoke). It deliberately does NOT pin a stage-1 Shen host: the
  # reference host is a locally built sibling `../shen-cl`, not a nixpkgs
  # package, so `yggdrasil shake` still needs $YGGDRASIL_HOST or a sibling
  # checkout as usual. git is included because `go build` stamps VCS info.
  description = "yggdrasil development environment (Go toolchain; a shake still needs an external Shen host)";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  outputs = { nixpkgs, ... }: let systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ]; each = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system}); in {
    packages = each (pkgs: { toolchain = pkgs.buildEnv { name = "yggdrasil-toolchain"; paths = [ pkgs.go pkgs.git ]; }; default = pkgs.buildEnv { name = "yggdrasil-toolchain"; paths = [ pkgs.go pkgs.git ]; }; });
    devShells = each (pkgs: { default = pkgs.mkShell { packages = [ pkgs.go pkgs.git ]; }; });
  };
}
