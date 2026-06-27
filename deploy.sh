#!/bin/bash
# Build + package aviary with Sentry crash reporting, then publish via Velopack.
#
# The target is gated on the host: run this on Linux to produce the Windows + Linux builds, and
# on macOS to produce the macOS build. macOS is Mac-only because the build is codesigned +
# notarized, which needs a real Mac (a Linux cross-compile can't be signed/notarized).
#
# Set SENTRY_DSN in the environment to enable crash reporting; with it unset everything still
# builds (just without Sentry), so forks can run this too.
if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi
VERSION="$1"
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT" || exit 1

# --- Sentry setup (shared by every platform) ------------------------------------------------
# Bake the DSN + release version into the Go binary (gitignored generated file; no SENTRY_DSN ⇒
# Sentry off). The version becomes the Sentry release "aviary@$VERSION".
AVIARY_VERSION="$VERSION" "$ROOT/internal/sentry/inject_dsn.sh"
# Fetch the official multi-platform sentry-godot addon (.dll/.dylib/.so + crashpad) into
# graphics/addons/sentry so it's packed into every export. On Windows/macOS/glibc it loads
# dynamically via its .gdextension; on the static musl build it's inert (Sentry is linked in —
# see internal/sentry) and `gd` strips .gdextension addons there. No SENTRY_DSN ⇒ addon removed.
"$ROOT/internal/sentry/fetch_sentry.sh"

# build_monitor GOOS GOARCH OUTPUT — the engine-free Go-panic crash monitor (pure Go ⇒ fully
# static, CGO off). aviary pipes its debug.SetCrashOutput to this sidecar so fatal Go panics
# (which unwind past the engine and abort) reach Sentry with a real Go stack. Shipped beside
# each binary; aviary finds it via os.Executable (".exe" on Windows).
build_monitor() { CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -o "$3" ./cmd/crashmonitor; }

vpk download http --url "https://vpk.quetzal.community" -o ./releases/velopack

if [ "$(uname)" = "Darwin" ]; then
  # --- macOS universal (native build, codesigned + notarized) -------------------------------
  gd build
  # Universal crash monitor inside the .app next to the executable (Contents/MacOS), so
  # os.Executable resolves it; signed together with the bundle below.
  APP="releases/darwin/universal/aviary.app/Contents/MacOS"
  if [ -d "$APP" ]; then
    build_monitor darwin amd64 releases/darwin/.crashmonitor-amd64
    build_monitor darwin arm64 releases/darwin/.crashmonitor-arm64
    lipo -create -output "$APP/aviary-crashmonitor" \
      releases/darwin/.crashmonitor-amd64 releases/darwin/.crashmonitor-arm64
    rm -f releases/darwin/.crashmonitor-amd64 releases/darwin/.crashmonitor-arm64
  fi
  vpk pack --packId "Aviary.EditorCollection" --packVersion "$VERSION" --mainExe aviary \
    --packDir ./releases/darwin/universal/aviary.app -o ./releases/velopack \
    --signAppIdentity "Developer ID Application: Quentin Quaadgras" \
    --signInstallIdentity "Developer ID Installer: Quentin Quaadgras" \
    --notaryProfile "QuentinQuaadgras"
fi

# --- Windows x86_64 -----------------------------------------------------------------------
GOOS=windows GOARCH=amd64 gd build
build_monitor windows amd64 releases/windows/amd64/aviary-crashmonitor.exe
# --- Linux x86_64 (fully-static musl: inproc native capture, runs on glibc + musl) --------
GOOS=musl GOARCH=amd64 gd build
build_monitor linux amd64 releases/musl/amd64/aviary-crashmonitor

[ -f releases/musl/amd64/aviary ] && \
vpk [linux] pack --packId "Aviary.EditorCollection" --packVersion "$VERSION" --packDir ./releases/musl/amd64 --mainExe aviary -o ./releases/velopack
[ -f releases/windows/amd64/aviary.exe ] && \
vpk [win] pack --packId "Aviary.EditorCollection" --packVersion "$VERSION" --packDir ./releases/windows/amd64 --mainExe aviary.exe -o ./releases/velopack

cd releases/velopack || exit 1
rclone copy -v --max-depth 1 . r2:aviary/
#vpk upload s3 --bucket aviary --endpoint
