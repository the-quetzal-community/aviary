package internal

import (
	"crypto/rand"
	"encoding/base64"
	"strings"

	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
	"the.quetzal.community/aviary/internal/musical"
	"the.quetzal.community/aviary/internal/networking"
)

type FlightPlanner struct {
	Panel.Extension[FlightPlanner] `gd:"FlightPlanner"`

	Back TextureButton.Instance `gd:"%Back"`
	Keys GridContainer.Instance `gd:"%Keys"`
	Code TextEdit.Instance      `gd:"%Code"`
	Plus Button.Instance        `gd:"%Plus"`

	Maps GridContainer.Instance `gd:"%Maps"`

	client *Client
}

func (fl *FlightPlanner) Reload() {
	for i, child := range fl.Maps.AsNode().GetChildren() {
		if i > 0 {
			child.QueueFree()
		}
	}
	fl.Maps.SetColumns(int(fl.AsControl().Size().X/256) - 1)
	for save := range DirAccess.Open("user://").Iter() {
		if strings.HasSuffix(save, ".png") {
			mapButton := TextureButton.New()
			mapButton.AsTextureButton().SetTextureNormal(ImageTexture.CreateFromImage(Image.LoadFromFile("user://" + save)).AsTexture2D())
			mapButton.AsBaseButton().OnPressed(func() {
				record, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(save, ".png"))
				if err != nil {
					Engine.Raise(err)
					return
				}
				fresh := NewClientLoading(musical.Unique(record))
				for _, child := range SceneTree.Get(fl.AsNode()).Root().AsNode().GetChildren() {
					child.QueueFree()
				}
				SceneTree.Add(fresh)
			})
			mapButton.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
			mapButton.SetIgnoreTextureSize(true)
			mapButton.SetStretchMode(TextureButton.StretchKeepAspect)
			fl.Maps.AsNode().AddChild(mapButton.AsNode())
		}
	}
}

func (fl *FlightPlanner) Ready() {
	fl.Reload()
	fl.Code.SetText("")
	fl.Back.AsBaseButton().OnPressed(func() {
		fl.AsCanvasItem().SetVisible(false)
	})
	fl.Plus.AsBaseButton().OnPressed(func() {
		var record musical.Unique
		if _, err := rand.Read(record[:]); err != nil {
			Engine.Raise(err)
			return
		}
		fresh := NewClientLoading(record)
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
				fresh := NewClientJoining()
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
