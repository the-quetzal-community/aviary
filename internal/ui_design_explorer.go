package internal

import (
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/Tween"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/String"
	"graphics.gd/variant/Vector2"
	"the.quetzal.community/aviary/internal/musical"
)

// DesignExplorer is the large panel at the bottom of the screen in Aviary.
// It's used for the exploration and selection of designs from The Quetzal
// Community Library for use in the active [Editor].
type DesignExplorer struct {
	HBoxContainer.Extension[DesignExplorer]

	Panel struct {
		Panel.Instance

		Themes struct {
			VBoxContainer.Instance

			Heading struct {
				Panel.Instance

				Selected TextureButton.Instance
			}
		}
	}
	Tabs TabContainer.Instance

	// This represents the area to hover over in order to expand the design
	// drawer.
	ExpansionIndicator Button.ID

	client *Client
	editor Editor
	tabbed []*GridFlowContainer // current tabbed containers
	cached map[Node3D.ID]map[string][]*GridFlowContainer
	slider map[string]map[string]HSlider.ID

	author                      string
	themes                      map[string]TextureButton.ID
	themes_available_for_editor map[editorMode]map[string]struct{}

	// state that enables the design drawer to open and close.
	drawExpanded  atomic.Bool
	drawExpansion Float.X
	locked        bool
	queued        func()

	last_slider_state sliderState
}

type editorMode struct {
	Editor Subject
	Mode   Mode
}

type sliderState struct {
	pending bool

	mode Mode
	tab  string
	val  float64
}

// Ready implements [Node.Interface.Ready].
func (de *DesignExplorer) Ready() {
	de.slider = make(map[string]map[string]HSlider.ID)
	de.Tabs.GetTabBar().AsControl().
		SetMouseFilter(Control.MouseFilterStop)
	de.themes = make(map[string]TextureButton.ID)
	de.themes_available_for_editor = make(map[editorMode]map[string]struct{})
	Dir := DirAccess.Open("res://library")
	if Dir == (DirAccess.Instance{}) {
		return
	}
	for name := range Dir.Iter() {
		if strings.Contains(name, ".") {
			continue
		}
		if FileAccess.FileExists("res://library/" + name + "/icon.png.import") {
			button := TextureButton.New().
				SetTextureNormal(Resource.Load[Texture2D.Instance]("res://library/" + name + "/icon.png")).
				SetIgnoreTextureSize(true).
				SetStretchMode(TextureButton.StretchKeepAspectCentered)
			button.AsControl().
				SetSizeFlagsHorizontal(Control.SizeShrinkBegin).
				SetCustomMinimumSize(Vector2.New(72, 64))
			button.AsBaseButton().OnPressed(func() {
				for theme := range de.themes_available_for_editor[editorMode{
					Editor: de.client.Editing,
					Mode:   de.client.ui.mode,
				}] {
					other_button, _ := de.themes[theme].Instance()
					other_button.AsCanvasItem().SetVisible(true)
				}
				de.Refresh(de.client.Editing, name, de.client.ui.mode)
				de.Panel.Themes.Heading.Selected.SetTextureNormal(Resource.Load[Texture2D.Instance]("res://library/" + name + "/icon.png"))
				button, _ := de.themes[name].Instance()
				button.AsCanvasItem().SetVisible(false)
			})
			de.themes[name] = button.ID()
			de.Panel.Themes.AsNode().AddChild(button.AsNode())
		}
	}
}

func (ui *DesignExplorer) Sculpt(brush musical.Sculpt) {
	if brush.Slider == "" {
		return
	}
	cache, ok := ui.slider[brush.Editor]
	if !ok {
		return
	}
	slider_id, ok := cache[brush.Slider]
	if !ok {
		return
	}
	slider, ok := slider_id.Instance()
	if !ok {
		return
	}
	slider.AsRange().SetValueNoSignal(Float.X(brush.Amount))
}

func (ui *DesignExplorer) Process(delta Float.X) {
	if ui.last_slider_state.pending && !Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
		ui.last_slider_state.pending = false
		ui.editor.SliderHandle(
			ui.last_slider_state.mode,
			ui.last_slider_state.tab,
			ui.last_slider_state.val,
			true,
		)
	}
}

