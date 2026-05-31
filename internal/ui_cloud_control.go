package internal

import (
	"sync/atomic"
	"time"

	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/GradientTexture2D"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSeparator"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/ProgressBar"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/RichTextLabel"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/Tween"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/Window"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Signal"
	"graphics.gd/variant/Vector2"
	"the.quetzal.community/aviary/internal/networking"
)

type CloudControl struct {
	Control.Extension[CloudControl]

	JoinCode struct {
		Panel.Instance

		Label       Label.Instance
		ShareButton TextureButton.Instance
		Versioning  struct {
			HBoxContainer.Instance

			Version RichTextLabel.Instance
			Restart TextureButton.Instance
			Updates TextureRect.Instance
		}
	}
	HBoxContainer struct {
		HBoxContainer.Instance

		Cloud struct {
			TextureButton.Instance

			OnlineIndicator TextureRect.Instance
		}
	}
	Keypad struct {
		Panel.Instance

		TextEdit TextEdit.Instance

		Keys GridContainer.Instance
	}

	GizmoTypes struct {
		VBoxContainer.Instance

		// Duplicate and Delete are the persistent action buttons for
		// GizmoClone / GizmoTrash. Their OnPressed handlers are wired
		// once in UI.Ready. They are never passed through AddChild
		// (they are Godot-owned nodes from the .tscn). SetGizmos only
		// reorders them via MoveChild to honour the editor's list.
		Duplicate TextureButton.Instance
		Delete    TextureButton.Instance
	}
	GizmoIndicator TextureRect.Instance

	UpdateProgress ProgressBar.Instance

	Gizmo Gizmo

	// allowedGizmos is the set of gizmos the active editor has declared
	// visible via SetGizmos. Nil means "all gizmos allowed" (the default
	// before any editor restricts the toolbar).
	allowedGizmos map[Gizmo]bool

	sharing    bool
	client     *Client
	on_process chan func(*CloudControl)

	// sizeSlider is the terrain brush-size slider built in code and
	// parented to CloudControl (a sibling of GizmoTypes, like
	// GizmoIndicator). It only shows while the terrain editor is active
	// and is pinned next to the Shift gizmo button each frame.
	sizeSlider      HSlider.Instance
	sizeSliderReady bool

	// densitySlider is the terrain dressing-density slider, shown only
	// while the terrain editor is in ModeDressing. It's pinned just below
	// the size slider and feeds the "dressing/density" slider channel.
	densitySlider      HSlider.Instance
	densitySliderReady bool

	// powerSlider is the terrain height-sculpt power slider, shown only
	// while the terrain editor is in ModeGeometry. It's pinned next to the
	// GizmoPower button and feeds the "editing/power" slider channel.
	powerSlider      HSlider.Instance
	powerSliderReady bool

	// gizmoButtons maps the Gizmo values currently present in the toolbar
	// (per the active editor's SetGizmos order) to their TextureButton (or
	// Control for the action buttons). These are always *borrowed*
	// references (obtained via GetNode/GetChild after the nodes are in the
	// tree) so that gizmoControl and the slider positioning helpers can
	// safely query Position/Size without any ownership-transfer issues.
	gizmoButtons map[Gizmo]Control.Instance
}

type Gizmo int

const (
	GizmoPoint Gizmo = iota
	GizmoShift
	GizmoTwist
	GizmoFloat

	GizmoSpace // Horizontal Rule

	GizmoClone
	GizmoTrash

	GizmoBrush
	GizmoPower
	GizmoScale
	GizmoErase
)

var setting_up atomic.Bool
var version string

func (ui *CloudControl) Setup() {
	ui.on_process = make(chan func(*CloudControl), 10)
	if !setting_up.CompareAndSwap(false, true) {
		return
	}
	go ui.automaticallyUpdate()
}

