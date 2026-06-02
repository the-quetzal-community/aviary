package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"

	"the.quetzal.community/aviary/internal/musical"
)

// environmentTopSpacer is the height (in EnvironmentMenu-local units) of the
// blank Control inserted before the first slider, reserving room for the
// EditorIndicator's white triangle the panel rolls out from. Tweak this to
// move the slider stack up or down.
const environmentTopSpacer Float.X = 372

// buildEnvironmentMenu creates the slider content for the lighting rolldown
// (Time of Day, Sun Angle, Fog, Clouds). The outer Panel lives in editor.tscn
// (EnvironmentMenu) so it participates in the same Rollout + scaling machinery
// as SettingsMenu. Content is built in code to keep the .tscn diff tiny and
// the layout easy to adjust.
func (ui *UI) buildEnvironmentMenu() {
	if ui.EnvironmentMenu.AsControl() == Control.Nil {
		return
	}
	ui.environmentSliders = make(map[string]HSlider.Instance)
	// Ensure we have a container to host the rows.
	containerNode := ui.EnvironmentMenu.AsNode().GetNode("Types")
	var container VBoxContainer.Instance
	if c, ok := Object.As[VBoxContainer.Instance](containerNode); ok {
		container = c
	} else {
		container = VBoxContainer.New()
		container.AsControl().SetAnchorsPreset(Control.PresetFullRect)
		ui.EnvironmentMenu.AsNode().AddChild(container.AsNode())
	}

	// Push the slider rows below the EditorIndicator's white triangle (the
	// toolbar this panel rolls out from) with a leading spacer, so they only
	// appear past its height. This is the single source of truth for that
	// gap — adjust environmentTopSpacer to move the first slider up/down.
	spacer := Control.New()
	spacer.SetCustomMinimumSize(Vector2.New(0, environmentTopSpacer))
	spacer.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	container.AsNode().AddChild(spacer.AsNode())

	// Downscale grabber exactly like the quality slider.
	const grabberSize = 64
	var smallGrabber Texture2D.Instance
	if tex := LoadSync[Texture2D.Instance]("res://ui/slider.png"); tex != Texture2D.Nil {
		if img := tex.GetImage(); img != Image.Nil {
			img.Resize(grabberSize, grabberSize)
			smallGrabber = ImageTexture.CreateFromImage(img).AsTexture2D()
		}
	}

	// Helper to make one labeled row. The caption is an icon loaded from
	// res://ui/<icon>.svg; if that texture isn't present yet (the icons are
	// still being sourced) it falls back to the text label so the row is
	// never blank.
	makeRow := func(sliderKey, icon, labelText string, minV, maxV, step, initV Float.X, onChange func(Float.X)) {
		row := HBoxContainer.New()
		row.AsControl().SetCustomMinimumSize(Vector2.New(0, 28))

		if tex := LoadSync[Texture2D.Instance]("res://ui/" + icon + ".svg"); tex != Texture2D.Nil {
			ico := TextureRect.New()
			ico.SetTexture(tex)
			ico.SetExpandMode(TextureRect.ExpandIgnoreSize)
			ico.SetStretchMode(TextureRect.StretchKeepAspectCentered)
			ico.AsControl().SetCustomMinimumSize(Vector2.New(72, 72))
			ico.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
			row.AsNode().AddChild(ico.AsNode())
		} else {
			lbl := Label.New()
			lbl.SetText(labelText)
			lbl.AsControl().SetCustomMinimumSize(Vector2.New(90, 0))
			// Small readable size for the compact rolldown.
			lbl.AsControl().AddThemeFontSizeOverride("font_size", 14)
			row.AsNode().AddChild(lbl.AsNode())
		}

		sld := HSlider.Advanced(HSlider.New())
		ui.environmentSliders[sliderKey] = HSlider.Instance(sld)
		// Let the slider grab all the horizontal space the fixed-width label
		// leaves, so it spans the full panel width regardless of how wide the
		// triangle (and thus the panel) ends up.
		sldControl := HSlider.Instance(sld).AsControl()
		sldControl.SetCustomMinimumSize(Vector2.New(0, 24))
		sldControl.SetSizeFlagsHorizontal(Control.SizeExpandFill)
		sld.AsRange().SetMin(float64(minV))
		sld.AsRange().SetMax(float64(maxV))
		sld.AsRange().SetStep(float64(step))
		sld.AsRange().SetValue(float64(initV))

		if smallGrabber != Texture2D.Nil {
			h := HSlider.Instance(sld)
			for _, nm := range []string{"grabber", "grabber_highlight", "grabber_disabled"} {
				h.AsControl().AddThemeIconOverride(nm, smallGrabber)
			}
		}

		Range.Instance(sld.AsRange()).OnValueChanged(onChange)

		row.AsNode().AddChild(HSlider.Instance(sld).AsNode())
		container.AsNode().AddChild(row.AsNode())
	}

	// Seed the friendly controls from the authoritative lighting state so the
	// menu opens on the real current values (each axis stays independent
	// because the state stores them directly, not a lossy re-derivation from
	// the light's rotation).
	tod := Float.X(0.38) // sensible daytime default before the world exists
	sunAng := Float.X(0.08)
	fg := Float.X(0.0)
	cl := Float.X(0.0)
	rn := Float.X(0.0)
	sn := Float.X(0.0)
	wn := Float.X(0.0)
	mn := Float.X(0.5) // half moon by default
	if ui.client != nil {
		tod, sunAng, fg, cl, rn, sn, wn, mn = ui.client.GetLightingMenuState()
	}

	// Friendly, non-technical controls. We drive them through
	// ApplyLightingMenuState so each axis stays completely independent.
	makeRow("environment/time_of_day", "daytime", "Time of Day", 0, 1, 0.005, tod, func(v Float.X) {
		if ui.client != nil {
			_, angle, fog, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(v, angle, fog, clouds, rain, snow, wind, moon)
		}
		// Still send the Sculpt for networking + persistence
		ui.sendEnvironmentSlider("environment/time_of_day", v)
	})
	makeRow("environment/sun_angle", "sunside", "Sun Angle", 0, 1, 0.005, sunAng, func(v Float.X) {
		if ui.client != nil {
			tod, _, fog, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, v, fog, clouds, rain, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/sun_angle", v)
	})
	makeRow("environment/fog", "fogmist", "Fog / Atmosphere", 0, 1, 0.01, fg, func(v Float.X) {
		if ui.client != nil {
			tod, angle, _, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, v, clouds, rain, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/fog", v)
	})
	makeRow("environment/clouds", "cumulus", "Clouds", 0, 1, 0.01, cl, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, _, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, v, rain, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/clouds", v)
	})
	makeRow("environment/rain", "raining", "Rain", 0, 1, 0.01, rn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, _, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, v, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/rain", v)
	})
	makeRow("environment/snow", "snowing", "Snow", 0, 1, 0.01, sn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, rain, _, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, rain, v, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/snow", v)
	})
	makeRow("environment/wind", "cyclone", "Wind", 0, 1, 0.01, wn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, rain, snow, _, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, v, moon)
		}
		ui.sendEnvironmentSlider("environment/wind", v)
	})
	makeRow("environment/moon", "moonlit", "Moon Phase", 0, 1, 0.01, mn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, rain, snow, wind, _ := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, wind, v)
		}
		ui.sendEnvironmentSlider("environment/moon", v)
	})
}

