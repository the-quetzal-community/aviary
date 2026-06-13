package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
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

	ui.buildLicenseToggles()
}

// buildLicenseToggles appends a row of the three Creative Commons license
// badges (CC0, CC-BY, CC-BY-SA) below the quality slider. Each badge is a
// toggle: switching one off hides every library author publishing under
// that license from the design explorer (see authorLicense), dimming the
// badge to show the off state. The choice persists in
// UserState.HiddenLicenses. The badges are the official Creative Commons
// button SVGs (res://ui/license_*.svg).
func (ui *UI) buildLicenseToggles() {
	types := ui.SettingsMenu.AsNode().GetNode("SettingsTypes")
	if types == Node.Nil {
		return
	}
	row := HBoxContainer.New()
	row.AsNode().SetName("LicenseRow")
	types.AddChild(row.AsNode())
	tooltips := map[ccLicense]string{
		ccZero: "Show/hide public-domain (CC0) artwork",
		ccBY:   "Show/hide attribution (CC-BY) artwork",
		ccBYSA: "Show/hide share-alike (CC-BY-SA) artwork",
	}
	for _, license := range ccLicenses {
		button := TextureButton.New().
			SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/license_" + string(license) + ".svg")).
			SetIgnoreTextureSize(true).
			SetStretchMode(TextureButton.StretchKeepAspectCentered)
		button.AsControl().
			SetCustomMinimumSize(Vector2.New(160, 64)).
			SetSizeFlagsHorizontal(Control.SizeExpandFill).
			SetTooltipText(tooltips[license])
		base := button.AsBaseButton()
		base.SetToggleMode(true)
		base.SetPressedNoSignal(!licenseHidden(license))
		dim := func(shown bool) {
			alpha := Float.X(1.0)
			if !shown {
				alpha = 0.3
			}
			button.AsCanvasItem().SetModulate(Color.RGBA{R: 1, G: 1, B: 1, A: alpha})
		}
		dim(!licenseHidden(license))
		base.OnToggled(func(shown bool) {
			setLicenseHidden(license, !shown)
			dim(shown)
			if ui.client != nil {
				ui.client.saveUserState()
				// Re-resolve the design explorer with the new filter; the
				// empty author lets it fall back to the highest-ranked
				// still-visible preference.
				ui.Editor.Refresh(ui.client.Editing, "", ui.mode)
				// Show/hide everything already placed in the scene that
				// comes from authors under this license.
				ui.client.applyLicenseVisibility()
			}
		})
		row.AsNode().AddChild(button.AsNode())
	}
}

// toggleSettings rolls the Settings menu in and out from behind the
// Toolbar triangle, sharing the Rollout helper with the editor switcher.
func (ui *UI) toggleSettings() {
	ui.settingsRollout.Toggle(ui.SettingsMenu.AsControl())
}