func (ui *CloudControl) Input(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() {
			switch event.Keycode() {
			case Input.KeyShift:
				if Input.IsKeyPressed(Input.KeyCtrl) {
					ui.set_gizmo(GizmoFloat)
				} else {
					ui.set_gizmo(GizmoShift)
				}
			case Input.KeyCtrl:
				if Input.IsKeyPressed(Input.KeyShift) {
					ui.set_gizmo(GizmoFloat)
				} else {
					ui.set_gizmo(GizmoTwist)
				}
			}
		} else {
			switch event.Keycode() {
			case Input.KeyShift, Input.KeyCtrl:
				// Re-evaluate the current temporary gizmo based on the
				// active modifier keys. Releasing the last of Shift/Ctrl
				// (including the Ctrl+Shift combo) always returns to the
				// neutral Point tool.
				switch {
				case Input.IsKeyPressed(Input.KeyShift) && Input.IsKeyPressed(Input.KeyCtrl):
					ui.set_gizmo(GizmoFloat)
				case Input.IsKeyPressed(Input.KeyShift):
					ui.set_gizmo(GizmoShift)
				case Input.IsKeyPressed(Input.KeyCtrl):
					ui.set_gizmo(GizmoTwist)
				default:
					ui.set_gizmo(GizmoPoint)
				}
			}
		}
	}
}

func (ui *CloudControl) set_gizmo(gizmo Gizmo) {
	ui.Gizmo = gizmo
	child := ui.gizmoControl(gizmo)
	if child == Control.Nil {
		return
	}
	types := ui.GizmoTypes.AsControl()
	indicator := ui.GizmoIndicator.AsControl()
	childCenter := Vector2.Add(
		types.Position(),
		Vector2.Mul(Vector2.Add(child.Position(), Vector2.MulX(child.Size(), 0.5)), types.Scale()),
	)
	indicatorHalf := Vector2.MulX(Vector2.Mul(indicator.Size(), indicator.Scale()), 0.5)
	target := Vector2.Sub(childCenter, indicatorHalf)
	PropertyTweener.Make(indicator.AsNode().CreateTween(), indicator.AsObject(), "position", target, 0.1).SetEase(Tween.EaseOut)
}

func (ui *CloudControl) Ready() {
	assertMainThread("CloudControl.Ready")
	// Gizmo buttons are now created dynamically by SetGizmos (which
	// runs on every editor switch) and wired at creation time in
	// newGizmoButton. The persistent Duplicate/Delete action buttons
	// have their OnPressed handlers wired from UI.Ready.
	ui.JoinCode.ShareButton.AsBaseButton().OnPressed(func() {
		if time.Now().After(UserState.Aviary.TogetherUntil) {
			OS.ShellOpen("https://the.quetzal.community/aviary/together?authorise=" + UserState.Secret)
			Object.To[Window.Instance](Viewport.Get(ui.AsNode())).OnFocusEntered(func() {
				ui.Setup()
			}, Signal.OneShot)
			return
		}
		if !ui.sharing {
			ui.sharing = true
			var spinner = LoadSync[Shader.Instance]("res://shader/spinner.gdshader")
			var material = ShaderMaterial.New()
			material.SetShader(spinner)
			ui.JoinCode.ShareButton.AsCanvasItem().SetMaterial(material.AsMaterial())
			go func() {
				code, err := ui.client.apiHost()
				if err != nil {
					Engine.Raise(err)
					ui.on_process <- func(cc *CloudControl) { cc.set_join_code("") }
					return
				}
				ui.on_process <- func(cc *CloudControl) { cc.set_join_code(code) }
				time.Sleep(5 * time.Minute)
				ui.on_process <- func(cc *CloudControl) { cc.set_join_code("") }
			}()
		}
	})

	ui.buildSizeSlider()
	ui.buildDensitySlider()
	ui.buildPowerSlider()
}

func (ui *CloudControl) set_update_available(restart func(), available bool) {
	if available {
		ui.UpdateProgress.AsCanvasItem().SetVisible(true)
		ui.JoinCode.Versioning.Updates.AsCanvasItem().SetVisible(true)
	} else {
		ui.JoinCode.Versioning.Version.SetText("[s]" + ui.JoinCode.Versioning.Version.Text() + "[/s]")
		ui.JoinCode.Versioning.Updates.AsCanvasItem().SetVisible(false)
		ui.JoinCode.Versioning.Restart.AsCanvasItem().SetVisible(true)
		ui.JoinCode.Versioning.Restart.AsBaseButton().OnPressed(restart)
		ui.UpdateProgress.AsCanvasItem().SetVisible(false)
	}
}

