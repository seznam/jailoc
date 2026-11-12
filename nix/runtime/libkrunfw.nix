{
  lib,
  stdenv,
  libkrunfwSrc,
  libkrunfwVersion,
  linuxKernelTarball,
  flex,
  bison,
  bc,
  cpio,
  perl,
  python3,
  elfutils,
}:
stdenv.mkDerivation {
  pname = "libkrunfw";
  version = libkrunfwVersion;
  src = libkrunfwSrc;

  __structuredAttrs = true;

  postPatch = ''
    # Builds have no network; the preceding Makefile command creates tarballs/.
    substituteInPlace Makefile \
      --replace-fail 'curl $(KERNEL_REMOTE) -o $(KERNEL_TARBALL)' 'ln -s ${linuxKernelTarball} $(KERNEL_TARBALL)'
  '';

  nativeBuildInputs = [
    flex
    bison
    bc
    cpio
    perl
    (python3.withPackages (p: [p.pyelftools]))
  ];

  buildInputs = [elfutils];

  makeFlags = [
    "PREFIX=${placeholder "out"}"
  ];

  # Fixes https://github.com/containers/libkrunfw/issues/55.
  env = lib.optionalAttrs stdenv.hostPlatform.isAarch64 {
    NIX_CFLAGS_COMPILE = "-march=armv8-a+crypto";
  };

  enableParallelBuilding = true;

  postFixup = ''
    # Linux ELF checks: fixup has moved the upstream lib64 directory to lib.
    test -e "$out/lib/libkrunfw.so.5"
    readelf -d "$out/lib/libkrunfw.so.5" | grep -q 'SONAME.*libkrunfw.so.5'
    test "$(grep -caF "Linux version 6.12.109" "$out/lib/libkrunfw.so.5")" -ge 1
  '';

  meta = {
    description = "Dynamic library bundling the guest payload consumed by libkrun";
    homepage = "https://github.com/libkrun/libkrunfw";
    license = with lib.licenses; [lgpl21Only gpl2Only];
    platforms = [
      "x86_64-linux"
      "aarch64-linux"
    ];
  };
}