// Refresh repopulates the tabbed designs depending on the active editor,
// these designs may be cached so that subsequent refreshes are faster.
func (ui *DesignExplorer) Refresh(editor Subject, author string, mode Mode) {
	expansion, _ := ui.ExpansionIndicator.Instance()
	for _, node := range ui.Tabs.AsNode().GetChildren() {
		container, ok := Object.As[*GridFlowContainer](node)
		if ok {
			container.AsObject()[0].Free()
		} else {
			slider, ok := Object.As[HSlider.Instance](node)
			if ok {
				slider.AsObject()[0].Free()
			}
		}
	}
	if ui.AsNode().GetChildCount() == 0 {
		ui.AsCanvasItem().SetVisible(false)
		expansion.AsCanvasItem().SetVisible(false)
	}
	const (
		glb = ".glb"
		png = ".png"
	)
	edits := false
	index := 0
	for _, button := range ui.themes {
		button, _ := button.Instance()
		button.AsCanvasItem().SetVisible(false)
	}
	themes_available, ok := ui.themes_available_for_editor[editorMode{
		Editor: editor,
		Mode:   mode,
	}]
	if !ok {
		themes_available = make(map[string]struct{})
		for author := range ui.themes {
			for _, tab := range ui.editor.Tabs(mode) {
				var path = "res://preview/" + author + "/" + tab
				resources := DirAccess.Open(path)
				if resources != DirAccess.Nil {
					themes_available[author] = struct{}{}
					break
				}
			}
		}
		ui.themes_available_for_editor[editorMode{
			Editor: editor,
			Mode:   mode,
		}] = themes_available
	}
	for _, theme := range slices.Sorted(maps.Keys(themes_available)) {
		if author == "" {
			author = theme
			ui.Panel.Themes.Heading.Selected.SetTextureNormal(Resource.Load[Texture2D.Instance]("res://library/" + author + "/icon.png"))
		} else {
			button, _ := ui.themes[theme].Instance()
			button.AsCanvasItem().SetVisible(true)
		}
	}
	preview_path := "res://preview/" + author
	library_path := "res://library/" + author
	themes := DirAccess.Open(preview_path)
	if themes == DirAccess.Nil {
		return
	}
	ui.tabbed = nil
	for _, tab := range ui.editor.Tabs(mode) {
		if strings.HasPrefix(tab, "editing/") {
			slider := HSlider.Advanced(HSlider.New())
			slider_id := HSlider.Instance(slider).ID()
			init, from, upto, step := ui.editor.SliderConfig(mode, tab)
			slider.AsRange().SetMin(from)
			slider.AsRange().SetMax(upto)
			slider.AsRange().SetValue(init)
			slider.AsRange().SetStep(step)
			Range.Instance(slider.AsRange()).OnValueChanged(func(value Float.X) {
				slider, _ := slider_id.Instance()
				ui.last_slider_state = sliderState{
					pending: true,
					mode:    mode,
					tab:     tab,
					val:     HSlider.Advanced(slider).AsRange().GetValue(),
				}
				ui.editor.SliderHandle(mode, tab, HSlider.Advanced(slider).AsRange().GetValue(), false)
			})
			if _, ok := ui.slider[ui.editor.Name()]; !ok {
				ui.slider[ui.editor.Name()] = make(map[string]HSlider.ID)
			}
			ui.slider[ui.editor.Name()][tab] = slider_id
			ui.Tabs.AsNode().AddChild(Node.Instance(slider.AsNode()))
			if FileAccess.FileExists("res://ui/" + strings.ToLower(editor.String()) + "/" + tab + ".svg.import") {
				ui.Tabs.SetTabIcon(index, Resource.Load[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+"/"+tab+".svg"))
			} else {
				ui.Tabs.SetTabIcon(index, Resource.Load[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+".svg"))
			}
			ui.Tabs.SetTabTitle(index, "")
			edits = true
			index++
		} else {
			var path = "res://preview/" + author + "/"
			path += tab
			resources := DirAccess.Open(path)
			if resources == DirAccess.Nil {
				continue
			}
			gridflow := new(GridFlowContainer)
			gridflow.AsControl().SetMouseFilter(Control.MouseFilterStop)
			gridflow.scroll_lock = true
			gridflow.AsNode().SetName(tab)
			ui.Tabs.AsNode().AddChild(gridflow.AsNode())
			gridflow.Scrollable.GetHScrollBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
			gridflow.Scrollable.GetVScrollBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
			ui.tabbed = append(ui.tabbed, gridflow)
			elements := gridflow.Scrollable.GridContainer
			var ext = glb
			if mode == ModeMaterial {
				ext = png
			}
			for resource := range resources.Iter() {
				resource = strings.TrimSuffix(resource, ".import")
				if !String.HasSuffix(resource, ".png") || String.HasSuffix(resource, "_cut.glb.png") {
					continue
				}
				var path = preview_path + "/" + tab + "/" + resource
				switch ext {
				case glb:
					preview := Resource.Load[Texture2D.Instance](path)
					if preview == Texture2D.Nil {
						continue
					}
					resource := library_path + "/" + tab + "/" + strings.TrimSuffix(string(resource), ".png")
					if tscn := library_path + "/" + tab + "/" + String.TrimSuffix(resource, ".png") + ".tscn"; FileAccess.FileExists(tscn) {
						resource = tscn
					}
					elements.AsNode().AddChild(TextureButton.New().
						SetTextureNormal(preview).
						SetIgnoreTextureSize(true).
						SetStretchMode(TextureButton.StretchKeepAspectCentered).
						AsBaseButton().OnPressed(
						func() {
							ui.editor.SelectDesign(mode, resource)
							ui.closeDrawer()
						}).
						AsControl().SetCustomMinimumSize(Vector2.New(256, 256)).
						AsControl().SetMouseFilter(Control.MouseFilterStop).AsNode(),
					)
				case png:
					texture := Resource.Load[Texture2D.Instance](path)
					resource := library_path + "/" + tab + "/" + resource
					elements.AsNode().AddChild(TextureButton.New().
						SetTextureNormal(texture).
						SetIgnoreTextureSize(true).
						SetStretchMode(TextureButton.StretchKeepAspectCentered).
						AsBaseButton().OnPressed(
						func() {
							ui.editor.SelectDesign(mode, resource)
							ui.closeDrawer()
						}).
						AsControl().SetCustomMinimumSize(Vector2.New(256, 256)).
						AsControl().SetMouseFilter(Control.MouseFilterStop).AsNode(),
					)
				}
			}
			gridflow.Update()
			if FileAccess.FileExists("res://ui/" + tab + ".svg.import") {
				ui.Tabs.SetTabIcon(index, Resource.Load[Texture2D.Instance]("res://ui/"+tab+".svg"))
			} else {
				ui.Tabs.SetTabIcon(index, Resource.Load[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+".svg"))
			}
			ui.Tabs.SetTabTitle(index, "")
			index++
		}
	}
	if len(themes_available) == 0 {
		ui.Panel.Themes.Heading.Selected.SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/editing.svg"))
	}
	ui.AsCanvasItem().SetVisible(index > 0 || len(themes_available) > 0)
	expansion.AsCanvasItem().SetVisible(index > 0 && !edits)
}

