package internal

import (
	"path"
	"strconv"
	"strings"
	"time"

	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Plane"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type ShelterEditor struct {
	Node3D.Extension[ShelterEditor]
	musical.Stubbed

	Objects Node3D.Instance
	Preview PreviewRenderer

	explore       bool
	current_plane Plane.NormalD
	current_level int
	levels        int

	grid_shader ShaderMaterial.ID

	client *Client

	last_angle_change Angle.Radians
	last_mouse_change time.Time

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity
}

func (editor *ShelterEditor) Ready() {
	editor.explore = true
	editor.design_to_entity, editor.entity_to_object, editor.object_to_entity = newEntityMaps()

	editor.current_plane = Plane.NormalD{Normal: Vector3.XYZ{0, 1, 0}}
	editor.Preview.setDefaultScale(0.2)
}

func (editor *ShelterEditor) Views() []string {
	var views = []string{
		"explore",
		"unicode/G",
	}
	for i := 1; i <= editor.levels; i++ {
		views = append(views, "unicode/"+strconv.Itoa(i))
	}
	return append(views, "unicode/+")
}

func (editor *ShelterEditor) SwitchToView(view string) {
	editor.explore = false
	scene := SceneTree.Get(editor.AsNode())
	switch view {
	case "explore":
		editor.setFloorShape(scene, editor.current_level, false)
		for i := 0; i <= editor.levels; i++ {
			scene.SetGroup("floor_"+strconv.Itoa(i), "visible", true)
			scene.SetGroup("floor_"+strconv.Itoa(i), "process_mode", Node.ProcessModeInherit)
		}
		editor.explore = true
		editor.setActiveLevel(0)
	case "unicode/G":
		editor.setFloorShape(scene, editor.current_level, false)
		editor.setActiveLevel(0)
		editor.setFloorShape(scene, 0, true)
		for i := 1; i <= editor.levels; i++ {
			scene.
				SetGroup("floor_"+strconv.Itoa(i), "visible", false).
				SetGroup("floor_"+strconv.Itoa(i), "process_mode", Node.ProcessModeDisabled)
		}
	case "unicode/+":
		editor.levels++
		editor.client.ui.ViewSelector.Refresh(editor.levels+1, editor.Views())
	default:
		if level_str, ok := strings.CutPrefix(view, "unicode/"); ok {
			level, err := strconv.Atoi(level_str)
			if err == nil {
				editor.setFloorShape(scene, editor.current_level, false)
				editor.setActiveLevel(level)
				for i := 0; i <= editor.levels; i++ {
					scene.SetGroup("floor_"+strconv.Itoa(i), "visible", i <= level)
					if i <= level {
						scene.SetGroup("floor_"+strconv.Itoa(i), "process_mode", Node.ProcessModeInherit)
					} else {
						scene.SetGroup("floor_"+strconv.Itoa(i), "process_mode", Node.ProcessModeDisabled)
					}
				}
				editor.setFloorShape(scene, level, true)
			}
		}
	}
}

// setFloorShape toggles which mesh group represents `level`: when
// exploded, the cutaway "floor_short_N" is shown and "floor_whole_N"
// hidden; when whole, the reverse. Both groups exist per level — the
// editor swaps which one is in the scene by visibility + process_mode.
func (editor *ShelterEditor) setFloorShape(scene SceneTree.Instance, level int, exploded bool) {
	suffix := strconv.Itoa(level)
	wholeMode := Node.ProcessModeInherit
	shortMode := Node.ProcessModeDisabled
	if exploded {
		wholeMode = Node.ProcessModeDisabled
		shortMode = Node.ProcessModeInherit
	}
	scene.
		SetGroup("floor_whole_"+suffix, "visible", !exploded).
		SetGroup("floor_whole_"+suffix, "process_mode", wholeMode).
		SetGroup("floor_short_"+suffix, "visible", exploded).
		SetGroup("floor_short_"+suffix, "process_mode", shortMode)
}

// setActiveLevel updates current_level, the clipping plane, the grid
// shader's center offset, and the camera Y so the rest of the editor
// is aligned with the just-entered floor.
func (editor *ShelterEditor) setActiveLevel(level int) {
	editor.current_level = level
	editor.current_plane = Plane.NormalD{Normal: Vector3.XYZ{0, 1, 0}, D: Float.X(level)}
	shader, _ := editor.grid_shader.Instance()
	shader.SetShaderParameter("center_offset", Vector3.New(0, float64(level), 0))
	pos := editor.client.FocalPoint.Position()
	pos.Y = Float.X(level)
	editor.client.FocalPoint.SetPosition(pos)
}

func (*ShelterEditor) Name() string { return "shelter" }
func (*ShelterEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"polygon",
			"divider",
			"fencing",
			"doorway",
			"windows",
			"surface",
			"rooftop",
			"columns",
			"ladders",
			"hanging",
		}
	case ModeDressing:
		return []string{
			"bedding",
			"kitchen",
			"bathing",
			"benches",
			"candles",
			"lesiure",
			"trinket",
			"storage",
			"mounted",
			"chimney",
			"carpets",
		}
	default:
		return TextureTabs
	}
}

