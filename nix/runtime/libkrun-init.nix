# Built against musl (via the caller's static stdenv), not glibc, so no LGPL glibc code ends up statically linked into the binary libkrun embeds.
{
  lib,
  stdenv,
  libkrunSrc,
  libkrunVersion,
}:
stdenv.mkDerivation {
  pname = "libkrun-init";
  version = libkrunVersion;
  src = libkrunSrc;

  dontConfigure = true;

  buildPhase = ''
    runHook preBuild
    $CC -O2 -static -Wall -o init.krun src/init_blob/init/init.c src/init_blob/init/dhcp.c
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    install -Dm755 init.krun $out/init.krun
    runHook postInstall
  '';

  postInstall = ''
    if $READELF -l $out/init.krun | grep -q "INTERP"; then
      echo "init.krun is not statically linked (has a program interpreter)" >&2
      exit 1
    fi
  '';

  meta = {
    description = "Statically linked guest init (init.krun) from libkrun";
    license = lib.licenses.asl20;
    platforms = lib.platforms.linux;
  };
}