func (ui *CloudControl) set_online_status_indicator(online bool) {
	var col = Color.X11.Green
	if !online {
		col = Color.X11.Red
	}
	tex := ui.HBoxContainer.Cloud.OnlineIndicator.Texture()
	grad := Object.To[GradientTexture2D.Instance](tex).Gradient()
	cols := grad.Colors()
	cols[0] = col
	grad.SetColors(cols)
}

func (ui *CloudControl) set_join_code(code networking.Code) {
	ui.JoinCode.ShareButton.AsCanvasItem().SetMaterial(Material.Nil)
	size := ui.JoinCode.AsControl().Size()
	if code != "" {
		size.X = 184
	} else {
		size.X = 54
	}
	PropertyTweener.Make(SceneTree.Get(ui.AsNode()).CreateTween(), ui.JoinCode.AsControl().AsObject(), "size", size, 0.2).SetEase(Tween.EaseOut)
	ui.JoinCode.Label.SetText(string(code))
	ui.sharing = false
}

func (ui *CloudControl) Process(dt Float.X) {
	ui.positionSizeSlider()
	ui.positionDensitySlider()
	ui.positionPowerSlider()
	for {
		select {
		case fn := <-ui.on_process:
			if fn != nil {
				fn(ui)
			}
		default:
			return
		}
	}
}

// newToolbarSlider builds a hidden horizontal slider styled to match the
// gizmo toolbar (tall hit rect that stops mouse fall-through, downscaled
// themed grabber) and wires its value_changed to onChanged. Shared by the
// brush-size and dressing-density sliders.
func (ui *CloudControl) newToolbarSlider(min, max, step, init float64, onChanged func(value Float.X)) HSlider.Instance {
	assertMainThread("newToolbarSlider")
	slider := HSlider.Advanced(HSlider.New())
	slider.AsRange().SetMin(min)
	slider.AsRange().SetMax(max)
	slider.AsRange().SetStep(step)
	slider.AsRange().SetValue(init)
	// Make the hit rect as tall as the grabber (64) — not the slim 24 the
	// track needs — so pressing anywhere on the visible handle lands on the
	// control. With MouseFilterStop, Godot then consumes the whole drag and
	// it never falls through to terrain sculpt/paint or selection in the world.
	HSlider.Instance(slider).AsControl().SetCustomMinimumSize(Vector2.New(280, 64))
	HSlider.Instance(slider).AsControl().SetMouseFilter(Control.MouseFilterStop)
	HSlider.Instance(slider).AsCanvasItem().SetVisible(false)
	// Match the Settings menu slider's handle: the themed grabber
	// (res://ui/slider.png) is 128×128 and Godot draws it at its native
	// size, so downscale a copy and override it on just this slider.
	const grabberSize = 64
	if tex := LoadSync[Texture2D.Instance]("res://ui/slider.png"); tex != Texture2D.Nil {
		if img := tex.GetImage(); img != Image.Nil {
			img.Resize(grabberSize, grabberSize)
			small := ImageTexture.CreateFromImage(img).AsTexture2D()
			for _, name := range []string{"grabber", "grabber_highlight", "grabber_disabled"} {
				HSlider.Instance(slider).AsControl().AddThemeIconOverride(name, small)
			}
		}
	}
	Range.Instance(slider.AsRange()).OnValueChanged(onChanged)
	ui.AsNode().AddChild(Node.Instance(slider.AsNode()))
	return HSlider.Instance(slider)
}

