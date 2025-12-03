package internal

import (
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
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

	current_level Plane.NormalD
	levels        int

	grid_shader ShaderMaterial.ID

	client *Client

	last_angle_change Angle.Radians
	last_mouse_change time.Time

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	entity_to_mirror map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity
}

func (editor *ShelterEditor) Ready() {
	editor.design_to_entity = make(map[musical.Design][]Node3D.ID)
	editor.entity_to_object = make(map[musical.Entity]Node3D.ID)
	editor.object_to_entity = make(map[Node3D.ID]musical.Entity)
	editor.entity_to_mirror = make(map[musical.Entity]Node3D.ID)

	editor.current_level = Plane.NormalD{Normal: Vector3.XYZ{0, 1, 0}}
	editor.Preview.AsNode3D().SetScale(Vector3.MulX(editor.Preview.AsNode3D().Scale(), 0.2))
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
	switch view {
	case "explore":
	case "unicode/G":
		shader, _ := editor.grid_shader.Instance()
		shader.SetShaderParameter("center_offset", Vector3.New(0, 0, 0))
		editor.current_level = Plane.NormalD{Normal: Vector3.XYZ{0, 1, 0}}
		pos := editor.client.FocalPoint.Position()
		pos.Y = 0
		editor.client.FocalPoint.SetPosition(pos)
	case "unicode/+":
		editor.levels++
		editor.client.ui.ViewSelector.Refresh(editor.levels+1, editor.Views())
	default:
		if level_str, ok := strings.CutPrefix(view, "unicode/"); ok {
			level, err := strconv.Atoi(level_str)
			if err == nil {
				editor.current_level = Plane.NormalD{Normal: Vector3.XYZ{0, 1, 0}, D: Float.X(level)}
				shader, _ := editor.grid_shader.Instance()
				shader.SetShaderParameter("center_offset", Vector3.New(0, float64(level), 0))
				pos := editor.client.FocalPoint.Position()
				pos.Y = Float.X(level)
				editor.client.FocalPoint.SetPosition(pos)
			}
		}
	}
}

func (*ShelterEditor) Name() string { return "shelter" }
func (*ShelterEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"polygon",
			"divider",
			"doorway",
			"windows",
			"surface",
			"roofing",
			"columns",
			"ladders",
			"chimney",
		}
	case ModeDressing:
		return []string{
			"bedding",
			"kitchen",
			"bathing",
			"storage",
			"benches",
			"seating",
			"candles",
			"lesiure",
			"trinket",
		}
	default:
		return TextureTabs
	}
}

func (editor *ShelterEditor) EnableEditor() {
	shader := ShaderMaterial.New()
	shader.SetShader(Resource.Load[Shader.Instance]("res://shader/grid.gdshader"))
	editor.grid_shader = shader.ID()
	editor.client.FocalPoint.Lens.Camera.Cover.SetSurfaceOverrideMaterial(0, shader.AsMaterial())
}
func (editor *ShelterEditor) ChangeEditor() {
	editor.client.FocalPoint.Lens.Camera.Cover.SetSurfaceOverrideMaterial(0, Material.Nil)
}

func (editor *ShelterEditor) SelectDesign(mode Mode, design string) {
	editor.Preview.SetDesign(design)
}

func (*ShelterEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*ShelterEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}

func (editor *ShelterEditor) Change(change musical.Change) error {
	container := editor.Objects.AsNode()
	exists, ok := editor.entity_to_object[change.Entity].Instance()
	if ok {
		if change.Remove {
			idx := slices.Index(editor.design_to_entity[change.Design], exists.ID())
			if idx >= 0 {
				editor.design_to_entity[change.Design] = slices.Delete(editor.design_to_entity[change.Design], idx, idx)
			}
			exists.AsNode().QueueFree()
			return nil
		}
		exists.SetPosition(change.Offset)
		exists.SetRotation(change.Angles)
		exists.SetScale(Vector3.New(0.2, 0.2, 0.2))
		return nil
	}
	var node Node3D.Instance
	scene, ok := editor.client.packed_scenes[change.Design].Instance()
	if ok {
		node = Object.To[Node3D.Instance](scene.Instantiate())
	} else {
		node = Node3D.New()
	}
	node.SetPosition(change.Offset)
	node.SetRotation(change.Angles)
	node.SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.2, 0.2, 0.2)))
	editor.entity_to_object[change.Entity] = node.ID()
	editor.object_to_entity[node.ID()] = change.Entity
	editor.design_to_entity[change.Design] = append(editor.design_to_entity[change.Design], node.ID())
	container.AddChild(node.AsNode())
	return nil
}

func (editor *ShelterEditor) Input(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.ButtonIndex() == Input.MouseButtonRight && event.AsInputEvent().IsPressed() {
		editor.Preview.Remove()
	}
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
		editor.client.entity_ids[editor.client.id]++
		editor.client.space.Change(musical.Change{
			Author: editor.client.id,
			Entity: musical.Entity{
				Author: editor.client.id,
				Number: editor.client.entity_ids[editor.client.id],
			},
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
		if event.AsInputEvent().IsPressed() && (event.Keycode() == Input.KeyDelete || event.Keycode() == Input.KeyBackspace) && !event.AsInputEvent().IsEcho() {
			node, ok := editor.client.selection.Instance()
			if ok {
				if entity, ok := editor.object_to_entity[Node3D.ID(node.ID())]; ok {
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

func (editor *ShelterEditor) PhysicsProcess(delta Float.X) {
	if design := editor.Preview.Design(); design != "" {
		mouse := Viewport.Get(editor.AsNode()).GetMousePosition()
		if point, ok := Plane.IntersectsRay(editor.current_level,
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
			point.X = Float.Round(point.X)
			point.Z = Float.Round(point.Z)
			if path.Base(path.Dir(design)) == "surface" {
				point.Y -= editor.Preview.AABB().Size.Y / 2
			}
			editor.Preview.AsNode3D().SetGlobalPosition(point)
		}
	}
}
