if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi
GOOS=windows GOARCH=amd64 gd build
GOOS=linux GOARCH=amd64 gd build
vpk [linux] pack --packId "Aviary.EditorCollection" --packVersion "$1" --packDir ./releases/linux/amd64 --mainExe aviary -o ./releases/velopack
vpk [win] pack --packId "Aviary.EditorCollection" --packVersion "$1" --packDir ./releases/windows/amd64 --mainExe aviary.exe -o ./releases/velopack
cd releases/velopack
rclone copy -v --max-depth 1 . r2:aviary/
