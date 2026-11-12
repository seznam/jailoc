# Upstream pins for the VM runtime (go-microvm runner, libkrun, libkrunfw, kernel).
#
# goMicrovmRev must stay in sync with the `replace github.com/stacklok/go-microvm`
# line in go.mod; evaluating goMicrovm.src fails if it drifts.
{
  lib,
  fetchFromGitHub,
  fetchurl,
  goModText ? builtins.readFile ../../go.mod,
}:

let
  goMicrovmOwner = "thejoeejoee";
  goMicrovmRepo = "go-microvm";
  goMicrovmRev = "32f39314de8eb0965d6e6b8ebaed61e0308b3955";

  libkrunVersion = "1.19.7";
  libkrunfwVersion = "5.6.2";
  kernelVersion = "6.12.109";

  kernelMajor = builtins.head (lib.splitString "." kernelVersion);

  # --- go.mod coupling check -------------------------------------------------
  goModLines = lib.splitString "\n" goModText;
  replaceLine = lib.findFirst (
    l: lib.hasPrefix "replace github.com/stacklok/go-microvm =>" l
  ) null goModLines;

  parsed =
    if replaceLine == null then
      null
    else
      builtins.match "replace github.com/stacklok/go-microvm => github.com/([^/ ]+)/([^/ ]+) +v[^ ]*-([0-9a-f]{12})[[:space:]]*" replaceLine;

  fixFiles = "update nix/runtime/sources.nix and nix/runtime/go-microvm-runner.nix to match";

  checkMsg =
    if parsed == null then
      "go.mod: could not find or parse the `replace github.com/stacklok/go-microvm => github.com/<owner>/<repo> v...-<12 hex>` line (found: ${
        if replaceLine == null then "none" else "`${replaceLine}`"
      }); expected revision ${goMicrovmRev}; ${fixFiles}"
    else
      let
        foundOwner = builtins.elemAt parsed 0;
        foundRepo = builtins.elemAt parsed 1;
        foundShort = builtins.elemAt parsed 2;
      in
      if foundOwner != goMicrovmOwner || foundRepo != goMicrovmRepo then
        "go.mod replace points at ${foundOwner}/${foundRepo} (revision ${foundShort}) but nix pins ${goMicrovmOwner}/${goMicrovmRepo} at expected revision ${goMicrovmRev}; ${fixFiles}"
      else if !(lib.hasPrefix foundShort goMicrovmRev) then
        "go.mod pins go-microvm revision ${foundShort} but nix expects revision ${goMicrovmRev}; ${fixFiles}"
      else
        null;

  goMicrovmSrc =
    if checkMsg != null then
      throw checkMsg
    else
      fetchFromGitHub {
        owner = goMicrovmOwner;
        repo = goMicrovmRepo;
        rev = goMicrovmRev;
        hash = "sha256-pLwZPpTTUsNz+W75MA765Ai8FQPa/d39Yrvds3wA1dk=";
      };
in
{
  goMicrovm = {
    owner = goMicrovmOwner;
    repo = goMicrovmRepo;
    rev = goMicrovmRev;
    src = goMicrovmSrc;
  };

  libkrun = {
    version = libkrunVersion;
    src = fetchFromGitHub {
      owner = "libkrun";
      repo = "libkrun";
      rev = "v${libkrunVersion}";
      hash = "sha256-VtepY0sAqP18OIhIPwESoGi7E2Ahs4ihJjLyLH78JAM=";
    };
  };

  libkrunfw = {
    version = libkrunfwVersion;
    inherit kernelVersion;
    src = fetchFromGitHub {
      owner = "libkrun";
      repo = "libkrunfw";
      rev = "v${libkrunfwVersion}";
      hash = "sha256-HklZgZPjXe+eAGzRulEwRR1eo83tGlZBTRooCv0/ADU=";
    };
    kernel = fetchurl {
      url = "https://cdn.kernel.org/pub/linux/kernel/v${kernelMajor}.x/linux-${kernelVersion}.tar.xz";
      hash = "sha256-VITlUqM04VAZ9K66ieW1jwRlHPL04k4E3p8VLxw44/o=";
    };
  };
}
