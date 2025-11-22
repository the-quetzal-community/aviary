package internal

import (
	"strings"
	"sync/atomic"

	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/Tween"
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
	TabContainer.Extension[DesignExplorer]

	// This represents the area to hover over in order to expand the design
	// drawer.
	ExpansionIndicator Button.ID

	client *Client
	editor Editor
	tabbed []*GridFlowContainer // current tabbed containers
	cached map[Node3D.ID]map[string][]*GridFlowContainer
	slider map[string]map[string]HSlider.ID

	author string

	// state that enables the design drawer to open and close.
	drawExpanded  atomic.Bool
	drawExpansion Float.X
	locked        bool
	queued        func()

	last_slider_state sliderState
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
	de.AsTabContainer().GetTabBar().AsControl().
		SetMouseFilter(Control.MouseFilterStop)
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
	preview_path := "res://preview/" + author
	library_path := "res://library/" + author
	for _, node := range ui.AsNode().GetChildren() {
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
	themes := DirAccess.Open(preview_path)
	if themes == DirAccess.Nil {
		return
	}
	ui.tabbed = nil
	const (
		glb = ".glb"
		png = ".png"
	)
	edits := false
	index := 0
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
			ui.AsNode().AddChild(Node.Instance(slider.AsNode()))
			if FileAccess.FileExists("res://ui/" + strings.ToLower(editor.String()) + "/" + tab + ".svg.import") {
				ui.AsTabContainer().SetTabIcon(index, Resource.Load[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+"/"+tab+".svg"))
			} else {
				ui.AsTabContainer().SetTabIcon(index, Resource.Load[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+".svg"))
			}
			ui.AsTabContainer().SetTabTitle(index, "")
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
			ui.AsNode().AddChild(gridflow.AsNode())
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
				if !String.HasSuffix(resource, ".png") {
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
					ImageButton := TextureButton.New()
					ImageButton.SetTextureNormal(preview)
					ImageButton.SetIgnoreTextureSize(true)
					ImageButton.SetStretchMode(TextureButton.StretchKeepAspectCentered)
					ImageButton.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
					ImageButton.AsControl().SetMouseFilter(Control.MouseFilterStop)
					ImageButton.AsBaseButton().OnPressed(func() {
						ui.editor.SelectDesign(mode, resource)
						ui.closeDrawer()
					})
					elements.AsNode().AddChild(ImageButton.AsNode())
				case png:
					texture := Resource.Load[Texture2D.Instance](path)
					resource := library_path + "/" + tab + "/" + resource
					ImageButton := TextureButton.New()
					ImageButton.SetTextureNormal(texture)
					ImageButton.SetIgnoreTextureSize(true)
					ImageButton.SetStretchMode(TextureButton.StretchKeepAspectCentered)
					ImageButton.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
					ImageButton.AsControl().SetMouseFilter(Control.MouseFilterStop)
					ImageButton.AsBaseButton().OnPressed(func() {
						ui.editor.SelectDesign(mode, resource)
						ui.closeDrawer()
					})
					elements.AsNode().AddChild(ImageButton.AsNode())
				}
			}
			texture := Resource.Load[Texture2D.Instance]("res://ui/" + tab + ".svg")
			gridflow.Update()
			ui.AsTabContainer().SetTabIcon(index, texture)
			ui.AsTabContainer().SetTabTitle(index, "")
			index++
		}
	}
	ui.AsCanvasItem().SetVisible(index > 0)
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
