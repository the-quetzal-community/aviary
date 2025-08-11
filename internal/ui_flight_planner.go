package internal

import (
	"strings"

	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/variant/Object"
	"the.quetzal.community/aviary/internal/networking"
)

type FlightPlanner struct {
	Panel.Extension[FlightPlanner] `gd:"FlightPlanner"`

	Back TextureButton.Instance `gd:"%Back"`
	Keys GridContainer.Instance `gd:"%Keys"`
	Code TextEdit.Instance      `gd:"%Code"`
	Plus Button.Instance        `gd:"%Plus"`

	client *Client
}

func (fl *FlightPlanner) Ready() {
	fl.Code.SetText("")
	fl.Back.AsBaseButton().OnPressed(func() {
		fl.AsCanvasItem().SetVisible(false)
	})
	fl.Plus.AsBaseButton().OnPressed(func() {
		fresh := NewClient()
		for _, child := range SceneTree.Get(fl.AsNode()).Root().AsNode().GetChildren() {
			child.QueueFree()
		}
		SceneTree.Add(fresh)
	})
	fl.Code.OnTextChanged(func() {
		text := fl.Code.Text()
		safe := ""
		for _, char := range text {
			if strings.ContainsRune("0123456789", char) {
				safe += string(char)
			}
		}
		if len(safe) > 6 {
			safe = safe[:6]
		}
		if text != safe {
			fl.Code.SetText(safe)
		}
	})
	keys := fl.Keys.AsNode().GetChildren()
	for _, key := range keys {
		name := key.Name()
		switch name {
		case "X":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := fl.Code.Text()
				if len(text) > 0 {
					text = text[:len(text)-1]
				}
				fl.Code.SetText(text)
			})
		case ">":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				fresh := NewClient()
				for _, child := range SceneTree.Get(fl.AsNode()).Root().AsNode().GetChildren() {
					child.QueueFree()
				}
				SceneTree.Add(fresh)

				go fresh.apiJoin(networking.Code(fl.Code.Text()))
			})
		default:
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := fl.Code.Text()
				text += name
				fl.Code.SetText(text)
			})
		}
	}
}
