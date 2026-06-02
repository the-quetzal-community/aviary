package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
)

// buildSettingsMenu wires the graphics-quality slider that lives in the
// Settings rollout. The layout — a gray toaster icon, the HSlider, and a
// gray sports-car icon, pushed below the rotated Toolbar triangle by a
// spacer — is declared in editor.tscn under
// SettingsMenu/SettingsTypes/QualityRow. The icons are PNGs from The Noun
// Project, tinted gray to read on the light drawer (see graphics/License).
// Code only handles what the scene can't: shrinking the oversized themed
// grabber for this compact row, syncing the launch level into both the
// handle and the live renderer, and applying [GraphicsQuality] on each move.
func (ui *UI) buildSettingsMenu() {
	if ui.SettingsMenu.AsControl() == Control.Nil {
		return
	}
	sliderNode := ui.SettingsMenu.AsNode().GetNode("SettingsTypes/QualityRow/Quality")
	slider, ok := Object.As[HSlider.Instance](sliderNode)
	if !ok {
		return
	}

	// The themed grabber (res://ui/slider.png) is 128×128 — sized for the
	// full-size sliders elsewhere — and Godot draws the handle at the
	// texture's native size, so it dwarfs this compact row. Downscale a
	// copy to roughly the row height and override it on just this slider.
	const grabberSize = 64
	if tex := LoadSync[Texture2D.Instance]("res://ui/slider.png"); tex != Texture2D.Nil {
		if img := tex.GetImage(); img != Image.Nil {
			img.Resize(grabberSize, grabberSize)
			small := ImageTexture.CreateFromImage(img).AsTexture2D()
			for _, name := range []string{"grabber", "grabber_highlight", "grabber_disabled"} {
				slider.AsControl().AddThemeIconOverride(name, small)
			}
		}
	}

	// Pin the handle to the persisted (or default) launch level, then react to
	// every move. Set the value before connecting so this seed doesn't fire the
	// handler — the explicit Apply below covers the initial render.
	launchQ := UserState.GraphicsQuality
	slider.AsRange().SetValue(Float.X(launchQ))
	Range.Instance(slider.AsRange()).OnValueChanged(func(value Float.X) {
		q := GraphicsQuality(int(value))
		q.Apply(ui.AsNode())
		// SSAO/SDFGI on/off flags live on the world Environment, and the cloud
		// state spans the sky material + the volumetric-fog FogVolume — none of
		// which Apply's viewport/global state reaches, so apply them alongside.
		if ui.client != nil {
			q.ApplyEnvironmentQuality(ui.client.Environment)
			ui.client.applyCloudQuality(q)
			ui.client.applyWaterQuality(q)
			ui.client.applyShadowQuality(q)
			// Persist the choice so it survives across runs.
			UserState.GraphicsQuality = q
			UserState.GraphicsQualitySet = true
			ui.client.saveUserState()
		}
	})

	// Apply the launch quality (persisted or default) so the renderer matches
	// the slider's initial position before the user ever opens the menu.
	launchQ.Apply(ui.AsNode())
}

// toggleSettings rolls the Settings menu in and out from behind the
// Toolbar triangle, sharing the Rollout helper with the editor switcher.
func (ui *UI) toggleSettings() {
	ui.settingsRollout.Toggle(ui.SettingsMenu.AsControl())
}
