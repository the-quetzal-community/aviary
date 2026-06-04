package internal

import (
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
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

// keptImports holds every imported library resource (PackedScenes, textures) for the
// lifetime of the session, so the weak .ID()s in packed_scenes/textures always resolve
// when scenery is (re)instantiated. They are loaded on the resource-loader thread, so each
// handle is cleanup-managed and Object.Free-able; we release them at shutdown rather than
// Object.Leak them (which would pin them un-freeably and report as a leak at exit). Append
// and free both run on the main thread (Import drains via the world queue; the cleanup runs
// from RunShutdownCleanups), so no locking is needed.
var keptImports []Resource.Instance

func init() {
	// Drop our session refs at shutdown. Free just decrements, so each resource is
	// destroyed for real once the scenery nodes still using it are finalized in teardown.
	OnShutdown(func() {
		for _, res := range keptImports {
			Object.Free(res)
		}
		keptImports = nil
	})
}

func (world musicalImpl) ReportError(err error) {
	Engine.Raise(fmt.Errorf("%s", err))
}

// enqueue pushes a replay mutation closure onto the world queue, tallying it
// and (when profiling) timing any block on a full queue. That block time is the
// decode goroutine waiting for the main thread to drain — i.e. a direct measure
// of main-thread apply back-pressure. Near-zero ⇒ decode is the bottleneck.
func (world musicalImpl) enqueue(fn func()) {
	world.loadEnqueued.Add(1)
	if loadProfileOn {
		start := time.Now()
		world.queue <- fn
		bucketEnqueueBlock.add(time.Since(start))
		return
	}
	world.queue <- fn
}

func (world musicalImpl) Open(space musical.WorkID) (fs.File, error) {
	defer timeIn(&bucketStorageOpen)()
	profMark("storage.Open begin")
	file, err := world.openStorage(space)
	if err != nil {
		return nil, err
	}
	profMark("storage.Open done")
	loadDecodeStartUs.Store(time.Since(loadProgramStart).Microseconds())
	return world.trackLoadProgress(file), nil
}

func (world musicalImpl) openStorage(space musical.WorkID) (fs.File, error) {
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

// trackLoadProgress wraps the initial-replay .mus3 file so the loading screen
// can show a determinate bar (bytes decoded / file size). Only the FIRST open
// is wrapped (gated by loadProgressArmed); later opens serve joining peers via
// the musical server's srv.handle and must not reset the bar. The wrapper keeps
// the underlying io.Writer so committed sculpts still persist — musical/storage
// keys persistence off whether the file implements io.Writer.
func (world musicalImpl) trackLoadProgress(file fs.File) fs.File {
	if !world.loadProgressArmed.CompareAndSwap(true, false) {
		return file
	}
	w, ok := file.(io.Writer)
	if !ok {
		return file
	}
	if st, err := file.Stat(); err == nil {
		world.loadTotalBytes.Store(st.Size())
	}
	return &countingFile{File: file, w: w, count: &world.loadReadBytes}
}

// countingFile is an fs.File (plus io.Writer) that tallies bytes read so the
// loading screen can report decode progress.
type countingFile struct {
	fs.File
	w     io.Writer
	count *atomic.Int64
}

func (c *countingFile) Read(p []byte) (int, error) {
	if loadProfileOn {
		start := time.Now()
		n, err := c.File.Read(p)
		bucketDecodeRead.add(time.Since(start))
		c.count.Add(int64(n))
		return n, err
	}
	n, err := c.File.Read(p)
	c.count.Add(int64(n))
	return n, err
}

func (c *countingFile) Write(p []byte) (int, error) { return c.w.Write(p) }

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
		loadDecodeEndUs.Store(time.Since(loadProgramStart).Microseconds())
		profMark("decode finished, member assigned (enqueued=%d)", world.loadEnqueued.Load())
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
	world.enqueue(func() {
		defer timeIn(&bucketSculpt)()
		editor, ok := world.editors[brush.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		es := timeIn(&bucketEditorSculpt)
		editor.Sculpt(brush)
		es()
		countEditorSculpt(brush.Editor)
		defer timeIn(&bucketUISculpt)()
		world.ui.Editor.Sculpt(brush)
	})
	return nil
}

// isSceneImportPath reports whether an import URI is a scenery PackedScene that
// can be loaded lazily (on first instantiation) rather than eagerly during the
// replay. Conservative: only the known scene extensions qualify, so textures and
// anything unrecognised keep their eager-load behaviour.
func isSceneImportPath(uri string) bool {
	u := strings.ToLower(uri)
	return strings.HasSuffix(u, ".glb") || strings.HasSuffix(u, ".gltf") || strings.HasSuffix(u, ".scn")
}

// sceneFor returns the PackedScene for a design, loading it on first use and
// caching the handle in packed_scenes. Because Import no longer eager-loads
// scenery, this is where a design's .glb actually loads — so a design that is
// never instantiated never loads. Runs on the main thread (packed_scenes and
// keptImports are main-thread only); the load itself still funnels through the
// loader thread via LoadSync.
func (client *Client) sceneFor(design musical.Design) (PackedScene.Instance, bool) {
	if id, ok := client.packed_scenes[design]; ok {
		if inst, ok := id.Instance(); ok {
			return inst, true
		}
	}
	uri, ok := client.design_to_string[design]
	if !ok || isKeepImporterPath(uri) || !isSceneImportPath(uri) {
		return PackedScene.Instance{}, false
	}
	res := LoadSync[Resource.Instance](boulderCompatPath(uri))
	if !Object.Is[PackedScene.Instance](res) {
		return PackedScene.Instance{}, false
	}
	scene := Object.To[PackedScene.Instance](res)
	client.packed_scenes[design] = scene.ID()
	keptImports = append(keptImports, res)
	return scene, true
}

func (world musicalImpl) Import(uri musical.Import) error {
	world.enqueue(func() {
		defer timeIn(&bucketImport)()
		if _, ok := world.loaded[uri.Import]; ok {
			return
		}
		world.design_ids[uri.Design.Author] = max(world.design_ids[uri.Design.Author], uri.Design.Number)
		// Some imports are non-Godot-resource files shipped verbatim
		// (.obj files used by the citizen dressing pipeline use the
		// `keep` importer). Resource.Load on those logs an error to
		// the console; skip the load for those — we still want the
		// URI→Design mapping registered for later lookup.
		// Scenery (.glb/.scn) is loaded lazily on first instantiation (sceneFor),
		// so designs that are imported but never placed — or placed then removed
		// before this replay — never load. Textures must still load eagerly here:
		// terrain paint (uploadDesign) copies their image during the replay, the
		// only window the imported texture ID is resolvable. .obj keep-imports are
		// non-resources (used only for their URI→Design mapping).
		if !isKeepImporterPath(uri.Import) && !isSceneImportPath(uri.Import) {
			// Loaded on the resource-loader thread, so the handle is cleanup-managed
			// (runtime.AddCleanup) and Object.Free-able. Keep a reachable strong ref for
			// the session (packed_scenes/textures store only the weak .ID(), and we need
			// the resource alive to re-instantiate scenery on demand) and release it at
			// shutdown — NOT Object.Leak, which stops that cleanup and pins it un-freeably.
			res := LoadSync[Resource.Instance](boulderCompatPath(uri.Import))
			keptImports = append(keptImports, res)
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
			if scene, ok := world.sceneFor(uri.Design); ok {
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
	})
	return nil
}
func (world musicalImpl) Change(con musical.Change) error {
	world.enqueue(func() {
		defer timeIn(&bucketChange)()
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
		scene, ok := world.sceneFor(con.Design)
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
	})
	return nil
}

func (world musicalImpl) Action(action musical.Action) error {
	world.enqueue(func() {
		defer timeIn(&bucketAction)()
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
	})
	return nil
}

func (world musicalImpl) LookAt(view musical.LookAt) error {
	world.enqueue(func() {
		defer timeIn(&bucketLookAt)()
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
	})
	return nil
}
