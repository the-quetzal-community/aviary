# Modding aviary

You can add your own 3D models to aviary by dropping `.glb` files into the game
data directory. They are picked up at launch and appear in the design drawer as a
new author, placeable exactly like the built-in Quetzal Community Library.

## Where the files go

Mods live under the `mods/` folder inside aviary's user data directory:

| Platform | Path |
| --- | --- |
| Linux   | `~/.local/share/aviary/mods/` |
| Windows | `%APPDATA%\aviary\mods\` |
| macOS   | `~/Library/Application Support/aviary/mods/` |

The folder (with a `README.txt`) is created automatically the first time you run
aviary.

## Layout

Mirror the library's structure:

```
mods/<modname>/<category>/<model>.glb
```

- **`<modname>`** becomes an author button in the design drawer, grouping all of
  that mod's models under its own tab set.
- **`<category>`** must match a tab shown in the drawer for the editor you want
  the model in (e.g. `housing`, `village`, `farming`, `factory`, `defense`,
  `fencing`, `utility`, `foliage`, `flowers`, `boulder`, `critter`, `swimmer`).
  A model in a category folder that matches no tab simply won't appear.

### Example

```
mods/
  mycastles/
    icon.png                      # optional author button icon
    housing/
      keep.glb
      keep.glb.png                # optional tile thumbnail
      tower.glb
```

## Thumbnails & icons (optional)

- `mods/<modname>/icon.png` — the author button icon.
- `mods/<modname>/<category>/<model>.glb.png` — the palette tile thumbnail.

If a thumbnail or icon is missing, a generic placeholder is shown.

## How it works

A raw `.glb` you drop in has not been through Godot's editor import step, so
aviary loads it at runtime via `GLTFDocument` and references it with a `mod://`
pseudo-URI (`mod://<modname>/<category>/<model>.glb`). That URI flows through the
same placement / undo / network pipeline as a library design, and aviary adds
collision so placed mods can be selected, moved and deleted.

## Notes & limitations

- **Sizing** — export models at a sensible real-world scale; aviary applies the
  same placement scaling it uses for library models. There is no per-model size
  override file yet.
- **Multiplayer** — mods are local to your machine. In a shared world, other
  players only see your modded model if they have the same file at the same
  `mods/...` path. Otherwise it is invisible to them — but the placement is still
  recorded, so the model appears for them if they later add the same mod.
- **Animated/skinned models** get static (non-deforming) collision for selection
  purposes; this is sufficient for placement but is not a full physics body.
- **Reload** — mods are scanned at launch. Add files, then restart aviary.
- **Desktop only** — the web build has no writable mods directory.