// sendEnvironmentSlider builds a Sculpt with the correct Editor routing key
// for the active editor (so it lands in the right editor's Sculpt handler)
// and also applies immediately for snappy local feedback.
func (ui *UI) sendEnvironmentSlider(slider string, value Float.X) {
	if ui.client == nil || ui.client.space == nil {
		// Fallback: still push to renderer so the menu works even before join.
		ui.client.applyLightingStateFromSlider(slider, value)
		return
	}
	key := ui.client.activeLightingEditorKey()
	ui.client.space.Sculpt(musical.Sculpt{
		Editor: key,
		Slider: slider,
		Amount: value,
		Commit: true,
	})
	// Immediate local apply (the round-trip will re-apply the same value).
	ui.client.applyLightingStateFromSlider(slider, value)
}

// toggleEnvironment rolls the lighting/fog/weather menu (Shading button).
func (ui *UI) toggleEnvironment() {
	// Re-sync the handles to the live lighting state before revealing them:
	// buildEnvironmentMenu seeds the sliders once during Setup, but the
	// persisted environment/* sculpts that carry the real values replay only
	// after the world loads, so the seed is stale by the time the user opens
	// the menu. By now the world is loaded, so GetLightingMenuState is right.
	ui.syncEnvironmentSliders()
	ui.environmentRollout.Toggle(ui.EnvironmentMenu.AsControl())
}

// syncEnvironmentSliders sets each lighting slider's handle to the current
// authoritative value from the Client's lightingMenuState. It uses
// SetValueNoSignal so refreshing the handle doesn't re-fire OnValueChanged
// (which would echo a redundant Sculpt back out). Safe to call repeatedly.
func (ui *UI) syncEnvironmentSliders() {
	if ui.client == nil || ui.environmentSliders == nil {
		return
	}
	tod, angle, fog, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
	for key, v := range map[string]Float.X{
		"environment/time_of_day": tod,
		"environment/sun_angle":   angle,
		"environment/fog":         fog,
		"environment/clouds":      clouds,
		"environment/rain":        rain,
		"environment/snow":        snow,
		"environment/wind":        wind,
		"environment/moon":        moon,
	} {
		if sld, ok := ui.environmentSliders[key]; ok {
			sld.AsRange().SetValueNoSignal(v)
		}
	}
}
