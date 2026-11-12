{
  pkgs,
  jailocVersion,
  jailocRev,
}: let
  inherit (pkgs) lib;
  sources = pkgs.callPackage ./runtime/sources.nix {};

  # jailoc's libkrun patches, applied in lexical order like scripts/vm-runtime.
  patchDir = ../scripts/vm-runtime/libkrun-patches;
  libkrunSrc = pkgs.applyPatches {
    name = "libkrun-${sources.libkrun.version}-jailoc-src";
    src = sources.libkrun.src;
    patches = map (name: patchDir + "/${name}") (
      lib.sort lib.lessThan (
        builtins.filter (lib.hasSuffix ".patch") (builtins.attrNames (builtins.readDir patchDir))
      )
    );
  };

  libkrunInit = pkgs.callPackage ./runtime/libkrun-init.nix {
    stdenv = pkgs.pkgsStatic.stdenv; # static musl for the guest
    inherit libkrunSrc;
    libkrunVersion = sources.libkrun.version;
  };

  libkrunfwJailoc = pkgs.callPackage ./runtime/libkrunfw.nix {
    libkrunfwSrc = sources.libkrunfw.src;
    libkrunfwVersion = sources.libkrunfw.version;
    linuxKernelTarball = sources.libkrunfw.kernel;
  };

  libkrunJailoc = pkgs.callPackage ./runtime/libkrun.nix {
    inherit libkrunSrc libkrunInit libkrunfwJailoc;
    libkrunVersion = sources.libkrun.version;
  };

  goMicrovmRunner = pkgs.callPackage ./runtime/go-microvm-runner.nix {
    goMicrovmSrc = sources.goMicrovm.src;
    goMicrovmRev = sources.goMicrovm.rev;
    inherit libkrunJailoc;
    libkrunVersion = sources.libkrun.version;
    libkrunfwVersion = sources.libkrunfw.version;
  };

  vmRuntime = {
    runner = "${goMicrovmRunner}/bin/go-microvm-runner";
    libDir = lib.makeLibraryPath [libkrunJailoc libkrunfwJailoc];
    components = {inherit libkrunInit libkrunfwJailoc libkrunJailoc goMicrovmRunner;};
  };
in {
  jailoc = pkgs.callPackage ./jailoc.nix {
    inherit jailocVersion jailocRev;
    # The VM runtime is built for Linux only; see docs/development.md for macOS.
    vmRuntime =
      if pkgs.stdenv.hostPlatform.isLinux
      then vmRuntime
      else null;
  };
}
