{
  description = "CLI tool for managing sandboxed Docker Compose environments for headless OpenCode agents";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      allSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forAllSystems = f: nixpkgs.lib.genAttrs allSystems (system: f {
        pkgs = nixpkgs.legacyPackages.${system};
      });
    in
    {
      packages = forAllSystems ({ pkgs }: {
        jailoc = pkgs.buildGoModule {
          pname = "jailoc";
          version =
            let
              dateStr = builtins.substring 0 8 (self.lastModifiedDate or "19700101");
              shortRev = self.shortRev or "dirty";
            in
            "0-unstable-${dateStr}-${shortRev}";

          src = pkgs.lib.fileset.toSource {
            root = ./.;
            fileset = pkgs.lib.fileset.unions [
              ./go.mod
              ./go.sum
              ./cmd
              ./internal
            ];
          };

          vendorHash = "sha256-F3Lcoqzf/3a6fs45i8VRdn9KB2xHOfuGv3DsawL1I/U=";

          subPackages = [ "cmd/jailoc" ];

          env.CGO_ENABLED = 0;

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${self.shortRev or "dirty"}"
            "-X main.commit=${self.shortRev or "dirty"}"
            # date intentionally omitted for reproducible builds
          ];

          nativeBuildInputs = [ pkgs.installShellFiles ];

          postInstall = pkgs.lib.optionalString (pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform) ''
            installShellCompletion --cmd jailoc \
              --bash <($out/bin/jailoc completion bash) \
              --fish <($out/bin/jailoc completion fish) \
              --zsh <($out/bin/jailoc completion zsh)
          '';

          meta = with pkgs.lib; {
            description = "Manage sandboxed Docker Compose environments for headless OpenCode coding agents";
            homepage = "https://github.com/seznam/jailoc";
            license = licenses.mit;
            mainProgram = "jailoc";
          };
        };

        default = self.packages.${pkgs.stdenv.hostPlatform.system}.jailoc;
      });

      devShells = forAllSystems ({ pkgs }: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_26
            gopls
            golangci-lint
            nix-update
          ];
        };

        ci = pkgs.mkShell {
          packages = [ pkgs.nix-update ];
        };
      });
    };
}
