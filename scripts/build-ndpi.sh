#!/usr/bin/env bash
# Builds a static libndpi.a for a gokrazy target with zig as the C cross
# compiler and installs it to third_party/ndpi/<goarch>/{include,lib}, where
# the cgo directives in internal/ndpi pick it up. fltr.def runs it in the
# image's build stage; run it yourself for `make` and `make test`.
#
# usage: scripts/build-ndpi.sh [amd64|arm64]...   (default: amd64 arm64)
#
# NDPI_DIR overrides the output directory (default third_party/ndpi).
#
# Requires: zig, curl, tar, make, autoconf, automake, libtool, pkg-config,
# sha256sum.
set -euo pipefail

NDPI_VERSION="${NDPI_VERSION:-6.0}"
# SHA-256 of GitHub's source tarball for NDPI_VERSION; set it too when
# changing the version.
NDPI_SHA256="${NDPI_SHA256:-21fc40cab5505942c0b21d9bbaf73e9adf8162ddfe782e4cd072cab855a2eda9}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${NDPI_DIR:-$ROOT/third_party/ndpi}"
SRC="$OUT/src/nDPI-$NDPI_VERSION"

command -v zig >/dev/null || { echo "zig not found in PATH (https://ziglang.org/download/)" >&2; exit 1; }

if [ ! -d "$SRC" ]; then
  mkdir -p "$OUT/src"
  tarball="$OUT/src/nDPI-$NDPI_VERSION.tar.gz"
  curl -fsSL -o "$tarball" "https://github.com/ntop/nDPI/archive/refs/tags/$NDPI_VERSION.tar.gz"
  echo "$NDPI_SHA256  $tarball" | sha256sum -c - >/dev/null || {
    echo "nDPI $NDPI_VERSION tarball checksum mismatch (expected $NDPI_SHA256)" >&2
    rm -f "$tarball"
    exit 1
  }
  tar -xzf "$tarball" -C "$OUT/src"
  rm -f "$tarball"
fi
if [ ! -x "$SRC/configure" ]; then
  (cd "$SRC" && ./autogen.sh)
fi

archs=("$@")
[ ${#archs[@]} -eq 0 ] && archs=(amd64 arm64)

for goarch in "${archs[@]}"; do
  case "$goarch" in
    amd64) target=x86_64-linux-musl  host=x86_64-linux-musl ;;
    arm64) target=aarch64-linux-musl host=aarch64-linux-musl ;;
    *) echo "unsupported GOARCH: $goarch" >&2; exit 1 ;;
  esac

  build="$OUT/build/$goarch"
  rm -rf "$build" "$OUT/$goarch"
  mkdir -p "$build" "$OUT/$goarch/lib" "$OUT/$goarch/include/ndpi"

  (
    cd "$build"
    CC="zig cc -target $target" AR="zig ar" RANLIB="zig ranlib" \
    CFLAGS="-O2 -fno-sanitize=undefined -DCROARING_COMPILER_SUPPORTS_AVX512=0" \
      "$SRC/configure" --host="$host" \
        --with-only-libndpi \
        --disable-plugin-support \
        --disable-global-context-support \
        --without-pcre --without-maxminddb
    make -C src/lib -j"$(nproc)" libndpi.a
  )

  cp "$build/src/lib/libndpi.a" "$OUT/$goarch/lib/"
  cp "$SRC"/src/include/*.h "$build"/src/include/*.h "$OUT/$goarch/include/ndpi/" 2>/dev/null || true
  echo "==> $OUT/$goarch/lib/libndpi.a"
done
