{
  lib,
  stdenv,
  buildGoModule,
  installShellFiles,
  jailocVersion,
  jailocRev,
  vmRuntime ? null,
}:
buildGoModule {
  pname = "jailoc";
  version = jailocVersion;

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
    ];
  };

  vendorHash = "sha256-Vv5vbXvTs6vnd+pF6LgqE8f0/a1oUWIt2a8ItDFrd3k=";

  subPackages = ["cmd/jailoc"];

  env.CGO_ENABLED = 0;

  # Embed jailoc-guest for linux/arm64 and linux/amd64, as the
  # GoReleaser before hook does; without it jailoc cannot boot a VM.
  preBuild = ''
    go generate ./internal/guestbin
  '';
  # The vendored-modules derivation runs preBuild too, before vendor/ exists.
  overrideModAttrs = _: {preBuild = "";};

  ldflags =
    [
      "-s"
      "-w"
      "-X main.version=${jailocRev}"
      "-X main.commit=${jailocRev}"
      # date intentionally omitted for reproducible builds
    ]
    ++ lib.optionals (vmRuntime != null) [
      "-X github.com/seznam/jailoc/internal/cmd.defaultVMRunner=${vmRuntime.runner}"
      "-X github.com/seznam/jailoc/internal/cmd.defaultVMLibDir=${vmRuntime.libDir}"
    ];

  nativeBuildInputs = [installShellFiles];

  postInstall =
    lib.optionalString (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
      installShellCompletion --cmd jailoc \
        --bash <($out/bin/jailoc completion bash) \
        --fish <($out/bin/jailoc completion fish) \
        --zsh <($out/bin/jailoc completion zsh)
    ''
    + lib.optionalString (vmRuntime != null) ''
      if ! grep -qF -- '${vmRuntime.runner}' "$out/bin/jailoc"; then
        echo "error: jailoc binary does not embed the VM runner path ${vmRuntime.runner}" >&2
        exit 1
      fi
    '';

  passthru = lib.optionalAttrs (vmRuntime != null) {
    runtime = vmRuntime.components;
  };

  meta = with lib; {
    description = "Manage sandboxed Docker Compose environments for headless OpenCode coding agents";
    homepage = "https://github.com/seznam/jailoc";
    license = licenses.mit;
    mainProgram = "jailoc";
  };
}