func (editor *ShelterEditor) EnableEditor() {
	editor.client.SetGizmos(placementGizmosWithScale())
	shader := ShaderMaterial.New()
	shader.SetShader(LoadSync[Shader.Instance]("res://shader/grid.gdshader"))
	editor.grid_shader = shader.ID()
	editor.client.FocalPoint.Lens.Camera.Cover.SetSurfaceOverrideMaterial(0, shader.AsMaterial())
}
func (editor *ShelterEditor) ChangeEditor() {
	// Hand the cover back to the default underwater post-process rather than
	// clearing it, so the waterline/underwater effect survives leaving shelter.
	editor.client.applyCoverDefault()
}

func (editor *ShelterEditor) SelectDesign(mode Mode, design string) {
	editor.Preview.SetDesign(design)
}

var _ ClickableEditor = (*ShelterEditor)(nil)

func (*ShelterEditor) EditorID() string { return "shelter" }

// GizmoManipulable implements [ClickableEditor]. Shelter has no modal
// sub-views that own their own drag, so gizmos are always available.
func (*ShelterEditor) GizmoManipulable() bool { return true }

// EntityForNode implements [ClickableEditor]. Shelter parts placed from
// library scenes nest the pickable mesh under an entity-root node, so a
// pick may land on a child; we resolve the node directly first, then
// walk up one level to the owning anchor.
func (editor *ShelterEditor) EntityForNode(node Node3D.Instance) (musical.Entity, Node3D.Instance, bool) {
	if e, has := editor.object_to_entity[Node3D.ID(node.ID())]; has {
		return e, node, true
	}
	if parent := node.GetParentNode3d(); parent != Node3D.Nil {
		if e, has := editor.object_to_entity[Node3D.ID(parent.ID())]; has {
			return e, parent, true
		}
	}
	return musical.Entity{}, Node3D.Nil, false
}

// DesignForNode implements [ClickableEditor], resolving through the same
// owner-walk as EntityForNode before scanning the design map.
func (editor *ShelterEditor) DesignForNode(node Node3D.Instance) (musical.Design, bool) {
	_, owner, ok := editor.EntityForNode(node)
	if !ok {
		return musical.Design{}, false
	}
	return findDesignInMap(editor.design_to_entity, Node3D.ID(owner.ID()))
}

func (*ShelterEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*ShelterEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}

func (editor *ShelterEditor) Change(change musical.Change) error {
	if change.Editor != "shelter" {
		return nil
	}
	change.Offset.Y += Float.Random() * 0.001 // quick fix for z-fighting
	container := editor.Objects.AsNode()
	exists, ok := editor.entity_to_object[change.Entity].Instance()
	if ok {
		if change.Remove {
			removeEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, exists)
			return nil
		}
		exists.
			SetPosition(change.Offset).
			SetRotation(change.Angles)
		// Apply explicit Bounds if present (scale gizmo); otherwise
		// leave whatever scale the instance already has so translate/
		// twist edits don't stomp the creation-time 0.2 factor (which
		// already incorporates any design root "preset scale").
		if change.Bounds != Vector3.Zero {
			exists.SetScale(change.Bounds)
		}
		return nil
	}
	var node Node3D.Instance
	level := int(Float.Round(change.Offset.Y))
	design := editor.client.design_to_string[change.Design]
	if FileAccess.FileExists(strings.TrimSuffix(design, path.Ext(design)) + "_cut.glb.import") {
		node = Node3D.New()
		scene, ok := editor.client.sceneFor(change.Design)
		if ok {
			full := Object.To[Node3D.Instance](scene.Instantiate())
			full.AsNode().AddToGroup("floor_whole_" + strconv.Itoa(level))
			full.SetVisible(editor.explore || editor.current_level != level)
			node.AsNode().AddChild(full.AsNode())
			cut := LoadSync[PackedScene.Is[Node3D.Instance]](strings.TrimSuffix(design, path.Ext(design)) + "_cut.glb").Instantiate()
			cut.SetVisible(!editor.explore && editor.current_level == level)
			cut.AsNode().AddToGroup("floor_short_" + strconv.Itoa(level))
			node.AsNode().AddChild(cut.AsNode())
		}
	} else {
		node = editor.client.instantiateDesign(change.Design)
		kind := designCategory(design)
		if kind == "hanging" || kind == "mounted" {
			node.AsNode().AddToGroup("floor_whole_" + strconv.Itoa(level))
		}
	}
	if level > editor.levels {
		editor.levels = level
		if editor.client.Editing == Editing.Shelter {
			editor.client.ui.ViewSelector.Refresh(editor.client.ui.ViewSelector.view, editor.Views())
		}
	}
	node.AsNode().AddToGroup("floor_" + strconv.Itoa(level))
	node.
		SetPosition(change.Offset).
		SetRotation(change.Angles)
	if change.Bounds != Vector3.Zero {
		node.SetScale(change.Bounds)
	} else {
		node.SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.2, 0.2, 0.2)))
	}
	registerEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, node)
	container.AddChild(node.AsNode())
	// Library-sizing debug mode: preview the sizes.txt entry for this part
	// (no-op outside debug mode). Shelter parts anchor to the level grid,
	// not the terrain, hence terrainSeated=false.
	editor.client.applyLibrarySizeOverride(change.Entity, change.Design, node, false)
	return nil
}

