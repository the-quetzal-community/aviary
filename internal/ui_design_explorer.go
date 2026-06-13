package internal

import (
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
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
	slider map[string]map[string]HSlider.ID

	author                      string
	themes                      map[string]TextureButton.ID
	themes_available_for_editor map[editorMode]map[string]struct{}

	// state that enables the design drawer to open and close.
	drawExpanded atomic.Bool
	locked       bool
	queued       func()

	last_slider_state sliderState

	// drag-and-drop state for placing designs into the scene by
	// dragging from the explorer. drag_active is armed on a tile's
	// button_down; drag_started flips true once the cursor moves past
	// dragThreshold from the press anchor — at that point the preview
	// is attached and the drawer closes (drag intent confirmed). If
	// the user releases without crossing the threshold, the tile's
	// legacy OnPressed handler attaches the preview the way it always
	// did, preserving the old click-then-click placement flow.
	// drag_mode / drag_resource / drag_thumb capture the pending tile
	// so the threshold-cross path can call SelectDesign without
	// re-plumbing the loop closures.
	drag_active   bool
	drag_started  bool
	drag_anchor   Vector2.XY
	drag_mode     Mode
	drag_resource string
	drag_thumb    Texture2D.Instance
	drag_ghost    TextureRect.Instance

	// placement_recency orders design resource paths most-recently-
	// placed first. BumpDesign pushes a resource to the front each time
	// an entity using it is created in the scene (local, remote, or
	// replayed — they all flow through the same Change handler), and
	// Refresh/applyRecency consult it so the designs you most recently
	// built float to the front of every tab. Persisted across Refreshes.
	placement_recency []string
	// tile_for_resource maps a design resource path to the live tile
	// shown for it in the current Refresh, letting BumpDesign reorder
	// the grid in place without a full rebuild. Rebuilt every Refresh.
	tile_for_resource map[string]TextureButton.ID
}

const dragThreshold Float.X = 6

// dragPlaceTravel is how far (in screen pixels) the cursor must have moved
// UP from the press anchor for a drag-release to count as a deliberate
// drop into the world rather than the drawer collapsing out from under a
// near-stationary cursor (an accidental micro-drag). Comfortably larger
// than dragThreshold so an idle wiggle never trips placement, far smaller
// than a real drag from the palette up into the scene.
const dragPlaceTravel Float.X = 32

// previewDropZoneReporter is implemented by editors that can tell the
// drag flow whether the in-progress preview is currently hovering a
// valid drop site. When true, the design explorer hides the 2D ghost
// (the 3D preview already shows where the design will land); when
// false, the 2D ghost follows the cursor as visual feedback.
type previewDropZoneReporter interface {
	PreviewOverDropZone() bool
}

