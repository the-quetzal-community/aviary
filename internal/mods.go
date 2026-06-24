package internal

import (
	"fmt"
	"os"
	"strings"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/GLTFDocument"
	"graphics.gd/classdb/GLTFState"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Object"
)

// Mod support lets a player drop their own .glb models into the game data
// directory and have them appear in the design explorer as a new author,
// placeable exactly like community-library content.
//
// The wiring deliberately reuses the existing library machinery rather than
// adding a parallel one. A mod model is referenced by a "mod://" pseudo-URI
// (mod://<modname>/<category>/<file>.glb) that flows through the same
// MusicalDesign -> Import -> sceneFor -> Change pipeline as a res://library URI.
// designCategory(), the terrain-placement grouping, recency and undo all key off
// the URI string / its category folder, so they work for mods unchanged.
//
// Two things differ from library assets and are handled here:
//
//   - A raw user-dropped .glb has not been through Godot's editor import step
//     (no .import / .godot/imported/*.scn), so ResourceLoader.load cannot read
//     it. We load it at runtime with GLTFDocument instead — the same escape
//     hatch loadStaticObjNode uses for un-imported MakeHuman .obj clothing.
//   - The runtime-generated scene carries no collision (library scenery gets a
//     StaticBody3D at import time). The selection raycast hits a collider and
//     selects its owner (see client.go's left-click handler), so we synthesise
//     trimesh collision and pack the result into a PackedScene whose descendants
//     are all owned by the root — making placed mods selectable/movable/deletable.
//
// Multiplayer is graceful local fallback (by user request): a placement is still
// broadcast as a normal Import/Change so the mutation stays observable, but a peer
// who lacks the same mod file resolves nothing for it (sceneFor marks the design
// missing -> an empty Node3D -> invisible) and does not crash.

const (
	// modScheme prefixes a mod design/import URI.
	modScheme = "mod://"
	// modsRoot is the Godot user:// directory mods are dropped into; it maps to
	// UserDataDir/mods — the same root as library.pck and saves/.
	modsRoot = "user://mods"
	// genericModIconPath is the fallback tile/author icon for mods that ship no
	// icon.png / <file>.glb.png sidecar. Reuses an already-imported UI glyph so we
	// don't have to add (and re-package) a new preview.pck asset.
	genericModIconPath = "res://ui/editing.svg"
)

// modsReadme is dropped into the mods directory on first run so players can find
// where to put their models and how to lay them out.
const modsReadme = `Aviary mods
===========

Drop your own .glb models in here and they show up in the design drawer as a new
author, placeable just like the built-in Quetzal Community Library.

Layout (mirror the library):

    mods/<modname>/<category>/<model>.glb

  - <modname>   becomes an author button in the design drawer (its own tab group).
  - <category>  must match a tab you see in the drawer for that editor, e.g.
                housing, village, farming, factory, defense, fencing, utility,
                foliage, flowers, boulder, critter, swimmer, ...
                A model whose category folder matches no tab simply won't appear.

Optional sidecars (next to a model or modname folder):

    mods/<modname>/icon.png                 author button icon
    mods/<modname>/<category>/<model>.glb.png   tile thumbnail for that model

If a thumbnail/icon is missing a generic placeholder is used instead.

Notes:

  - Export your models at a sensible real-world size; aviary applies the same
    placement scaling it uses for library models.
  - Mods are local to this machine. In a shared world, other players only see a
    modded model if they have the same file at the same path; otherwise it is
    invisible to them (the placement is still recorded, so it appears for them if
    they later add the mod).
  - Mods are scanned at launch — add files, then restart aviary.
`

// ensureModsDir creates the user mods directory (UserDataDir/mods) and drops the
// README there on first run, so players can discover where to put their models.
// Best-effort: any error is surfaced and ignored (mods just won't be available).
func ensureModsDir() {
	if UserDataDir == "" {
		return
	}
	dir := UserDataDir + "/mods"
	if err := os.MkdirAll(dir, 0o777); err != nil {
		Engine.Raise(fmt.Errorf("mod: create %s: %w", dir, err))
		return
	}
	readme := dir + "/README.txt"
	if _, err := os.Stat(readme); err == nil {
		return // already present; don't clobber a user-edited copy
	}
	if err := os.WriteFile(readme, []byte(modsReadme), 0o666); err != nil {
		Engine.Raise(fmt.Errorf("mod: write %s: %w", readme, err))
	}
}

// isModPath reports whether uri is a mod pseudo-URI (mod://...).
func isModPath(uri string) bool { return strings.HasPrefix(uri, modScheme) }

// modGodotPath converts a mod:// design URI into the user:// path of its backing
// file, rejecting anything that tries to escape the mods directory. ok is false
// for a malformed or traversing path.
func modGodotPath(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, modScheme)
	if !ok || rest == "" || strings.HasPrefix(rest, "/") || strings.Contains(rest, "..") {
		return "", false
	}
	return modsRoot + "/" + rest, true
}