// buildSizeSlider creates the terrain brush-size slider shown in the
// gizmo toolbar beside the Shift button. It's created hidden; the editor
// switch (setSizeSliderVisible) reveals it only while the terrain editor
// is active, and positionSizeSlider keeps it pinned next to Shift. The
// value is forwarded to the terrain editor through the same
// SliderHandle/SliderConfig contract the design explorer used.
func (ui *CloudControl) buildSizeSlider() {
	ui.sizeSlider = ui.newToolbarSlider(0, 10, 0.01, 2, func(value Float.X) {
		if ui.client == nil {
			return
		}
		ui.client.TerrainEditor.SliderHandle(ModeGeometry, "editing/radius", float64(value), false)
	})
	ui.sizeSliderReady = true
}

// buildDensitySlider creates the terrain dressing-density slider. Like the
// size slider it's created hidden and pinned in the toolbar; it's only
// revealed while the terrain editor is in ModeDressing.
func (ui *CloudControl) buildDensitySlider() {
	ui.densitySlider = ui.newToolbarSlider(0, 1, 0.01, 0.5, func(value Float.X) {
		if ui.client == nil {
			return
		}
		ui.client.TerrainEditor.SliderHandle(ModeDressing, "dressing/density", float64(value), false)
	})
	ui.densitySliderReady = true
}

// buildPowerSlider creates the GizmoPower toolbar slider. Like the size slider
// it's created hidden and pinned in the toolbar; it's only revealed while the
// terrain editor is in ModeGeometry. It follows the active brush, feeding
// whichever editing channel GizmoPowerEditing reports (sculpt power for
// raise/lower, channel depth for the river tools).
func (ui *CloudControl) buildPowerSlider() {
	ui.powerSlider = ui.newToolbarSlider(0.1, 10, 0.1, 2, func(value Float.X) {
		if ui.client == nil {
			return
		}
		editing := ui.client.TerrainEditor.GizmoPowerEditing()
		ui.client.TerrainEditor.SliderHandle(ModeGeometry, editing, float64(value), false)
	})
	ui.powerSliderReady = true
}

// setSizeSliderVisible reveals or hides the terrain brush-size slider.
// When revealing, it syncs the slider's range and current value from the
// terrain editor so it reflects the live brush radius.
func (ui *CloudControl) setSizeSliderVisible(v bool) {
	assertMainThread("setSizeSliderVisible")
	if !ui.sizeSliderReady {
		return
	}
	if v && ui.client != nil {
		init, min, max, step := ui.client.TerrainEditor.SliderConfig(ModeGeometry, "editing/radius")
		r := Range.Advanced(ui.sizeSlider.AsRange())
		r.SetMin(min)
		r.SetMax(max)
		r.SetStep(step)
		ui.sizeSlider.AsRange().SetValueNoSignal(Float.X(init))
	}
	ui.sizeSlider.AsCanvasItem().SetVisible(v)
}

// setSizeSliderValue moves the size slider's handle to v without
// re-emitting value_changed — the caller has already applied the radius
// (e.g. Shift+scroll resizing the terrain brush).
func (ui *CloudControl) setSizeSliderValue(v float64) {
	assertMainThread("setSizeSliderValue")
	if !ui.sizeSliderReady {
		return
	}
	ui.sizeSlider.AsRange().SetValueNoSignal(Float.X(v))
}

// setDensitySliderVisible reveals or hides the dressing-density slider,
// syncing its range and value from the terrain editor when revealing.
func (ui *CloudControl) setDensitySliderVisible(v bool) {
	assertMainThread("setDensitySliderVisible")
	if !ui.densitySliderReady {
		return
	}
	if v && ui.client != nil {
		init, min, max, step := ui.client.TerrainEditor.SliderConfig(ModeDressing, "dressing/density")
		r := Range.Advanced(ui.densitySlider.AsRange())
		r.SetMin(min)
		r.SetMax(max)
		r.SetStep(step)
		ui.densitySlider.AsRange().SetValueNoSignal(Float.X(init))
	}
	ui.densitySlider.AsCanvasItem().SetVisible(v)
}