// previewClearer is implemented by editors that can wipe their
// preview without arguments. The drag flow calls it on a missed drop
// so the ghost doesn't linger after the user releases the mouse off
// any valid drop site.
type previewClearer interface {
	ClearPreview()
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

// preferredAuthor returns the highest-ranked author from prefs that is
// present in available. If none match, it falls back to the
// lexicographically first key in available (preserving the prior reset
// behaviour), or "" if available is empty.
func preferredAuthor(available map[string]struct{}, prefs []string) string {
	for _, p := range prefs {
		if _, ok := available[p]; ok {
			return p
		}
	}
	if len(available) == 0 {
		return ""
	}
	return slices.Sorted(maps.Keys(available))[0]
}

// bumpAuthorPreference moves name to the front of the global ranked
// preference list (creating it if necessary) and drops any duplicate.
func bumpAuthorPreference(name string) {
	prefs := UserState.AuthorPreferences
	newPrefs := make([]string, 0, len(prefs)+1)
	newPrefs = append(newPrefs, name)
	for _, p := range prefs {
		if p != name {
			newPrefs = append(newPrefs, p)
		}
	}
	UserState.AuthorPreferences = newPrefs
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
		// On Android/Quest exports, .import metadata files are stripped
		// and `FileAccess.FileExists(...".import")` always returns
		// false — which made the loop skip every theme and left the
		// drawer empty. ResourceLoader.Exists works against the
		// packed converted texture (.ctex) too, so it's correct on
		// both desktop and Android.
		if ExistsSync("res://library/" + name + "/icon.png") {
			button := TextureButton.New().
				SetTextureNormal(LoadSync[Texture2D.Instance]("res://library/" + name + "/icon.png")).
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
					if authorHidden(theme) {
						continue
					}
					other_button, _ := de.themes[theme].Instance()
					other_button.AsCanvasItem().SetVisible(true)
				}
				de.Refresh(de.client.Editing, name, de.client.ui.mode)
				de.Panel.Themes.Heading.Selected.SetTextureNormal(LoadSync[Texture2D.Instance]("res://library/" + name + "/icon.png"))
				button, _ := de.themes[name].Instance()
				button.AsCanvasItem().SetVisible(false)
				// Record explicit user choice as the new top-ranked preference
				// and persist so the design explorer remembers across runs and
				// editor switches. Skipped in the library-sizing debug mode so
				// a sizing session doesn't reshuffle the saved preferences the
				// explorer will use in normal play.
				if de.client != nil && librarySizesFile() == "" {
					bumpAuthorPreference(name)
					de.client.saveUserState()
				}
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
	ui.updateDrag()
}

// ensureDragGhost lazily creates the 2D thumbnail overlay used during
// a drag. Parented to the UI root (rather than the design explorer
// panel) so the ghost can float anywhere on screen without inheriting
// the drawer's animated transform. Mouse-filter-ignore so it never
// eats clicks, top-level so it doesn't get sucked into the explorer's
// HBoxContainer layout, and Z-indexed above sibling UI.
func (ui *DesignExplorer) ensureDragGhost() {
	if ui.drag_ghost != TextureRect.Nil {
		return
	}
	rect := TextureRect.New()
	rect.SetExpandMode(TextureRect.ExpandIgnoreSize)
	rect.SetStretchMode(TextureRect.StretchKeepAspectCentered)
	rect.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	rect.AsControl().SetSize(Vector2.New(96, 96))
	rect.AsCanvasItem().SetVisible(false)
	rect.AsCanvasItem().SetTopLevel(true)
	rect.AsCanvasItem().SetZIndex(100)
	var parent Node.Instance
	if ui.client != nil && ui.client.ui != nil {
		parent = ui.client.ui.AsControl().AsNode()
	} else {
		parent = ui.AsNode()
	}
	parent.AddChild(rect.AsNode())
	ui.drag_ghost = rect
}

// armDrag is called from a tile's button_down. It captures the press
// anchor and pending design but deliberately does NOT call SelectDesign
// or close the drawer yet — that way an accidental press that's
// released off the tile (with no movement past dragThreshold) leaves
// the explorer state untouched, exactly like the pre-drag behaviour.
// The preview is attached either when updateDrag confirms drag intent
// (threshold crossed) or when the tile's OnPressed fires (legacy tap).
func (ui *DesignExplorer) armDrag(mode Mode, resource string, thumb Texture2D.Instance) {
	ui.ensureDragGhost()
	ui.drag_active = true
	ui.drag_started = false
	ui.drag_mode = mode
	ui.drag_resource = resource
	ui.drag_thumb = thumb
	ui.drag_anchor = ui.AsCanvasItem().GetGlobalMousePosition()
	if ui.drag_ghost != TextureRect.Nil {
		if thumb != Texture2D.Nil {
			ui.drag_ghost.SetTexture(thumb)
		}
		ui.drag_ghost.AsCanvasItem().SetVisible(false)
	}
}

// tapTile is the legacy click-to-enter-preview path. It runs from the
// tile's OnPressed (which Godot fires only when the release lands on
// the same button), so it both preserves the pre-drag behaviour and
// covers the "released without crossing the drag threshold" tap case.
// If drag intent was already confirmed (drag_started=true) we leave
// the preview to the drag/release path.
func (ui *DesignExplorer) tapTile(mode Mode, resource string) {
	if ui.drag_started {
		return
	}
	ui.editor.SelectDesign(mode, resource)
	ui.closeDrawer()
}

func (ui *DesignExplorer) updateDrag() {
	if !ui.drag_active {
		return
	}
	mouse := ui.AsCanvasItem().GetGlobalMousePosition()
	if !ui.drag_started && Vector2.Distance(mouse, ui.drag_anchor) > dragThreshold {
		// Drag intent confirmed — attach the preview and collapse the
		// drawer so the user sees the 3D ghost track their cursor.
		ui.drag_started = true
		ui.editor.SelectDesign(ui.drag_mode, ui.drag_resource)
		ui.closeDrawer()
	}
	if !Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
		ui.endDrag()
		return
	}
	if ui.drag_ghost == TextureRect.Nil {
		return
	}
	if !ui.drag_started {
		ui.drag_ghost.AsCanvasItem().SetVisible(false)
		return
	}
	overDropZone := false
	if dz, ok := ui.editor.(previewDropZoneReporter); ok {
		overDropZone = dz.PreviewOverDropZone()
	}
	ui.drag_ghost.AsCanvasItem().SetVisible(!overDropZone)
	if !overDropZone {
		size := ui.drag_ghost.AsControl().Size()
		ui.drag_ghost.AsControl().SetPosition(Vector2.New(
			mouse.X-size.X/2,
			mouse.Y-size.Y/2,
		))
	}
}