func (editor *ShelterEditor) UnhandledInput(event InputEvent.Instance) {
	if !editor.AsNode3D().Visible() {
		return
	}
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.ButtonIndex() == Input.MouseButtonRight && event.AsInputEvent().IsPressed() {
		editor.Preview.Remove()
	}
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
		editor.client.space.Change(musical.Change{
			Author: editor.client.id,
			Entity: editor.client.NextEntity(),
			Design: editor.client.MusicalDesign(editor.Preview.Design()),
			Offset: editor.Preview.AsNode3D().Position(),
			Angles: editor.Preview.AsNode3D().Rotation(),
			Editor: "shelter",
			Commit: true,
		})
		if !Input.IsKeyPressed(Input.KeyShift) {
			editor.Preview.Remove()
		}
	}
	if _, ok := Object.As[InputEventMouseMotion.Instance](event); ok {
		editor.last_mouse_change = time.Now()
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if isDeletePress(event) {
			node, ok := editor.client.selection.Instance()
			if ok {
				entity, ok := editor.object_to_entity[Node3D.ID(node.ID())]
				if !ok {
					entity, ok = editor.object_to_entity[node.GetParentNode3d().ID()]
				}
				if ok {
					editor.client.space.Change(musical.Change{
						Author: editor.client.id,
						Entity: entity,
						Editor: "shelter",
						Remove: true,
						Commit: true,
					})
				}
			}
		}
	}
}

func (editor *ShelterEditor) PhysicsProcess(_ Float.X) {
	if design := editor.Preview.Design(); design != "" {
		mouse := Viewport.Get(editor.AsNode()).GetMousePosition()
		kind := designCategory(design)
		switch editor.client.ui.mode {
		case ModeDressing:
			if hover := MousePicker(editor.AsNode3D()); hover.Collider != Object.Nil {
				point := hover.Position
				switch kind {
				case "mounted":
					point.Y = Float.Floor(point.Y)
					normal := hover.Normal
					// Only allow mounting on vertical surfaces (normal.Y == 0)
					if Float.Abs(normal.Y) > 0.01 {
						break
					}
					// Only rotate around Y axis to face the surface
					angle := Angle.Atan2(normal.X, normal.Z)
					editor.Preview.AsNode3D().
						SetRotation(Euler.Radians{Y: angle}).
						SetGlobalPosition(point)
				case "chimney":
					point.Y -= 0.1
					editor.Preview.AsNode3D().SetGlobalPosition(point)
				default:
					node := Object.To[Node3D.Instance](hover.Collider)
					// Only allow mounting on horizontal surfaces (normal.Y == 1)
					if Float.Abs(hover.Normal.Y-1) > 0.01 || node.GetParentNode3d().AsNode().IsInGroup("floor_short_"+strconv.Itoa(editor.current_level)) {
						break
					}
					editor.Preview.AsNode3D().SetGlobalPosition(point)
				}
			}
		case ModeGeometry:
			if point, ok := Plane.IntersectsRay(editor.current_plane,
				editor.client.FocalPoint.Lens.Camera.ProjectRayOrigin(mouse),
				editor.client.FocalPoint.Lens.Camera.ProjectRayNormal(mouse),
			); ok {
				var angle Angle.Radians
				// Determine which triangular quadrant the intersection point is in and set angle accordingly
				theta := Angle.Atan2(point.Z-Float.Round(point.Z), point.X-Float.Round(point.X)) // returns radians
				same_direction := false
				switch {
				case theta >= -Angle.Pi/4 && theta < Angle.Pi/4:
					angle = -Angle.Pi / 2
					if editor.last_angle_change == Angle.Pi/2 {
						same_direction = true
					}
				case theta >= Angle.Pi/4 && theta < 3*Angle.Pi/4:
					angle = Angle.Pi
					if editor.last_angle_change == 0 {
						same_direction = true
					}
				case theta >= -3*Angle.Pi/4 && theta < -Angle.Pi/4:
					angle = 0
					if editor.last_angle_change == Angle.Pi {
						same_direction = true
					}
				default:
					angle = Angle.Pi / 2
					if editor.last_angle_change == -Angle.Pi/2 {
						same_direction = true
					}
				}
				if time.Since(editor.last_mouse_change) > time.Millisecond*50 || same_direction {
					editor.last_angle_change = angle
					editor.Preview.AsNode3D().SetRotation(Euler.Radians{Y: angle})
				}
				aabb := editor.Preview.AABB()
				if kind == "columns" && aabb.Size.X < 1 && aabb.Size.Z < 1 {
					point.X = Float.Round(point.X*2) / 2
					point.Z = Float.Round(point.Z*2) / 2
				} else {
					point.X = Float.Round(point.X)
					point.Z = Float.Round(point.Z)
				}
				if kind == "surface" {
					point.Y -= 0.05
				}
				editor.Preview.AsNode3D().SetGlobalPosition(point)
			}
		}
	}
}
