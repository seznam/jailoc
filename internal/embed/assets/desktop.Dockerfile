# =============================================================================
# jailoc desktop layer
#
# Optional VNC desktop for workspaces with a desktop: a TigerVNC X server
# (Xvnc), the noVNC web client with websockify, the Openbox window manager and
# the small X tools jailoc-guest needs to start the session. No browser is
# installed here; the agent brings its own. jailoc appends this RUN fragment
# to a layer Dockerfile that runs as root, after `ARG JAILOC_CLEAN_CACHES`.
# =============================================================================
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    if ! command -v apt-get >/dev/null; then \
      echo "jailoc: the desktop layer needs a Debian or Ubuntu base image (apt-get not found); install the desktop packages in your own image instead" >&2; \
      exit 1; \
    fi \
    && apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      tigervnc-standalone-server novnc websockify openbox dbus-x11 xterm fonts-dejavu-core x11-utils xauth \
    && if [ -n "$JAILOC_CLEAN_CACHES" ]; then apt-get clean && rm -rf /var/lib/apt/lists/*; fi
