if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi
# Bake the Sentry DSN into the Go binary (gitignored generated file; no SENTRY_DSN → Sentry off).
"$(dirname "$0")/internal/sentry/inject_dsn.sh"
GOOS=windows GOARCH=amd64 gd build
# Linux ships the fully-static musl build (inproc crash capture, runs on glibc + musl).
GOOS=musl GOARCH=amd64 gd build
vpk download http --url "https://vpk.quetzal.community" -o ./releases/velopack
vpk [linux] pack --packId "Aviary.EditorCollection" --packVersion "$1" --packDir ./releases/musl/amd64 --mainExe aviary -o ./releases/velopack
vpk [win] pack --packId "Aviary.EditorCollection" --packVersion "$1" --packDir ./releases/windows/amd64 --mainExe aviary.exe -o ./releases/velopack
cd releases/velopack || exit 1
rclone copy -v --max-depth 1 . r2:aviary/
#vpk upload s3 --bucket aviary --endpoint
