{
  description = "agtlog — browse coding-agent logs and estimate recursive session cost";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    git-hooks = {
      url = "github:cachix/git-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, git-hooks }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        buildGoModule = pkgs.buildGoModule.override { go = pkgs.go_1_26; };
        version = self.shortRev or self.dirtyShortRev or "dev";

        preCommitHook = pkgs.writeShellApplication {
          name = "agtlog-pre-commit";
          runtimeInputs = [ pkgs.go_1_26 pkgs.just ];
          text = ''
            export GOTOOLCHAIN=local
            just pre-commit
          '';
        };
        nixBuildHook = pkgs.writeShellApplication {
          name = "agtlog-nix-build";
          runtimeInputs = [ pkgs.just ];
          text = "just nix-build";
        };

        preCommitCheck = git-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            agtlog-pre-commit = {
              enable = true;
              name = "agtlog build + checks";
              entry = pkgs.lib.getExe preCommitHook;
              language = "system";
              pass_filenames = false;
            };
            agtlog-nix-build = {
              enable = true;
              name = "nix build .#agtlog";
              entry = pkgs.lib.getExe nixBuildHook;
              language = "system";
              pass_filenames = false;
              files = "(^go\\.(mod|sum)$|^flake\\.(nix|lock)$)";
            };
          };
        };
      in
      {
        packages = {
          default = self.packages.${system}.agtlog;

          agtlog = buildGoModule {
            pname = "agtlog";
            inherit version;
            src = ./.;
            proxyVendor = true;
            # Replace with the hash reported by `nix build` after dependencies settle.
            vendorHash = pkgs.lib.fakeHash;
            subPackages = [ "cmd/agtlog" ];
            ldflags = [ "-s" "-w" "-X main.version=${version}" ];
            env.CGO_ENABLED = 0;

            meta = {
              description = "Browse coding-agent logs and estimate recursive session cost";
              homepage = "https://github.com/motoki317/agtlog";
              license = pkgs.lib.licenses.mit;
              mainProgram = "agtlog";
            };
          };
        };

        apps.default = {
          type = "app";
          program = "${self.packages.${system}.agtlog}/bin/agtlog";
        };

        devShells.default = pkgs.mkShell {
          inherit (preCommitCheck) shellHook;
          packages = (with pkgs; [
            go_1_26
            gopls
            golangci-lint
            just
          ]) ++ preCommitCheck.enabledPackages;
        };
      });
}