// setPowerSliderVisible reveals or hides the GizmoPower toolbar slider, syncing
// its range and value to whatever parameter the active brush exposes (via
// GizmoPowerEditing) when revealing. Also called on a terrain-tool change to
// re-sync the slider to the newly selected brush's parameter.
func (ui *CloudControl) setPowerSliderVisible(v bool) {
	assertMainThread("setPowerSliderVisible")
	if !ui.powerSliderReady {
		return
	}
	if v && ui.client != nil {
		editing := ui.client.TerrainEditor.GizmoPowerEditing()
		init, min, max, step := ui.client.TerrainEditor.SliderConfig(ModeGeometry, editing)
		r := Range.Advanced(ui.powerSlider.AsRange())
		r.SetMin(min)
		r.SetMax(max)
		r.SetStep(step)
		ui.powerSlider.AsRange().SetValueNoSignal(Float.X(init))
	}
	ui.powerSlider.AsCanvasItem().SetVisible(v)
}

// setPowerSliderValue moves the GizmoPower slider's handle to v without
// re-emitting value_changed — the caller has already applied the value
// (e.g. Ctrl+scroll adjusting the active brush's power / river depth).
func (ui *CloudControl) setPowerSliderValue(v float64) {
	assertMainThread("setPowerSliderValue")
	if !ui.powerSliderReady {
		return
	}
	ui.powerSlider.AsRange().SetValueNoSignal(Float.X(v))
}

// positionSizeSlider pins the size slider just to the right of the Shift
// gizmo button each frame while it's visible, using the same lookup as
// set_gizmo so the slider tracks the (possibly dynamically ordered) button.
func (ui *CloudControl) positionSizeSlider() {
	if !ui.sizeSliderReady || !ui.sizeSlider.AsCanvasItem().Visible() {
		return
	}
	// The size slider is only shown for the terrain editor, whose sole gizmo
	// is the brush, so pin it next to that button.
	shift := ui.gizmoControl(GizmoBrush)
	if shift == Control.Nil {
		return
	}
	types := ui.GizmoTypes.AsControl()
	// Right-edge vertical-centre of the Shift button in CloudControl space.
	edge := Vector2.Add(
		types.Position(),
		Vector2.Mul(
			Vector2.Add(shift.Position(), Vector2.New(shift.Size().X, shift.Size().Y*0.5)),
			types.Scale(),
		),
	)
	const gap = 12
	size := ui.sizeSlider.AsControl().Size()
	assertMainThread("positionSizeSlider")
	ui.sizeSlider.AsControl().SetPosition(Vector2.New(edge.X+gap, edge.Y-size.Y*0.5))
}

// positionDensitySlider pins the dressing-density slider just below the
// brush-size slider's slot (both sit to the right of the Shift button), so
// the two stack vertically in the toolbar. It uses gizmoControl for lookup.
func (ui *CloudControl) positionDensitySlider() {
	if !ui.densitySliderReady || !ui.densitySlider.AsCanvasItem().Visible() {
		return
	}
	// Stacks under the size slider, anchored to the same terrain brush button.
	shift := ui.gizmoControl(GizmoBrush)
	if shift == Control.Nil {
		return
	}
	types := ui.GizmoTypes.AsControl()
	edge := Vector2.Add(
		types.Position(),
		Vector2.Mul(
			Vector2.Add(shift.Position(), Vector2.New(shift.Size().X, shift.Size().Y*0.5)),
			types.Scale(),
		),
	)
	const gap = 12
	const stack = 56 // vertical offset below the size slider's centre line
	size := ui.densitySlider.AsControl().Size()
	assertMainThread("positionDensitySlider")
	ui.densitySlider.AsControl().SetPosition(Vector2.New(edge.X+gap, edge.Y-size.Y*0.5+stack))
}

