package internal

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type musicalImpl struct {
	*Client
}

func (world musicalImpl) ReportError(err error) {
	Engine.Raise(fmt.Errorf("%s", err))
}

func (world musicalImpl) Open(space musical.WorkID) (fs.File, error) {
	name := base64.RawURLEncoding.EncodeToString(space[:])
	if UserState.Aviary.TogetherUntil.After(time.Now()) {
		fmt.Println("opening cloud save for", name)
		return OpenCloud(world.signalling, space)
	}
	if err := os.MkdirAll(UserDataDir+"/saves/"+name, 0777); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(UserDataDir+"/saves/"+name+"/"+UserState.Device+".mus3", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func parseVersion(version string) (major, minor, patch int) {
	_, version, _ = strings.Cut(version, " ")
	version = strings.TrimPrefix(version, ".")
	splits := strings.Split(version, ".")
	if len(splits) != 3 {
		return 0, 0, 0
	}
	fmt.Sscan(splits[0], &major)
	fmt.Sscan(splits[1], &minor)
	fmt.Sscan(splits[2], &patch)
	return
}

func (world musicalImpl) Member(req musical.Member) error {
	if req.Assign {
		if req.Server != "" {
			our_major, our_minor, our_patch := parseVersion(version)
			srv_major, srv_minor, srv_patch := parseVersion(req.Server)
			if srv_major > our_major || srv_major > srv_minor || srv_patch > our_patch {
				if !(our_major == 0 && our_minor == 0 && our_patch == 0) && !(srv_major == 0 && srv_minor == 0 && srv_patch == 0) {
					OS.ShellOpen("https://the.quetzal.community/aviary/mismatch")
				}
			}
		}
		Callable.Defer(Callable.New(func() {
			world.id = req.Author
			world.record = req.Record
			world.member = true
		}))
	}
	return nil
}

func (world musicalImpl) Upload(file musical.Upload) error { return nil }
func (world musicalImpl) Sculpt(brush musical.Sculpt) error {
	world.queue <- func() {
		editor, ok := world.editors[brush.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		editor.Sculpt(brush)
		world.ui.Editor.Sculpt(brush)
	}
	return nil
}
func (world musicalImpl) Import(uri musical.Import) error {
	world.queue <- func() {
		if _, ok := world.loaded[uri.Import]; ok {
			return
		}
		world.design_ids[uri.Design.Author] = max(world.design_ids[uri.Design.Author], uri.Design.Number)
		// Some imports are non-Godot-resource files shipped verbatim
		// (.obj files used by the citizen dressing pipeline use the
		// `keep` importer). Resource.Load on those logs an error to
		// the console; skip the load for those — we still want the
		// URI→Design mapping registered for later lookup.
		if !isKeepImporterPath(uri.Import) {
			res := Object.Leak(LoadSync[Resource.Instance](boulderCompatPath(uri.Import)))
			switch {
			case Object.Is[PackedScene.Instance](res):
				world.packed_scenes[uri.Design] = Object.To[PackedScene.Instance](res).ID()
			case Object.Is[Texture2D.Instance](res):
				world.textures[uri.Design] = Object.To[Texture2D.Instance](res).ID()
			}
		}
		world.loaded[uri.Import] = uri.Design
		world.design_to_string[uri.Design] = uri.Import

		redesigns := world.design_to_entity[uri.Design]
		for i, id := range redesigns {
			node, ok := id.Instance()
			if !ok {
				continue
			}
			if scene, ok := world.packed_scenes[uri.Design].Instance(); ok {
				new_node := Object.To[Node3D.Instance](scene.Instantiate()).
					SetPosition(node.AsNode3D().Position()).
					SetRotation(node.AsNode3D().Rotation()).
					SetScale(node.AsNode3D().Scale())
				if new_node.AsNode().HasNode("AnimationPlayer") {
					anim := Object.To[AnimationPlayer.Instance](new_node.AsNode().GetNode("AnimationPlayer"))
					anim.AsAnimationMixer().GetAnimation("Idle").SetLoopMode(Animation.LoopLinear)
					if anim.AsAnimationMixer().HasAnimation("Idle") {
						anim.PlayNamed("Idle")
					}
				}
				node.AsNode().ReplaceBy(new_node.AsNode())
				node.AsNode().QueueFree()
				redesigns[i] = new_node.ID()
				world.entity_to_object[world.object_to_entity[id]] = new_node.ID()
				world.object_to_entity[new_node.ID()] = world.object_to_entity[id]
				delete(world.object_to_entity, id)
			}
		}
		world.design_to_entity[uri.Design] = redesigns
	}
	return nil
}
func (world musicalImpl) Change(con musical.Change) error {
	world.queue <- func() {
		world.entity_ids[con.Entity.Author] = max(world.entity_ids[con.Entity.Author], con.Entity.Number)

		editor, ok := world.editors[con.Editor]
		if !ok {
			editor = world.TerrainEditor
		} else {
			editor.Change(con)
			return
		}

		container := world.TerrainEditor.AsNode()

		exists, ok := world.entity_to_object[con.Entity].Instance()
		if ok {
			if con.Remove {
				removeEntity(world.design_to_entity, world.entity_to_object, world.object_to_entity, con.Design, con.Entity, exists)
				return
			}

			pos := con.Offset
			if con.Editor == "float" {
				// Offset.Y is a lift delta relative to terrain HeightAt(XZ).
				// Apply it on top so floats ride terrain changes and survive reload.
				xz := Vector3.New(pos.X, 0, pos.Z)
				terrainY := world.TerrainEditor.HeightAt(xz)
				pos.Y = terrainY + pos.Y
			}
			exists.
				SetPosition(pos).
				SetRotation(con.Angles)
			// This just set the object's committed Y directly. If a terrain
			// height-brush hover preview had nudged it (objectPreviewOffsets), that
			// transient offset is now stale — drop it so the preview's
			// node.Y == committedY + offset invariant resets and Process re-derives
			// a fresh offset next frame (avoids a desync when a peer moves an object
			// while we hover a height brush over it).
			if world.TerrainEditor != nil {
				delete(world.TerrainEditor.objectPreviewOffsets, world.entity_to_object[con.Entity])
			}
			// If the Change carries an explicit Bounds (set by the
			// scale gizmo or restored from the musical log), use it
			// as the absolute scale. Otherwise leave whatever
			// scale the instance currently has — the creation path
			// applied the (editor default * design-intrinsic) scale,
			// and translate/twist edits must not stomp it.
			if con.Bounds != Vector3.Zero {
				exists.SetScale(con.Bounds)
			}
			return
		}
		var node Node3D.Instance
		scene, ok := world.packed_scenes[con.Design].Instance()
		if ok {
			node = Object.To[Node3D.Instance](scene.Instantiate())
		} else {
			node = Node3D.New()
		}
		if node.AsNode().HasNode("AnimationPlayer") {
			anim := Object.To[AnimationPlayer.Instance](node.AsNode().GetNode("AnimationPlayer"))
			anim.AsAnimationMixer().GetAnimation("Idle").SetLoopMode(Animation.LoopLinear)
			if anim.AsAnimationMixer().HasAnimation("Idle") {
				anim.PlayNamed("Idle")
			}
		}
		pos := con.Offset
		if con.Editor == "float" {
			// Offset.Y is a lift delta relative to terrain HeightAt(XZ).
			// Apply it on top so floats ride terrain changes and survive reload.
			xz := Vector3.New(pos.X, 0, pos.Z)
			terrainY := world.TerrainEditor.HeightAt(xz)
			pos.Y = terrainY + pos.Y
		}
		node.
			SetPosition(pos).
			SetRotation(con.Angles)
		if con.Bounds != Vector3.Zero {
			node.SetScale(con.Bounds)
		} else {
			// For editors that don't supply Bounds on creation (shelter,
			// vehicle, coaster props), multiply the post-instantiate
			// root scale. This automatically includes any "preset scale"
			// authored into the design's root (Kenney .scn assets).
			node.SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.1, 0.1, 0.1)))
		}
		registerEntity(world.design_to_entity, world.entity_to_object, world.object_to_entity, con.Design, con.Entity, node)
		container.AddChild(node.AsNode())
		// A placement that streams in (history replay at load, or a remote
		// peer) while we're terrain editing must also be non-pickable, so it
		// doesn't block the terrain brush raycast until the next editor
		// switch. StartEditing's sweep handles everything already present and
		// flips it all back to pickable on leaving terrain mode.
		if world.Editing == Editing.Terrain {
			setPickableExceptTerrain(node.AsNode(), false)
		}
		// Bump this design to the front of the design explorer's recency
		// ordering so the most recently placed designs surface first.
		// Fires for every creation — local, remote, or replayed from the
		// musical log — keeping the ordering observable across clients.
		if world.ui != nil && world.ui.Editor != nil {
			if resource, ok := world.design_to_string[con.Design]; ok {
				world.ui.Editor.BumpDesign(resource)
			}
		}
	}
	return nil
}

