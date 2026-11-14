{
  lib,
  buildGoModule,
  pkg-config,
  goMicrovmSrc,
  goMicrovmRev,
  libkrunJailoc,
  libkrunVersion,
  libkrunfwVersion,
}:
buildGoModule {
  pname = "go-microvm-runner";
  version = "0-unstable-${builtins.substring 0 12 goMicrovmRev}";

  src = goMicrovmSrc;

  vendorHash = "sha256-zSfWYHdngggVSCU9fRYK7/9ansS0vbe3oBbBPyTKnyo=";

  subPackages = ["runner/cmd/go-microvm-runner"];

  env.CGO_ENABLED = 1;

  nativeBuildInputs = [pkg-config];
  buildInputs = [libkrunJailoc];

  # Upstream tests need a hypervisor unavailable in the build sandbox.
  doCheck = false;

  postPatch = ''
    grep -Fxq "LIBKRUN_VERSION=v${libkrunVersion}" versions.env || {
      echo "error: versions.env must contain LIBKRUN_VERSION=v${libkrunVersion}; update nix/runtime/sources.nix to match the pinned go-microvm source" >&2
      exit 1
    }
    grep -Fxq "LIBKRUNFW_VERSION=v${libkrunfwVersion}" versions.env || {
      echo "error: versions.env must contain LIBKRUNFW_VERSION=v${libkrunfwVersion}; update nix/runtime/sources.nix to match the pinned go-microvm source" >&2
      exit 1
    }
  '';

  postFixup = ''
    # Linux ELF checks: link to jailoc's patched libkrun and retain its RUNPATH.
    dynamic=$(readelf -d "$out/bin/go-microvm-runner")
    grep -q 'NEEDED.*\[libkrun\.so\.1\]' <<< "$dynamic" || {
      echo "error: go-microvm-runner must list libkrun.so.1 in NEEDED" >&2
      exit 1
    }
    grep 'RUNPATH' <<< "$dynamic" | grep -Fq "${lib.getLib libkrunJailoc}/lib" || {
      echo "error: go-microvm-runner RUNPATH must contain ${lib.getLib libkrunJailoc}/lib" >&2
      exit 1
    }
  '';

  meta = {
    description = "CGO runner for libkrun micro-VMs";
    homepage = "https://github.com/thejoeejoee/go-microvm";
    license = lib.licenses.asl20;
    mainProgram = "go-microvm-runner";
    platforms = ["x86_64-linux" "aarch64-linux"];
  };
}
