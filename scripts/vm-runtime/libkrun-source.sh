# Sourced by the build scripts that build from the libkrun source: the libkrun
# release and the commit its tag points to, so a moved tag cannot change what a
# release embeds. The tag must match LIBKRUN_VERSION in the versions.env of the
# go-microvm revision jailoc pins in go.mod. To move the pin, look up the
# commit of the new tag with
#   git ls-remote https://github.com/libkrun/libkrun 'refs/tags/<tag>' 'refs/tags/<tag>^{}'
# (for an annotated tag, the ^{} line names the commit).
libkrun_tag=v1.19.7
libkrun_commit=43b2f5aadeebb90f42053c9df2ec8b16583e554f

# libkrun_release GO_MICROVM_CHECKOUT prints the pinned libkrun tag and fails
# if the checkout's versions.env names another release.
libkrun_release() {
  local want
  want=$(grep '^LIBKRUN_VERSION=' "$1/versions.env" | cut -d= -f2)
  if [ "$want" != "$libkrun_tag" ]; then
    echo "$1/versions.env wants libkrun $want, but libkrun-source.sh pins $libkrun_tag; update the pin" >&2
    return 1
  fi
  echo "$libkrun_tag"
}

# clone_libkrun GO_MICROVM_CHECKOUT DIR clones the pinned libkrun into DIR and
# fails if the checkout's versions.env names another release or the tag points
# to another commit.
clone_libkrun() {
  local head
  libkrun_release "$1" >/dev/null || return 1
  git clone --quiet --depth 1 --branch "$libkrun_tag" https://github.com/libkrun/libkrun "$2"
  head=$(git -C "$2" rev-parse HEAD)
  if [ "$head" != "$libkrun_commit" ]; then
    echo "libkrun $libkrun_tag points to $head, not the pinned $libkrun_commit" >&2
    return 1
  fi
}