func (world musicalImpl) Action(action musical.Action) error {
	world.queue <- func() {
		editor, ok := world.editors[action.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		editor.Action(action)

		object, ok := world.entity_to_object[action.Entity].Instance()
		if ok {
			if !object.AsNode().HasNode("ActionRenderer") {
				actions := new(ActionRenderer)
				actions.client = world.Client
				actions.Initial = object.AsNode3D().Position()
				actions.AsNode().SetName("ActionRenderer")
				object.AsNode().AddChild(actions.AsNode())
			}
			actions := Object.To[*ActionRenderer](object.AsNode().GetNode("ActionRenderer"))
			actions.Add(action)
		}
	}
	return nil
}

func (world musicalImpl) LookAt(view musical.LookAt) error {
	world.queue <- func() {
		editor, ok := world.editors[view.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		editor.LookAt(view)

		if world.joining && view.Author == 0 {
			world.time.Follow(view.Timing)
		}
		if view.Author == world.id {
			return
		}
		if avatar, ok := world.authors[view.Author].Instance(); ok {
			tween := avatar.AsNode().CreateTween()
			PropertyTweener.Make(tween, avatar.AsObject(), "position", view.Offset, 0.1)
			PropertyTweener.Make(tween, avatar.AsObject(), "rotation", view.Angles, 0.1)
			return
		}
		avatar := LoadSync[PackedScene.Is[Node3D.Instance]]("res://library/everything/avatar/bald_eagle.glb").Instantiate().
			SetPosition(view.Offset).
			SetRotation(view.Angles).
			SetScale(Vector3.New(0.1, 0.1, 0.1))
		if avatar.AsNode().HasNode("AnimationPlayer") {
			anim := Object.To[AnimationPlayer.Instance](avatar.AsNode().GetNode("AnimationPlayer"))
			if anim.AsAnimationMixer().HasAnimation("Flap") {
				anim.AsAnimationMixer().GetAnimation("Flap").SetLoopMode(Animation.LoopLinear)
				anim.PlayNamed("Flap")
			}
		}
		world.AsNode().AddChild(avatar.AsNode())
		world.authors[view.Author] = avatar.ID()
	}
	return nil
}
