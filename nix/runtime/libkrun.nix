{
  lib,
  stdenv,
  rustPlatform,
  cargo,
  rustc,
  libkrunSrc,
  libkrunVersion,
  libkrunInit,
  libkrunfwJailoc,
}:
stdenv.mkDerivation (finalAttrs: {
  pname = "libkrun";
  version = libkrunVersion;
  src = libkrunSrc;

  outputs = [
    "out"
    "dev"
  ];

  cargoDeps = rustPlatform.fetchCargoVendor {
    inherit (finalAttrs) pname version src;
    hash = "sha256-Ab7b37Npf0vNg7rF8KfPdLYCBuBCyRAYhR13RLecV04=";
  };

  # libkrun dlopens libkrunfw by name: keep it NEEDED so RUNPATH survives stdenv's shrinking fixup.
  env.RUSTFLAGS = toString (
    map (flag: "-C link-arg=" + flag) [
      "-Wl,--push-state,--no-as-needed"
      "-lkrunfw"
      "-Wl,--pop-state"
    ]
  );
  env.KRUN_INIT_BINARY_PATH = "${libkrunInit}/init.krun";

  nativeBuildInputs = [
    rustPlatform.cargoSetupHook
    rustPlatform.bindgenHook
    cargo
    rustc
  ];

  buildInputs = [libkrunfwJailoc];

  makeFlags = [
    "PREFIX=${placeholder "out"}"
    "BLK=1"
    "NET=1"
  ];

  postInstall = ''
    mkdir -p $dev/lib/pkgconfig
    mv $out/lib64/pkgconfig $dev/lib/
    mv $out/include $dev/

    # Upstream writes absolute paths; point them at the final split outputs.
    substituteInPlace "$dev/lib/pkgconfig/libkrun.pc" \
      --replace-fail "libdir=$out/lib64" "libdir=$out/lib" \
      --replace-fail "includedir=$out/include" "includedir=$dev/include"
  '';

  postFixup = ''
    # Linux ELF checks: lib64 normalization and RUNPATH shrinking have finished.
    test -e "$out/lib/libkrun.so.1"
    readelf -d "$out/lib/libkrun.so.1" | grep -q 'NEEDED.*\[libkrunfw\.so\.5\]'
    readelf -d "$out/lib/libkrun.so.1" | grep -q 'SONAME.*\[libkrun\.so\.1\]'
    readelf -d "$out/lib/libkrun.so.1" | grep 'RUNPATH' | grep -Fq '${libkrunfwJailoc}/lib'

    test -d "$out/lib"
    test -d "$dev/include"
    grep -Fxq "libdir=$out/lib" "$dev/lib/pkgconfig/libkrun.pc"
    grep -Fxq "includedir=$dev/include" "$dev/lib/pkgconfig/libkrun.pc"
  '';

  meta = {
    description = "Patched micro-VM library with the musl guest init for jailoc";
    homepage = "https://github.com/libkrun/libkrun";
    license = lib.licenses.asl20;
    platforms = [
      "x86_64-linux"
      "aarch64-linux"
    ];
  };
})