func (ui *DesignExplorer) endDrag() {
	if !ui.drag_active {
		return
	}
	dragged := ui.drag_started
	ui.drag_active = false
	ui.drag_started = false
	if ui.drag_ghost != TextureRect.Nil {
		ui.drag_ghost.AsCanvasItem().SetVisible(false)
	}
	if !dragged {
		return // tap path: legacy OnPressed already handled preview attach.
	}
	// A drag only drops a design into the world if it was released over the
	// 3D viewport — NOT over the design explorer. The desktop mouse picker
	// raycasts straight through the 2D panel (it's a Control, not a physics
	// collider), so previewOnTerrain alone can't tell a real drop from a
	// release over the palette; the click-to-place path is spared this only
	// because Godot's input propagation lets the panel consume clicks, which
	// this polled drag path bypasses. The panel is bottom-anchored, so "over
	// the viewport" means the cursor sits above the panel's current top edge;
	// we additionally require the cursor to have travelled up from the press
	// anchor, which rejects the drawer merely collapsing out from under a
	// near-stationary cursor. When the drop is rejected the design simply
	// stays selected as a preview (SelectDesign ran when the threshold was
	// crossed), so the user can click in the viewport to place it — a fumbled
	// drag degrades to exactly the same outcome as a tap.
	mouse := ui.AsCanvasItem().GetGlobalMousePosition()
	panelTop := ui.AsControl().GetGlobalRect().Position.Y
	overViewport := mouse.Y < panelTop
	travelledUp := ui.drag_anchor.Y-mouse.Y > dragPlaceTravel
	if !overViewport || !travelledUp {
		return
	}
	placed := false
	if pp, ok := ui.editor.(PreviewPlacer); ok {
		placed = pp.TryPlacePreview()
	}
	if !placed {
		if pc, ok := ui.editor.(previewClearer); ok {
			pc.ClearPreview()
		}
	}
}

// applyRecency reorders the tiles across every currently-built tab so
// the most-recently-placed designs come first. It walks
// placement_recency oldest-first and moves each matching tile to the
// front of its own grid, so after the pass the most-recent tile lands
// at child index 0 within its tab. Tiles whose design has never been
// placed keep their original library order behind the placed ones.
func (ui *DesignExplorer) applyRecency() {
	for i := len(ui.placement_recency) - 1; i >= 0; i-- {
		tile, ok := ui.tile_for_resource[ui.placement_recency[i]].Instance()
		if !ok {
			continue
		}
		parent := tile.AsNode().GetParent()
		if parent == Node.Nil {
			continue
		}
		parent.MoveChild(tile.AsNode(), 0)
	}
}

// BumpDesign records that an entity using `resource` was just placed,
// pushing it to the front of placement_recency and — when its tile is
// currently shown — moving that tile to the front of its tab grid so
// the explorer always lists the most recently built designs first.
// Called from the Change handler on every entity creation, so
// placements by any client keep the ordering live and observable.
func (ui *DesignExplorer) BumpDesign(resource string) {
	if resource == "" {
		return
	}
	// Library-sizing debug mode: keep every tab in its stable library
	// order instead of floating placed designs to the front, so working
	// through the catalogue sizing models stays trackable.
	if librarySizesFile() != "" {
		return
	}
	ui.placement_recency = slices.DeleteFunc(ui.placement_recency, func(s string) bool {
		return s == resource
	})
	ui.placement_recency = append([]string{resource}, ui.placement_recency...)
	tile, ok := ui.tile_for_resource[resource].Instance()
	if !ok {
		return
	}
	parent := tile.AsNode().GetParent()
	if parent == Node.Nil {
		return
	}
	parent.MoveChild(tile.AsNode(), 0)
}

