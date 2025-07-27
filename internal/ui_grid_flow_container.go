package internal

import (
	"graphics.gd/classdb/Container"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/ScrollContainer"
	"graphics.gd/variant/Object"
)

type GridFlowContainer struct {
	Container.Extension[GridFlowContainer] `gd:"GridFlowContainer"`

	Scrollable struct {
		ScrollContainer.Instance

		GridContainer GridContainer.Instance
	}
}

func (grid *GridFlowContainer) Ready() {
	grid.Scrollable.AsControl().SetAnchorsPreset(Control.PresetFullRect)
	grid.Scrollable.SetHorizontalScrollMode(ScrollContainer.ScrollModeDisabled)
	grid.Scrollable.SetVerticalScrollMode(ScrollContainer.ScrollModeDisabled)
	grid.AsControl().SetClipContents(true)
}

func (grid *GridFlowContainer) Update() {
	new_columns := int(Object.To[Control.Instance](grid.AsNode().GetParent()).Size().X / 256)
	new_columns = max(1, new_columns)
	grid.Scrollable.GridContainer.SetColumns(new_columns)
	grid.Scrollable.SetHorizontalScrollMode(ScrollContainer.ScrollModeDisabled)
	if DrawExpanded.Load() {
		grid.Scrollable.SetVerticalScrollMode(ScrollContainer.ScrollModeAuto)
	} else {
		grid.Scrollable.SetVerticalScrollMode(ScrollContainer.ScrollModeDisabled)
	}
}

func (grid *GridFlowContainer) Notification(what int, reversed bool) {
	if what == 40 {
		grid.Update()
	}
}