// genericModIconCache memoises the fallback icon (main-thread only).
var genericModIconCache Texture2D.Instance

func genericModIcon() Texture2D.Instance {
	if genericModIconCache == Texture2D.Nil {
		genericModIconCache = LoadSync[Texture2D.Instance](genericModIconPath)
	}
	return genericModIconCache
}

// loadModTexture loads a raw (un-imported) image file from a user:// path into a
// Texture2D, the way the flight planner loads snapshot thumbnails. Returns
// Texture2D.Nil on any failure; callers should FileExists-check first.
func loadModTexture(godotPath string) Texture2D.Instance {
	img := Image.LoadFromFile(godotPath)
	if img == Image.Nil || img.IsEmpty() {
		return Texture2D.Nil
	}
	return ImageTexture.CreateFromImage(img).AsTexture2D()
}

// modAuthorIcon returns the explorer button/heading icon for a mod author: the
// author's user://mods/<author>/icon.png sidecar when present, else the generic
// mod glyph.
func modAuthorIcon(author string) Texture2D.Instance {
	if p := modsRoot + "/" + author + "/icon.png"; FileAccess.FileExists(p) {
		if t := loadModTexture(p); t != Texture2D.Nil {
			return t
		}
	}
	return genericModIcon()
}

// generateModNode loads a mod .glb at runtime and returns its root Node3D. ok is
// false if the URI is malformed, the file is missing/corrupt, or the generated
// root is not a Node3D. Prints a one-line diagnostic on failure, mirroring
// loadStaticObjNode.
func generateModNode(uri string) (Node3D.Instance, bool) {
	godotPath, ok := modGodotPath(uri)
	if !ok {
		fmt.Println("mod: bad mod path", uri)
		return Node3D.Nil, false
	}
	doc := GLTFDocument.New()
	state := GLTFState.New()
	if err := doc.AppendFromFile(godotPath, state); err != nil {
		fmt.Println("mod: append_from_file failed for", godotPath, err)
		return Node3D.Nil, false
	}
	node := doc.GenerateScene(state)
	if node == Node.Nil {
		fmt.Println("mod: generate_scene returned nothing for", godotPath)
		return Node3D.Nil, false
	}
	root, ok := Object.As[Node3D.Instance](node)
	if !ok {
		fmt.Println("mod: generate_scene root is not a Node3D for", godotPath)
		node.QueueFree()
		return Node3D.Nil, false
	}
	return root, true
}

// loadModSceneNode returns a fresh runtime instance of a mod model, used by the
// placement-preview ghost (which strips collision on attach anyway). The caller
// owns the returned node.
func loadModSceneNode(uri string) (Node3D.Instance, bool) {
	return generateModNode(uri)
}

// loadModPackedScene loads a mod model, gives it trimesh collision so placed
// copies are selectable, and packs it into a PackedScene so it slots into the
// same packed_scenes cache and Instantiate() flow as library designs.
func loadModPackedScene(uri string) (PackedScene.Instance, bool) {
	root, ok := generateModNode(uri)
	if !ok {
		return PackedScene.Nil, false
	}
	addModCollision(root.AsNode())
	// Every descendant must be owned by the root for PackedScene.Pack to include
	// it — including the StaticBody3D nodes addModCollision just created, which is
	// why ownership is assigned last. After Instantiate() the collider's Owner()
	// is the instance root, which is exactly what the selection raycast selects.
	setModOwners(root.AsNode(), root.AsNode())
	scene := PackedScene.New()
	if err := scene.Pack(root.AsNode()); err != nil {
		Engine.Raise(fmt.Errorf("mod: pack %s: %w", uri, err))
		root.AsNode().QueueFree()
		return PackedScene.Nil, false
	}
	root.AsNode().QueueFree()
	return scene, true
}

// addModCollision walks the subtree and gives every MeshInstance3D a trimesh
// StaticBody3D child (Godot's create_trimesh_collision), so the placed model can
// be hit by the editor's selection raycast. Static concave collision is correct
// for placed scenery; skinned/animated critter mods get only this static shape (a
// known limitation — library critters get convex collision at import time).
func addModCollision(node Node.Instance) {
	if mi, ok := Object.As[MeshInstance3D.Instance](node); ok && mi.Mesh() != Mesh.Nil {
		mi.CreateTrimeshCollision()
	}
	for _, child := range node.GetChildren() {
		addModCollision(child)
	}
}

// setModOwners assigns root as the owner of every descendant of node (root itself
// keeps no owner), the prerequisite for PackedScene.Pack to serialise a code-built
// tree.
func setModOwners(root, node Node.Instance) {
	for _, child := range node.GetChildren() {
		child.SetOwner(root)
		setModOwners(root, child)
	}
}