// Refresh repopulates the tabbed designs depending on the active editor,
// these designs may be cached so that subsequent refreshes are faster.
func (ui *DesignExplorer) Refresh(editor Subject, author string, mode Mode) {
	expansion, _ := ui.ExpansionIndicator.Instance()
	// Rebuilt each Refresh — tiles are recreated below, so the previous
	// map's node IDs are stale. placement_recency persists separately.
	ui.tile_for_resource = make(map[string]TextureButton.ID)
	for _, node := range ui.Tabs.AsNode().GetChildren() {
		ui.Tabs.AsNode().RemoveChild(node)
		node.QueueFree()
	}
	if ui.Tabs.AsNode().GetChildCount() == 0 {
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
	// Authors whose license badge is toggled off in the Settings menu are
	// filtered out here (rather than from the cached availability map) so
	// flipping a badge back on only needs a Refresh, not a rescan.
	visible_authors := make(map[string]struct{}, len(themes_available))
	for theme := range themes_available {
		if !authorHidden(theme) {
			visible_authors[theme] = struct{}{}
		}
	}
	if _, ok := visible_authors[author]; !ok {
		author = ""
	}
	if author == "" {
		author = preferredAuthor(visible_authors, UserState.AuthorPreferences)
		if author != "" {
			ui.Panel.Themes.Heading.Selected.SetTextureNormal(LoadSync[Texture2D.Instance]("res://library/" + author + "/icon.png"))
		}
	}
	for _, theme := range slices.Sorted(maps.Keys(visible_authors)) {
		if theme == author {
			continue // chosen author is shown in the heading; hide its button
		}
		button, _ := ui.themes[theme].Instance()
		button.AsCanvasItem().SetVisible(true)
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
			if ExistsSync("res://ui/" + strings.ToLower(editor.String()) + "/" + tab + ".svg") {
				ui.Tabs.SetTabIcon(index, LoadSync[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+"/"+tab+".svg"))
			} else {
				ui.Tabs.SetTabIcon(index, LoadSync[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+".svg"))
			}
			ui.Tabs.SetTabTitle(index, "")
			edits = true
			index++
		} else {
			var path = "res://preview/" + author + "/"
			path += tab
			resources := DirAccess.Open(path)
			// Builtin procedural tiles the editor wants shown in this
			// tab (e.g. the critter editor's procedural foreleg). The
			// tab is shown if EITHER the library has entries OR the
			// editor injects builtins, so procedural-only tabs aren't
			// hidden by a missing preview directory.
			var builtins []BuiltinDesign
			if provider, ok := ui.editor.(BuiltinDesignProvider); ok {
				builtins = provider.BuiltinDesigns(mode, tab)
			}
			if resources == DirAccess.Nil && len(builtins) == 0 {
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
			for _, b := range builtins {
				resource := b.Resource
				button := TextureButton.New()
				var iconTex Texture2D.Instance
				if b.Icon != "" {
					if icon := LoadSync[Texture2D.Instance](b.Icon); icon != Texture2D.Nil {
						iconTex = icon
						button.SetTextureNormal(icon)
					}
				}
				base := button.SetIgnoreTextureSize(true).
					SetStretchMode(TextureButton.StretchKeepAspectCentered).
					AsBaseButton()
				base.OnButtonDown(func() {
					ui.armDrag(mode, resource, iconTex)
				})
				base.OnPressed(func() {
					ui.tapTile(mode, resource)
				})
				if b.Label != "" {
					button.AsControl().SetTooltipText(b.Label)
				}
				elements.AsNode().AddChild(button.
					AsControl().SetCustomMinimumSize(Vector2.New(256, 256)).
					AsControl().SetMouseFilter(Control.MouseFilterStop).AsNode())
				ui.tile_for_resource[resource] = button.ID()
			}
			if resources == DirAccess.Nil {
				gridflow.Update()
				if ExistsSync("res://ui/" + tab + ".svg") {
					ui.Tabs.SetTabIcon(index, LoadSync[Texture2D.Instance]("res://ui/"+tab+".svg"))
				} else {
					ui.Tabs.SetTabIcon(index, LoadSync[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+".svg"))
				}
				ui.Tabs.SetTabTitle(index, "")
				index++
				continue
			}
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
					resource := library_path + "/" + tab + "/" + strings.TrimSuffix(string(resource), ".png")
					if tscn := library_path + "/" + tab + "/" + String.TrimSuffix(resource, ".png") + ".tscn"; FileAccess.FileExists(tscn) {
						resource = tscn
					}
					// Load the thumbnail off the main thread: the palette has
					// hundreds of these and they aren't needed for the world to
					// render, so blocking on each one stalled the whole load. The
					// tile exists immediately; its texture pops in when ready.
					tile := TextureButton.New().
						SetIgnoreTextureSize(true).
						SetStretchMode(TextureButton.StretchKeepAspectCentered)
					tileID := tile.ID()
					var preview Texture2D.Instance
					LoadAsync(path, func(tex Texture2D.Instance) {
						preview = tex
						if tex == Texture2D.Nil {
							return
						}
						if b, ok := tileID.Instance(); ok {
							b.SetTextureNormal(tex)
						}
					})
					tile.AsBaseButton().OnButtonDown(func() {
						ui.armDrag(mode, resource, preview)
					})
					tile.AsBaseButton().OnPressed(func() {
						ui.tapTile(mode, resource)
					})
					tile.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
					tile.AsControl().SetMouseFilter(Control.MouseFilterStop)
					elements.AsNode().AddChild(tile.AsNode())
					ui.tile_for_resource[resource] = tile.ID()
				case png:
					// Prefer a .region sidecar over a raw .png when both
					// exist — the sidecar describes a sub-region of a
					// shared atlas material, while the raw .png is the
					// legacy pre-cropped form.
					base := strings.TrimSuffix(string(resource), ".png")
					region_path := library_path + "/" + tab + "/" + base + ".region"
					resource := library_path + "/" + tab + "/" + resource
					if FileAccess.FileExists(region_path) {
						resource = region_path
					}
					// Thumbnail loaded off the main thread (see the glb case).
					tile := TextureButton.New().
						SetIgnoreTextureSize(true).
						SetStretchMode(TextureButton.StretchKeepAspectCentered)
					tileID := tile.ID()
					var texture Texture2D.Instance
					LoadAsync(path, func(tex Texture2D.Instance) {
						texture = tex
						if tex == Texture2D.Nil {
							return
						}
						if b, ok := tileID.Instance(); ok {
							b.SetTextureNormal(tex)
						}
					})
					tile.AsBaseButton().OnButtonDown(func() {
						ui.armDrag(mode, resource, texture)
					})
					tile.AsBaseButton().OnPressed(func() {
						ui.tapTile(mode, resource)
					})
					tile.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
					tile.AsControl().SetMouseFilter(Control.MouseFilterStop)
					elements.AsNode().AddChild(tile.AsNode())
					ui.tile_for_resource[resource] = tile.ID()
				}
			}
			gridflow.Update()
			if ExistsSync("res://ui/" + tab + ".svg") {
				ui.Tabs.SetTabIcon(index, LoadSync[Texture2D.Instance]("res://ui/"+tab+".svg"))
			} else {
				ui.Tabs.SetTabIcon(index, LoadSync[Texture2D.Instance]("res://ui/"+strings.ToLower(editor.String())+".svg"))
			}
			ui.Tabs.SetTabTitle(index, "")
			index++
		}
	}
	// Now that every tab's tiles exist, reorder them by how recently
	// their design was placed in the scene (most recent first).
	ui.applyRecency()
	if len(visible_authors) == 0 {
		ui.Panel.Themes.Heading.Selected.SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/editing.svg"))
	}
	ui.AsCanvasItem().SetVisible(index > 0 || len(visible_authors) > 0)
	expansion.AsCanvasItem().SetVisible(index > 0 && !edits)
}

