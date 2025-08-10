package internal

import (
	"graphics.gd/classdb/Container"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/ScrollContainer"
)

type GridFlowContainer struct {
	Container.Extension[GridFlowContainer] `gd:"GridFlowContainer"`

	Scrollable struct {
		ScrollContainer.Instance

		GridContainer GridContainer.Instance
	}

	scroll_lock bool
}

func (grid *GridFlowContainer) Ready() {
	grid.Scrollable.AsControl().SetAnchorsPreset(Control.PresetFullRect)
	if grid.scroll_lock {
		grid.Scrollable.SetHorizontalScrollMode(ScrollContainer.ScrollModeDisabled)
		grid.Scrollable.SetVerticalScrollMode(ScrollContainer.ScrollModeDisabled)
	}
	grid.AsControl().SetClipContents(true)
}

func (grid *GridFlowContainer) Update() {
	if grid.AsControl() == Control.Nil || grid.Scrollable.Instance == ScrollContainer.Nil {
		return
	}
	new_columns := int(grid.AsControl().Size().X / 256)
	new_columns = max(1, new_columns)
	grid.Scrollable.GridContainer.SetColumns(new_columns)
	grid.Scrollable.SetHorizontalScrollMode(ScrollContainer.ScrollModeDisabled)
	if !grid.scroll_lock {
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
