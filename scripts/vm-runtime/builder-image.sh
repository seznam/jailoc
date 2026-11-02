# Sourced by the Linux build scripts: go-microvm's builder image pinned by
# digest, so a re-pushed tag cannot change what a release embeds. The tag must
# match LIBKRUN_VERSION in the versions.env of the go-microvm revision jailoc
# pins in go.mod. To move the pin, look up the digest of the new tag with
#   docker buildx imagetools inspect ghcr.io/stacklok/go-microvm-builder:<tag>
builder_repo=ghcr.io/stacklok/go-microvm-builder
builder_tag=v1.19.7
builder_digest=sha256:b23a700976ae05d74177c4b5b9de7e277a9aa034c6d70660bbc228bba0a02f96

# builder_image GO_MICROVM_CHECKOUT prints the pinned image reference and fails
# if the checkout's versions.env names another libkrun release.
builder_image() {
  local want
  want=$(grep '^LIBKRUN_VERSION=' "$1/versions.env" | cut -d= -f2)
  if [ "$want" != "$builder_tag" ]; then
    echo "$1/versions.env wants $builder_repo:$want, but builder-image.sh pins $builder_tag; update the pin" >&2
    return 1
  fi
  echo "$builder_repo:$builder_tag@$builder_digest"
}
