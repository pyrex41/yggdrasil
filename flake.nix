{
  description = "Yggdrasil — complete Nix toolchain for Shen shaking and stage-2 targets";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
      each = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      tools = pkgs: with pkgs; [
        go_1_27 git gnumake clang cmake ninja pkg-config boehmgc
        sbcl clisp ecl luajit luarocks rustc cargo nodejs_22 julia chez swift
        beamPackages.erlang maven graalvmPackages.graalvm-ce
        python3 curl unzip gnutar coreutils
      ] ++ lib.optionals stdenv.hostPlatform.isLinux [ gcc gforth ];
    in {
      packages = each (pkgs: rec {
        yggdrasil = (pkgs.buildGoModule.override { go = pkgs.go_1_27; }) { pname = "yggdrasil"; version = "0.1.0"; src = pkgs.lib.cleanSource ./.; vendorHash = null; subPackages = [ "." ]; };
        toolchain = pkgs.buildEnv { name = "yggdrasil-toolchain"; paths = tools pkgs; };
        default = yggdrasil;
      });
      apps = each (pkgs: { default = { type = "app"; program = "${self.packages.${pkgs.system}.yggdrasil}/bin/yggdrasil"; }; });
      checks = each (pkgs: { inherit (self.packages.${pkgs.system}) yggdrasil; });
      devShells = each (pkgs: { default = pkgs.mkShell { packages = tools pkgs; env.GOTOOLCHAIN = "local"; JAVA_HOME = "${pkgs.graalvmPackages.graalvm-ce}"; }; });
    };
}