// positionPowerSlider pins the height-sculpt power slider just to the right
// of the GizmoPower button each frame while it's visible, the same way the
// size slider tracks the GizmoBrush button.
func (ui *CloudControl) positionPowerSlider() {
	if !ui.powerSliderReady || !ui.powerSlider.AsCanvasItem().Visible() {
		return
	}
	power := ui.gizmoControl(GizmoPower)
	if power == Control.Nil {
		return
	}
	types := ui.GizmoTypes.AsControl()
	// Right-edge vertical-centre of the GizmoPower button in CloudControl space.
	edge := Vector2.Add(
		types.Position(),
		Vector2.Mul(
			Vector2.Add(power.Position(), Vector2.New(power.Size().X, power.Size().Y*0.5)),
			types.Scale(),
		),
	)
	const gap = 12
	size := ui.powerSlider.AsControl().Size()
	assertMainThread("positionPowerSlider")
	ui.powerSlider.AsControl().SetPosition(Vector2.New(edge.X+gap, edge.Y-size.Y*0.5))
}

// isGizmoAllowed reports whether the given gizmo is currently permitted
// by the active editor's SetGizmos declaration. When no editor has
// restricted the set, everything is allowed.
func (ui *CloudControl) isGizmoAllowed(g Gizmo) bool {
	if ui.allowedGizmos == nil {
		return true
	}
	return ui.allowedGizmos[g]
}

// gizmoIconName returns the basename (without extension) for the
// texture that represents this gizmo. The texture is loaded from
// res://ui/gizmo/<name>.svg when the button is first needed.
func gizmoIconName(g Gizmo) string {
	switch g {
	case GizmoPoint:
		return "point"
	case GizmoShift:
		return "shift"
	case GizmoTwist:
		return "twist"
	case GizmoFloat:
		return "float"
	case GizmoBrush:
		return "brush"
	case GizmoPower:
		return "power"
	case GizmoScale:
		return "scale"
	case GizmoErase:
		return "erase"
	case GizmoClone:
		return "clone"
	case GizmoTrash:
		return "trash"
	default:
		return ""
	}
}

// newGizmoButton creates a fresh TextureButton for the given gizmo kind
// (always a new owned object via .New()). The caller is responsible for
// AddChild'ing it exactly once. It loads the icon from the named path
// under ui/gizmo/ and wires the press handler. Returns Nil for unknown
// gizmos.
func (ui *CloudControl) newGizmoButton(g Gizmo) TextureButton.Instance {
	name := gizmoIconName(g)
	if name == "" {
		return TextureButton.Nil
	}
	path := "res://ui/gizmo/" + name + ".svg"
	tex := LoadSync[Texture2D.Instance](path)

	btn := TextureButton.New()
	btn.
		SetIgnoreTextureSize(true).
		SetStretchMode(TextureButton.StretchKeepAspectCentered).
		AsControl().SetCustomMinimumSize(Vector2.New(42, 42))
	if tex != Texture2D.Nil {
		btn.SetTextureNormal(tex)
	}

	// The closure captures the specific Gizmo value for this button.
	gVal := g
	btn.AsBaseButton().OnPressed(func() {
		ui.set_gizmo(gVal)
	})

	// Give it a stable name so later (after AddChild) we can safely
	// obtain a borrowed reference via GetNode for the gizmoButtons map
	// and for positioning without ever holding a transferable Instance
	// across ownership boundaries.
	btn.AsNode().SetName("Gizmo_" + name)

	return TextureButton.Instance(btn)
}

// newGizmoSeparator creates a fresh thin horizontal rule (always a new
// owned object). The caller AddChild's it once.
func (ui *CloudControl) newGizmoSeparator() HSeparator.Instance {
	sep := HSeparator.New()
	HSeparator.Instance(sep).AsControl().SetCustomMinimumSize(Vector2.New(42, 8))
	// Name is not strictly needed for separators (they are never looked
	// up in gizmoButtons), but helps during debugging.
	HSeparator.Instance(sep).AsNode().SetName("GizmoSpace")
	return HSeparator.Instance(sep)
}

