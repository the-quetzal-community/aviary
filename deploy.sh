if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi
GOOS=windows GOARCH=amd64 gd build
GOOS=linux GOARCH=amd64 gd build
gd release $1
cd releases
rclone copy --max-depth 1 . r2:aviary/aviary/
