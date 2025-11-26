GOOS=darwin gd build
vpk download http --url "https://vpk.quetzal.community" -o ./releases/velopack
vpk [osx] pack --packId "Aviary.EditorCollection" --packVersion "0.0.15" --packDir ./releases/darwin/universal/aviary.app -o ./releases/velopack \
    --signAppIdentity "Developer ID Application: Quentin Quaadgras" \
    --signInstallIdentity "Developer ID Installer: Quentin Quaadgras" \
    --notaryProfile "QuentinQuaadgras"