func (ui *DesignExplorer) UnhandledInput(event InputEvent.Instance) {
	if ui.drawExpanded.Load() && Object.Is[InputEventMouseMotion.Instance](event) {
		height := DisplayServer.WindowGetSize(0).Y
		if ui.AsCanvasItem().GetGlobalMousePosition().Y < Float.X(height)*0.3 {
			ui.closeDrawer()
		}
	}
}

func (ui *DesignExplorer) openDrawer() {
	if ui.locked {
		ui.queued = ui.openDrawer
		return
	}
	if !ui.drawExpanded.CompareAndSwap(false, true) {
		return
	}
	ui.locked = true
	for _, container := range ui.tabbed {
		container.scroll_lock = false
	}
	ui.client.scroll_lock = true
	window_size := DisplayServer.WindowGetSize(0)
	scale_factor := ui.AsControl().Scale().Y
	current_eff_height := ui.AsControl().Size().Y * scale_factor
	var amount Float.X = -(Float.X(window_size.Y) - current_eff_height) * 0.8
	move := Vector2.New(ui.AsControl().Position().X, ui.AsControl().Position().Y+amount)
	grow := Vector2.New(ui.AsControl().Size().X, ui.AsControl().Size().Y-(amount/scale_factor))
	tween := SceneTree.Get(ui.AsNode()).CreateTween()
	PropertyTweener.Make(tween, ui.AsControl().AsObject(), "size", grow, 0.1).SetEase(Tween.EaseOut)
	PropertyTweener.Make(SceneTree.Get(ui.AsNode()).CreateTween(), ui.AsControl().AsObject(), "position", move, 0.1).SetEase(Tween.EaseOut)
	tween.OnFinished(func() {
		ui.locked = false
		if ui.queued != nil {
			queued := ui.queued
			ui.queued = nil
			queued()
		}
	})
	expansion, _ := ui.ExpansionIndicator.Instance()
	expansion.AsCanvasItem().SetVisible(false)
	// Remove ui.drawExpansion = amount (no longer needed)
}

func (ui *DesignExplorer) closeDrawer() {
	if ui.locked {
		ui.queued = ui.closeDrawer
		return
	}
	if !ui.drawExpanded.CompareAndSwap(true, false) {
		return
	}
	ui.locked = true
	for _, container := range ui.tabbed {
		container.scroll_lock = true
	}
	ui.client.scroll_lock = false
	window_size := DisplayServer.WindowGetSize(0)
	scale_factor := ui.AsControl().Scale().Y
	const base_logical_height = 360.0 // Your base collapsed logical height (adjust to 370.0 if that's intended)
	grow := Vector2.New(ui.AsControl().Size().X, base_logical_height)
	move := Vector2.New(ui.AsControl().Position().X, Float.X(window_size.Y)-(base_logical_height*scale_factor))
	tween := SceneTree.Get(ui.AsNode()).CreateTween()
	PropertyTweener.Make(tween, ui.AsControl().AsObject(), "size", grow, 0.1).SetEase(Tween.EaseOut)
	PropertyTweener.Make(SceneTree.Get(ui.AsNode()).CreateTween(), ui.AsControl().AsObject(), "position", move, 0.1).SetEase(Tween.EaseOut)
	tween.OnFinished(func() {
		ui.locked = false
		if ui.queued != nil {
			queued := ui.queued
			ui.queued = nil
			queued()
		}
	})
	expansion, _ := ui.ExpansionIndicator.Instance()
	expansion.AsCanvasItem().SetVisible(true)
}