// gizmoControl returns the Control that visually represents g in the
// current toolbar (if any). Used for indicator positioning and slider
// attachment. All entries are borrowed references (safe for Position/
// Size queries). Returns Nil for separators and for gizmos not offered.
func (ui *CloudControl) gizmoControl(g Gizmo) Control.Instance {
	if ui.gizmoButtons != nil {
		if c, ok := ui.gizmoButtons[g]; ok && c != Control.Nil {
			return c
		}
	}
	// Fallbacks for the two permanent action buttons (they are always
	// children of GizmoTypes once the first rebuild has run).
	switch g {
	case GizmoClone:
		return ui.GizmoTypes.Duplicate.AsControl()
	case GizmoTrash:
		return ui.GizmoTypes.Delete.AsControl()
	}
	return Control.Nil
}

// borrowedGizmoButton returns a safe borrowed reference to a dynamic
// gizmo button we previously created (we give them stable names
// "Gizmo_<name>"). It is obtained via GetNode so it is always a
// LetObject borrow (never a transferable ownership reference).
func (ui *CloudControl) borrowedGizmoButton(name string) Control.Instance {
	if ui.GizmoTypes.AsNode() == Node.Nil {
		return Control.Nil
	}
	n := ui.GizmoTypes.AsNode().GetNode("Gizmo_" + name)
	if n == Node.Nil {
		return Control.Nil
	}
	// Most dynamic buttons are TextureButtons; fall back to Control.
	if b, ok := Object.As[TextureButton.Instance](n); ok && b != TextureButton.Nil {
		return b.AsControl()
	}
	if c, ok := Object.As[Control.Instance](n); ok {
		return c
	}
	return Control.Nil
}

// rebuildGizmoToolbar arranges the GizmoTypes VBox exactly as the
// supplied slice requests. Dynamic buttons and separators are always
// created fresh (via .New()) and AddChild'ed exactly once. The two
// Godot-owned action buttons (Duplicate/Delete) are never passed to
// AddChild; they are reordered in place with MoveChild.
func (ui *CloudControl) rebuildGizmoToolbar(gizmos []Gizmo) {
	vbox := ui.GizmoTypes.AsNode()
	if vbox == Node.Nil {
		return
	}

	// Remove only children we created (the old baked anonymous buttons
	// from the .tscn and any previous dynamic ones). Leave the two
	// named action buttons alone — we will MoveChild them into the
	// positions the editor requested.
	//
	// Identity must be tested by instance ID, not by Go's == on the
	// Instance structs: two references to the same engine node (one
	// from GetChild, one from the stored Duplicate/Delete field) carry
	// different borrow sentinel/revision bookkeeping and never compare
	// equal. Using == here freed the action buttons (RemoveChild +
	// QueueFree), so the later MoveChild logged "Child is not a child
	// of this node" and UI.Process then SIGSEGV'd touching the dangling
	// Delete reference.
	dupID := ui.GizmoTypes.Duplicate.AsNode().ID()
	delID := ui.GizmoTypes.Delete.AsNode().ID()
	for i := vbox.GetChildCount() - 1; i >= 0; i-- {
		child := vbox.GetChild(i)
		if id := child.ID(); id == dupID || id == delID {
			continue
		}
		vbox.RemoveChild(child)
		// The nodes we created are no longer referenced by any Go
		// cache, so QueueFree is the right thing to do.
		child.QueueFree()
	}

	// Track where we want the two action buttons to end up (by their
	// final child index after we have inserted the dynamics). We do a
	// two-pass approach: first insert all the requested dynamics (and
	// separators), then MoveChild the actions into the correct slots.
	type actionPlacement struct {
		g     Gizmo
		index int // desired index among the final children
	}
	var placements []actionPlacement

	ui.gizmoButtons = make(map[Gizmo]Control.Instance, len(gizmos))

	insertIndex := 0 // current insertion point as we walk the request list
	for _, g := range gizmos {
		switch g {
		case GizmoSpace:
			sep := ui.newGizmoSeparator()
			vbox.AddChild(sep.AsNode())
			// Move it to the logical position if earlier insertions
			// (e.g. actions we will place later) require it. For a
			// pure append-and-reorder strategy we can just let them
			// append and fix with MoveChild in a final pass, but
			// doing it incrementally is also fine.
			vbox.MoveChild(sep.AsNode(), insertIndex)
			insertIndex++

		case GizmoClone:
			placements = append(placements, actionPlacement{GizmoClone, insertIndex})
			insertIndex++ // reserve the slot

		case GizmoTrash:
			placements = append(placements, actionPlacement{GizmoTrash, insertIndex})
			insertIndex++

		default:
			btn := ui.newGizmoButton(g)
			if btn == TextureButton.Nil {
				continue
			}
			vbox.AddChild(btn.AsNode())
			vbox.MoveChild(btn.AsNode(), insertIndex)
			// Store a *borrowed* reference obtained via the stable
			// name we gave the button. This is always safe.
			if c := ui.borrowedGizmoButton(gizmoIconName(g)); c != Control.Nil {
				ui.gizmoButtons[g] = c
			} else {
				// Fallback: the just-inserted node as a borrow.
				ui.gizmoButtons[g] = btn.AsControl()
			}
			insertIndex++
		}
	}

	// Now place the action buttons (which have remained children the
	// whole time) at the exact indices the editor asked for.
	for _, p := range placements {
		var action Node.Instance
		var setVisible func(bool)
		if p.g == GizmoClone {
			action = ui.GizmoTypes.Duplicate.AsNode()
			setVisible = func(v bool) { ui.GizmoTypes.Duplicate.AsCanvasItem().SetVisible(v) }
		} else {
			action = ui.GizmoTypes.Delete.AsNode()
			setVisible = func(v bool) { ui.GizmoTypes.Delete.AsCanvasItem().SetVisible(v) }
		}
		if action != Node.Nil {
			vbox.MoveChild(action, p.index)
			setVisible(true)
		}
	}

	// As a final safety step, make sure any action button that was
	// *not* mentioned in the slice is hidden (the Process() override
	// will still run every frame for the selection-dependent case).
	// Also populate the (borrowed) map entries for the actions so that
	// gizmoControl has a uniform source of truth.
	if ui.isGizmoAllowed(GizmoClone) {
		ui.gizmoButtons[GizmoClone] = ui.GizmoTypes.Duplicate.AsControl()
	} else {
		ui.GizmoTypes.Duplicate.AsCanvasItem().SetVisible(false)
	}
	if ui.isGizmoAllowed(GizmoTrash) {
		ui.gizmoButtons[GizmoTrash] = ui.GizmoTypes.Delete.AsControl()
	} else {
		ui.GizmoTypes.Delete.AsCanvasItem().SetVisible(false)
	}
}

