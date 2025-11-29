if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi
gd build
vpk download http --url "https://vpk.quetzal.community" -o ./releases/velopack
vpk pack --packId "Aviary.EditorCollection" --packVersion "$1" --mainExe aviary --packDir ./releases/darwin/universal/aviary.app -o ./releases/velopack \
    --signAppIdentity "Developer ID Application: Quentin Quaadgras" \
    --signInstallIdentity "Developer ID Installer: Quentin Quaadgras" \
    --notaryProfile "QuentinQuaadgras"
cd releases/velopack
rclone copy -v --max-depth 1 . r2:aviary/
