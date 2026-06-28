package internal

import (
	"math"

	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"

	"the.quetzal.community/aviary/internal/musical"
)

// presenceChip is one remote player's floating avatar thumbnail on the editor
// switcher. It is a top-level TextureRect positioned directly in global canvas
// space, so it can sit scattered around the collapsed EditorIcon and glide to
// the editor button the player is in when the switcher rolls out — independent
// of the sliding panel's own layout.
type presenceChip struct {
	rect    TextureRect.Instance
	preview string     // res:// path currently shown (avoids reloading every frame)
	pos     Vector2.XY // current global top-left, lerped toward target
	placed  bool       // pos seeded yet (first visible frame snaps instead of gliding from 0,0)
}

const (
	presenceChipSize  = 34 // px, square
	presenceChipSpeed = 14 // glide responsiveness toward the target (higher = snappier)
)

// makePresenceChip builds a hidden top-level chip parented to the indicator.
func (ed *EditorIndicator) makePresenceChip() *presenceChip {
	rect := TextureRect.New()
	rect.SetExpandMode(TextureRect.ExpandIgnoreSize)
	rect.SetStretchMode(TextureRect.StretchKeepAspectCentered)
	rect.AsControl().SetCustomMinimumSize(Vector2.New(presenceChipSize, presenceChipSize))
	rect.AsControl().SetSize(Vector2.New(presenceChipSize, presenceChipSize))
	rect.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	// Top-level + high z so the chip floats over the switcher button and the
	// rollout panel, positioned directly in global canvas coordinates.
	rect.AsCanvasItem().SetTopLevel(true)
	rect.AsCanvasItem().SetZIndex(200)
	rect.AsCanvasItem().SetVisible(false)
	ed.AsNode().AddChild(rect.AsNode())
	return &presenceChip{rect: rect}
}

// Process maintains the presence chips: it reconciles them with the current set
// of remote players, then places each one — scattered around the editor icon
// while the switcher is collapsed (only for peers in a *different* editor than
// us), or scattered around the matching editor button while it is rolled out.
func (ed *EditorIndicator) Process(delta Float.X) {
	if ed.client == nil || ed.chips == nil {
		return
	}
	peers := ed.client.peerPresenceSnapshot()

	// Reconcile: drop chips for players who have gone.
	present := make(map[musical.Author]struct{}, len(peers))
	for _, p := range peers {
		present[p.Author] = struct{}{}
	}
	for author, chip := range ed.chips {
		if _, ok := present[author]; !ok {
			chip.rect.AsNode().QueueFree()
			delete(ed.chips, author)
		}
	}

	open := ed.rollout.Open()
	local := ed.client.Editing
	// Per-target running counts give each chip a stable index within its group,
	// so the scatter spreads a group's chips out instead of piling them up.
	iconCount := 0
	buttonCount := make(map[int]int)

	for _, p := range peers {
		chip := ed.chips[p.Author]
		if chip == nil {
			chip = ed.makePresenceChip()
			ed.chips[p.Author] = chip
		}
		// Refresh the thumbnail only when the peer's avatar preview changes.
		if p.Preview != chip.preview {
			chip.preview = p.Preview
			setPresenceChipTexture(chip, p.Preview)
		}

		// Decide visibility + the icon this chip scatters around.
		var center Vector2.XY
		var radius Float.X
		visible := false
		switch {
		case !open:
			// Collapsed: only peers in a different editor than us, clustered
			// around the editor icon. Same-editor peers show no preview.
			if p.Known && p.Subject == local {
				break
			}
			center, radius = controlCenterRadius(ed.EditorIcon.AsControl(), 0)
			center = addScatter(center, p.Author, iconCount, radius)
			iconCount++
			visible = true
		case p.Known && p.Subject.Int() >= 0 && p.Subject.Int() < len(ed.editorButtons):
			// Rolled out: scatter around the button for the peer's editor.
			// Settle to the fully-open position so chips fly straight to where
			// the button lands while the panel is still sliding in.
			settle := -ed.EditorSelector.AsControl().Position().Y
			idx := p.Subject.Int()
			center, radius = controlCenterRadius(ed.editorButtons[idx], settle)
			center = addScatter(center, p.Author, buttonCount[idx], radius)
			buttonCount[idx]++
			visible = true
		}

		chip.rect.AsCanvasItem().SetVisible(visible)
		if !visible {
			continue
		}
		target := Vector2.Sub(center, Vector2.New(presenceChipSize/2, presenceChipSize/2))
		if !chip.placed {
			chip.pos = target
			chip.placed = true
		} else {
			w := Float.Min(1, delta*presenceChipSpeed)
			chip.pos = Vector2.Lerp(chip.pos, target, w)
		}
		chip.rect.AsControl().SetPosition(chip.pos)
	}
}

// setPresenceChipTexture streams the avatar preview in off the main thread (it
// comes from library.pck), falling back to the default avatar's preview when the
// peer's own thumbnail is missing. The chip shows blank until it arrives.
func setPresenceChipTexture(chip *presenceChip, preview string) {
	id := chip.rect.ID()
	LoadAsync(preview, func(tex Texture2D.Instance) {
		if tex == Texture2D.Nil {
			tex = LoadSync[Texture2D.Instance](avatarPreviewURI(defaultAvatarURI))
		}
		if r, ok := id.Instance(); ok {
			r.SetTexture(tex)
		}
	})
}

// controlCenterRadius returns the global-space center of a control (shifted by
// settleY on the Y axis, to project a still-sliding rollout button to its
// settled position) and a scatter radius derived from its size.
func controlCenterRadius(c Control.Instance, settleY Float.X) (Vector2.XY, Float.X) {
	rect := c.GetGlobalRect()
	center := Vector2.New(rect.Position.X+rect.Size.X/2, rect.Position.Y+rect.Size.Y/2+settleY)
	radius := Float.Max(rect.Size.X, rect.Size.Y)/2 + presenceChipSize*0.35
	return center, radius
}

// addScatter offsets a point to a stable, organic-looking position around it.
// The golden-angle term spreads a group's chips apart by index; the per-author
// phase and radial jitter scatter them so they don't read as a tidy ring.
func addScatter(center Vector2.XY, author musical.Author, index int, radius Float.X) Vector2.XY {
	h := uint32(author)*2654435761 + 0x9e3779b9
	angle := float64(index)*2.39996323 + float64(h%628)/100.0
	r := float64(radius) * (0.85 + float64((h>>10)%40)/100.0) // 0.85..1.25× radius
	return Vector2.New(
		center.X+Float.X(math.Cos(angle)*r),
		center.Y+Float.X(math.Sin(angle)*r),
	)
}
