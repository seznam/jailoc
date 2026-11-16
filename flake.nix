{
  description = "CLI tool for managing sandboxed Docker Compose environments for headless OpenCode agents";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = {
    self,
    nixpkgs,
  }: let
    allSystems = [
      "x86_64-linux"
      "aarch64-linux"
      "aarch64-darwin"
    ];
    packageSystems = [
      "x86_64-linux"
      "aarch64-linux"
    ];

    forAllSystems = f:
      nixpkgs.lib.genAttrs allSystems (system:
        f {
          pkgs = nixpkgs.legacyPackages.${system};
        });

    forPackagedSystems = f:
      nixpkgs.lib.genAttrs packageSystems (system:
        f {
          pkgs = nixpkgs.legacyPackages.${system};
        });
  in {
    formatter = forAllSystems ({pkgs}: pkgs.alejandra);

    packages = forPackagedSystems ({pkgs}:
      import ./nix {
        inherit pkgs;
        jailocVersion = let
          dateStr = builtins.substring 0 8 (self.lastModifiedDate or "19700101");
          shortRev = self.shortRev or "dirty";
        in "0-unstable-${dateStr}-${shortRev}";
        jailocRev = self.shortRev or "dirty";
      }
      // {
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.jailoc;
      });

    apps = forPackagedSystems ({pkgs}: {
      jailoc = {
        type = "app";
        program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.jailoc}/bin/jailoc";
      };

      default = self.apps.${pkgs.stdenv.hostPlatform.system}.jailoc;
    });

    devShells = forAllSystems ({pkgs}: {
      default = pkgs.mkShell {
        packages = with pkgs; [
          go_1_26
          gopls
          golangci-lint
          nix-update
        ];
      };

      ci = pkgs.mkShell {
        packages = [pkgs.nix-update];
      };
    });
  };
}