// SetGizmos restricts the visible gizmo tools in the toolbar to exactly
// the supplied list, in the order given. Editors should call this (via
// client.SetGizmos) from their EnableEditor so that only the tools
// relevant to that editor are offered, and in the order the editor
// prefers them to appear.
//
// GizmoSpace inserts a visual separator at that point in the column.
// GizmoClone and GizmoTrash insert the Duplicate and Delete action
// buttons respectively.
//
// Each gizmo button (except the two actions, which reuse the persistent
// scene nodes) loads its icon from res://ui/gizmo/<name>.svg using the
// mapping in gizmoIconName (GizmoPoint → "point.svg", etc.).
func (ui *CloudControl) SetGizmos(gizmos []Gizmo) {
	allowed := make(map[Gizmo]bool, len(gizmos))
	for _, g := range gizmos {
		allowed[g] = true
	}
	ui.allowedGizmos = allowed

	ui.rebuildGizmoToolbar(gizmos)

	// If the currently active gizmo is no longer allowed by the new
	// set, pick the first non-action gizmo from the supplied order
	// (respecting the editor's preferred default by list position).
	if !ui.isGizmoAllowed(ui.Gizmo) {
		picked := false
		for _, g := range gizmos {
			if g != GizmoSpace && g != GizmoClone && g != GizmoTrash && ui.isGizmoAllowed(g) {
				ui.set_gizmo(g)
				picked = true
				break
			}
		}
		if !picked {
			// No selectable gizmo remains; hide the active-gizmo border.
			ui.GizmoIndicator.AsCanvasItem().SetVisible(false)
		}
	}
}