func (ui *DesignExplorer) UnhandledInput(event InputEvent.Instance) {
	if ui.drawExpanded.Load() && Object.Is[InputEventMouseMotion.Instance](event) {
		height := uiDisplaySize(ui.client).Y
		if ui.AsCanvasItem().GetGlobalMousePosition().Y < Float.X(height)*0.3 {
			ui.closeDrawer()
		}
	}
}

// fadeGizmos tweens the modulate alpha of the gizmo toolbar
// (GizmoTypes + GizmoIndicator on CloudControl) to `alpha` over
// 150 ms. Called with 0.0 when the drawer opens (so the gizmo
// strip doesn't fight the design grid visually) and 1.0 when it
// closes. Modulate propagates to children, so the whole strip of
// gizmo buttons fades together.
func (ui *DesignExplorer) fadeGizmos(alpha float32) {
	if ui.client == nil || ui.client.ui == nil || ui.client.ui.CloudControl == nil {
		return
	}
	cc := ui.client.ui.CloudControl
	const dur = 0.15
	for _, ctl := range []Control.Instance{
		cc.GizmoTypes.AsControl(),
		cc.GizmoIndicator.AsControl(),
	} {
		target := ctl.AsCanvasItem().Modulate()
		target.A = alpha
		tween := SceneTree.Get(ui.AsNode()).CreateTween()
		PropertyTweener.Make(tween, ctl.AsObject(), "modulate", target, dur).SetEase(Tween.EaseOut)
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
	ui.fadeGizmos(0.0)
	window_size := uiDisplaySize(ui.client)
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
	ui.fadeGizmos(1.0)
	window_size := uiDisplaySize(ui.client)
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
