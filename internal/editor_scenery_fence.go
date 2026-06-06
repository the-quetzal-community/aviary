package internal

import (
	"path"
	"strings"

	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// fenceTool lets the scenery editor lay a continuous run of fence panels by
// stretching a line across the terrain, instead of dropping one panel per click.
// It is embedded in SceneryEditor and only arms itself for designs that live in
// a .../fencing/ library folder; for every other design it is an inert no-op and
// the editor's one-entity-per-click path is untouched.
//
// The interaction is a two-click line tool, NOT a press-drag-release: the first
// left click anchors the start and enters preview mode; while previewing (button
// released) the run follows the cursor, the single Preview is hidden, and a pool
// of lightweight ghost instances (cheap Instantiate copies of the same
// PackedScene) shows the whole line, each panel re-seated onto the terrain height
// under it. A second left click commits; right click cancels.
//
// The run axis, panel length and seating pivot all come from a PCA of the panel
// mesh (see fenceFootprint / analyzeFootprint), so a diagonal panel runs along
// its diagonal and an off-centre-origin panel (Kenney fences anchor their origin
// on a unit-cell edge) still seats on the line. The same analysis flags blocky
// corner/blob pieces as NOT draggable, so those fall back to single placement.
//
// On commit every ghost position becomes its own committed musical.Change entity
// — identical to placing that many panels by hand, so peers and the .mus3 log
// observe the run as ordinary placements (every mutation stays observable). The
// whole run is recorded as ONE grouped undo entry, so a single Ctrl+Z removes it
// rather than one panel per keypress.
type fenceTool struct {
	design string // current fencing design path ("" when the design isn't a fence)

	previewing bool        // between the first (anchor) and second (commit) click
	start      Vector3.XYZ // world point the run is anchored at (only X/Z are used)
	end        Vector3.XYZ // last previewed cursor end, committed on the second click

	scene     PackedScene.Is[Node3D.Instance] // cached so ghosts Instantiate cheaply
	scenePath string                          // design the cached scene was loaded for
	ghosts    Node3D.Instance                 // container parented under the editor
	pool      []Node3D.Instance               // reusable ghost panels (grown on demand)

	// fp is the cached geometric analysis of the current design's panel mesh;
	// fpDesign records which design it was computed for (recomputed lazily once
	// the preview mesh has loaded, since the PCA needs the geometry).
	fp       fenceFootprint
	fpDesign string

	// Hover heading: before a run is anchored, the preview rotation follows the
	// cursor's movement so the panel lies along the path it's being swept over.
	// hoverYaw is the current rotation; hoverPrev is last frame's cursor terrain
	// point (used to read the movement direction).
	hoverPrev    Vector3.XYZ
	hoverPrevSet bool
	hoverYaw     Angle.Radians

	// lastEnd is the far end of the most recently committed run. Holding Shift
	// continues from it — a Shift+commit click re-anchors the next run here (so
	// repeated Shift-clicks build a connected fence), and a Shift+anchor click in
	// hover mode resumes from it.
	lastEnd    Vector3.XYZ
	hasLastEnd bool

	// Captured from the live preview at the anchor click so spacing, orientation
	// and the pivot stay stable for the whole run even as the preview/terrain
	// change.
	seg      Float.X       // run-axis length of one panel, in world units
	angle    Angle.Radians // panel's run axis in its own local XZ (0 = +X, from PCA)
	scale    Vector3.XYZ   // preview root scale carried onto each placed panel
	startYaw Angle.Radians // preview yaw at anchor; orients a zero-length (click) run
	anchor   Vector3.XYZ   // pivot offset (world units, identity rot): near end along
	// the run, centred across the panel's thickness. Rotated by yaw and subtracted
	// from each line point so panels pivot around their end, tile forward from the
	// start, and sit centred on the line whatever the asset's origin.
}

// fenceFootprint is the cached geometric analysis of a fence design's panel mesh,
// from a PCA over its triangle vertices (in the placement frame). It drives the
// run axis (handling diagonals), panel length, seating pivot, and whether the
// piece is a thin strip worth dragging at all.
type fenceFootprint struct {
	angle     Angle.Radians // principal (run) axis in local XZ
	seg       Float.X       // extent along the run axis, world units
	anchor    Vector3.XYZ   // pivot: near end along run + centre across thickness
	draggable bool          // thin strip (line tool) vs blocky corner/blob (single place)
}

// fenceMaxSegments caps how many panels one run can lay, so a huge sweep can't
// spawn an unbounded number of entities (or ghost nodes). A very long run is
// truncated at this many panels.
const fenceMaxSegments = 256

// fenceMaxPerpRatio is the perpendicular/along-run extent ratio above which a
// fence piece is treated as a blocky corner/blob (placed singly) rather than a
// thin strip to drag. Straight and diagonal panels sit well below it (≤0.4);
// corner pieces and square wall panels sit above (0.5–1.0).
const fenceMaxPerpRatio Float.X = 0.5

// fenceHeadingMinMove is the cursor travel (world units) needed to re-aim the
// hover heading. It keeps the panel from twitching when the cursor is nearly
// still; once the cursor moves this far the rotation snaps to the new direction.
const fenceHeadingMinMove Float.X = 0.15

// isFenceDesign reports whether a design path lives in a .../fencing/ library
// folder (the convention the design explorer uses for the scenery "fencing" tab),
// which is what arms the fence line tool.
func isFenceDesign(design string) bool { return path.Base(path.Dir(design)) == "fencing" }

// isCornerPiece reports whether a design's base name marks it as a corner or end
// piece — transition pieces that shouldn't tile, so they're placed singly. Names
// can't be distinguished from thin strips by geometry alone (a corner's
// perp/run ratio can dip into the strip range), so this is a name backstop on top
// of the geometric ratio test.
func isCornerPiece(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "corner") || strings.HasSuffix(n, "_end")
}

// analyzeFootprint runs a PCA over the panel's triangle vertices (already in the
// placement frame and world scale; see PreviewRenderer.Faces) to find the run
// axis and seating pivot, and classifies the piece as draggable or not.
func analyzeFootprint(pts []Vector3.XYZ, name string) (fenceFootprint, bool) {
	if len(pts) < 3 {
		return fenceFootprint{}, false
	}
	var cx, cz Float.X
	for _, p := range pts {
		cx += p.X
		cz += p.Z
	}
	inv := 1 / Float.X(len(pts))
	cx, cz = cx*inv, cz*inv
	var sxx, szz, sxz Float.X
	for _, p := range pts {
		dx, dz := p.X-cx, p.Z-cz
		sxx += dx * dx
		szz += dz * dz
		sxz += dx * dz
	}
	// Principal axis of the XZ covariance = the run direction.
	angle := Angle.Atan2(2*sxz, sxx-szz) / 2
	rdx, rdz := Angle.Cos(angle), Angle.Sin(angle)
	// Extents along the run axis and across it (perp = (-rdz, rdx)).
	r0 := (pts[0].X-cx)*rdx + (pts[0].Z-cz)*rdz
	q0 := -(pts[0].X-cx)*rdz + (pts[0].Z-cz)*rdx
	runMin, runMax, perpMin, perpMax := r0, r0, q0, q0
	for _, p := range pts {
		r := (p.X-cx)*rdx + (p.Z-cz)*rdz
		q := -(p.X-cx)*rdz + (p.Z-cz)*rdx
		runMin, runMax = min(runMin, r), max(runMax, r)
		perpMin, perpMax = min(perpMin, q), max(perpMax, q)
	}
	run := runMax - runMin
	if run < 1e-4 {
		return fenceFootprint{}, false
	}
	perp := perpMax - perpMin
	// Pivot = near end along the run (runMin) + centre across the thickness.
	perpC := (perpMin + perpMax) / 2
	anchor := Vector3.New(
		cx+runMin*rdx-perpC*rdz,
		0,
		cz+runMin*rdz+perpC*rdx,
	)
	return fenceFootprint{
		angle:     angle,
		seg:       run,
		anchor:    anchor,
		draggable: perp/run <= fenceMaxPerpRatio && !isCornerPiece(name),
	}, true
}

// ensureFootprint computes (and caches) the geometric analysis for the current
// design from the live preview mesh. Returns false until the mesh has loaded.
func (f *fenceTool) ensureFootprint(editor *SceneryEditor) bool {
	if f.design == "" {
		return false
	}
	if f.fpDesign == f.design {
		return true
	}
	// Only measure once the CURRENT design's mesh is the one displayed — right
	// after a design switch the previous mesh is still attached for a frame
	// (its QueueFree is deferred), and measuring it would cache a wrong footprint.
	if editor.Preview.AttachedDesign() != f.design {
		return false
	}
	pts := editor.Preview.Faces()
	if len(pts) == 0 {
		return false // mesh not attached yet
	}
	name := strings.TrimSuffix(path.Base(f.design), path.Ext(f.design))
	fp, ok := analyzeFootprint(pts, name)
	if !ok {
		return false
	}
	f.fp, f.fpDesign = fp, f.design
	return true
}

// draggable reports whether the current design should engage the line tool (a
// thin straight/diagonal strip) rather than fall back to single placement (a
// corner/blob, or a non-fence design). False until the mesh has loaded.
func (f *fenceTool) draggable(editor *SceneryEditor) bool {
	return f.isFence() && f.ensureFootprint(editor) && f.fp.draggable
}

// isFence reports whether the currently selected design is a fence (and thus
// whether a left click anchors/commits a run rather than placing one entity).
func (f *fenceTool) isFence() bool { return f.design != "" }

// selectDesign arms the tool for a fencing design (and preloads its ghost scene)
// or disarms it for anything else, cancelling any in-progress run.
func (f *fenceTool) selectDesign(editor *SceneryEditor, design string) {
	if !isFenceDesign(design) {
		f.cancel(editor)
		f.design = ""
		return
	}
	f.design = design
	// A new design has its own run axis, so start the heading fresh.
	f.hoverPrevSet, f.hoverYaw = false, 0
	f.loadScene(design)
}

// loadScene caches the PackedScene for design so ghosts can be instantiated
// without a per-frame disk hit. The previous scene's ghost pool is freed because
// those instances are the wrong mesh once the design changes.
func (f *fenceTool) loadScene(design string) {
	if f.scenePath == design {
		return
	}
	f.scenePath = design
	f.scene = PackedScene.Is[Node3D.Instance]{}
	f.clearPool()
	LoadAsync(design, func(scene PackedScene.Is[Node3D.Instance]) {
		if f.scenePath != design { // superseded by a later selection
			return
		}
		f.scene = scene
	})
}

// begin anchors a fence run and enters preview mode, capturing the panel's
// analysed footprint (run axis, length, pivot) so the whole run spaces and orients
// its panels consistently. Only reached when draggable() is true, so the footprint
// is already computed.
//
// The run's END is the cursor; its START is the trailing far end of the hover
// preview — one panel back along the preview's run direction — so the panel the
// user was looking at stays put as the first segment and the run grows forward
// from it (rather than the panel flipping to the other side of the cursor). With
// Shift held and a previous run present, the start is that run's end instead.
func (f *fenceTool) begin(editor *SceneryEditor) {
	hover := editor.client.PreviewPicker()
	if !Object.Is[*TerrainTile](hover.Collider) {
		return
	}
	f.ensureFootprint(editor)
	f.previewing = true
	f.scale = editor.Preview.AsNode3D().Scale()
	f.startYaw = editor.Preview.AsNode3D().Rotation().Y
	f.angle = f.fp.angle
	f.anchor = f.fp.anchor
	f.seg = f.fp.seg
	if f.seg < 0.05 {
		f.seg = 0.05 // guard a degenerate footprint against a divide blow-up
	}
	cursor := hover.Position
	if f.hasLastEnd && Input.IsKeyPressed(Input.KeyShift) {
		f.start = f.lastEnd // continue the fence from the last run's end
	} else {
		// Trailing far end: the hover preview's panel runs from its pivot (at the
		// cursor) along world direction (cos, -sin) of (startYaw-angle); step one
		// panel that way so that end becomes the run's start and the previewed
		// panel is the first segment.
		rd := f.startYaw - f.angle
		f.start = Vector3.New(
			cursor.X+f.seg*Angle.Cos(rd),
			cursor.Y,
			cursor.Z-f.seg*Angle.Sin(rd),
		)
	}
	f.end = cursor
	f.ensureContainer(editor)
	f.resetHover() // re-seed the trailing heading when hover resumes after the run
	editor.Preview.AsNode3D().SetVisible(false)
	f.update(editor, cursor) // show the first segment immediately
}

// update lays out the ghost row from the drag start to end, re-seating each
// panel on the terrain. A no-op until the ghost scene has finished loading.
func (f *fenceTool) update(editor *SceneryEditor, end Vector3.XYZ) {
	f.end = end // remembered so a commit click off-terrain still uses it
	if f.scene == (PackedScene.Is[Node3D.Instance]{}) {
		return
	}
	f.ensureContainer(editor)
	segs, _, _ := f.segments(editor, end)
	for len(f.pool) < len(segs) {
		inst := f.scene.Instantiate()
		editor.Preview.remove_collisions(inst.AsNode())
		f.ghosts.AsNode().AddChild(inst.AsNode())
		f.pool = append(f.pool, inst)
	}
	for i, inst := range f.pool {
		if i >= len(segs) {
			inst.AsNode3D().SetVisible(false)
			continue
		}
		inst.AsNode3D().SetVisible(true)
		inst.AsNode3D().SetScale(f.scale)
		inst.AsNode3D().SetRotation(Euler.Radians{Y: segs[i].yaw})
		inst.AsNode3D().SetGlobalPosition(segs[i].pos)
	}
}

// commit places every panel of the run as its own entity and records the batch
// as one grouped undo. It uses the last previewed end so a commit click that
// lands off-terrain still places the line the user was looking at. With Shift
// held it then continues from the run's far end (staying in preview mode) so
// repeated Shift-clicks build a connected fence; otherwise it restores the single
// preview for a fresh run.
func (f *fenceTool) commit(editor *SceneryEditor) {
	if !f.previewing {
		return
	}
	segs, runEnd, runYaw := f.segments(editor, f.end)
	if len(segs) > 0 {
		design := editor.client.MusicalDesign(f.design)
		dos := make([]musical.Change, 0, len(segs))
		undos := make([]musical.Change, 0, len(segs))
		for _, s := range segs {
			change := musical.Change{
				Author: editor.client.id,
				Entity: editor.client.NextEntity(),
				Design: design,
				Offset: s.pos,
				Angles: Euler.Radians{Y: s.yaw},
				Bounds: f.scale,
				Commit: true,
			}
			editor.client.space.Change(change)
			dos = append(dos, change)
			undos = append(undos, musical.Change{
				Author: editor.client.id,
				Entity: change.Entity,
				Remove: true,
			})
		}
		editor.client.RecordChangeGroup(dos, undos)
		f.lastEnd, f.hasLastEnd = runEnd, true
	}
	// Shift held: continue the fence from this run's end without leaving preview
	// mode. The new run starts zero-length there, oriented along the run just laid
	// (runYaw), and grows as the cursor moves.
	if f.hasLastEnd && len(segs) > 0 && Input.IsKeyPressed(Input.KeyShift) {
		f.start = f.lastEnd
		f.startYaw = runYaw
		f.update(editor, f.start)
		return
	}
	f.previewing = false
	f.hideGhosts()
	editor.Preview.AsNode3D().SetVisible(true)
}

// cancel abandons an in-progress run, hiding the ghosts and restoring the
// single preview. Safe to call when not previewing.
func (f *fenceTool) cancel(editor *SceneryEditor) {
	if !f.previewing {
		return
	}
	f.previewing = false
	f.hideGhosts()
	editor.Preview.AsNode3D().SetVisible(true)
}

// hoverHeading aims the preview rotation along the cursor's movement: the panel's
// run axis snaps to lie along the path the cursor is sweeping, with the body
// trailing BEHIND the cursor (extending opposite to the movement). Called each
// frame while hovering a draggable fence (before the run is anchored); returns
// the rotation to apply. The footprint must already be analysed (f.fp valid).
func (f *fenceTool) hoverHeading(cur Vector3.XYZ) Angle.Radians {
	if !f.hoverPrevSet {
		f.hoverPrev, f.hoverPrevSet = cur, true
		return f.hoverYaw
	}
	dx, dz := cur.X-f.hoverPrev.X, cur.Z-f.hoverPrev.Z
	if dx*dx+dz*dz > fenceHeadingMinMove*fenceHeadingMinMove {
		// Aim the run axis along -movement so the body trails behind the cursor.
		// Same yaw mapping as segments(): r = angle + atan2(-uz, ux) with the run
		// direction u = -movement, which simplifies to atan2(dz, -dx).
		f.hoverYaw = f.fp.angle + Angle.Atan2(dz, -dx)
		f.hoverPrev = cur
	}
	return f.hoverYaw
}

// resetHover clears the heading state so the next hover re-seeds from a fresh
// cursor position (avoids a heading jump from a stale previous point after a run
// or a design switch).
func (f *fenceTool) resetHover() { f.hoverPrevSet = false }

// anchorOrigin maps a desired pivot world point to the node origin that lands the
// panel's pivot (near-end centre) there, using the preview's current yaw and the
// analysed footprint. Used for the pre-anchor hover preview so the panel hangs off
// the cursor by its end — matching how a committed run pivots each panel — instead
// of sitting beside it and jumping on the anchor click. The Y passes through
// unchanged (origin stays on the terrain). Returns target unchanged until the
// footprint has been computed.
func (f *fenceTool) anchorOrigin(editor *SceneryEditor, target Vector3.XYZ) Vector3.XYZ {
	if !f.ensureFootprint(editor) {
		return target
	}
	yaw := editor.Preview.AsNode3D().Rotation().Y
	cos, sin := Angle.Cos(yaw), Angle.Sin(yaw)
	offX := cos*f.fp.anchor.X + sin*f.fp.anchor.Z
	offZ := -sin*f.fp.anchor.X + cos*f.fp.anchor.Z
	return Vector3.New(target.X-offX, target.Y, target.Z-offZ)
}

type fenceSeg struct {
	pos Vector3.XYZ
	yaw Angle.Radians
}

// segments computes the panel transforms for a run from f.start to end. Panels
// of length f.seg tile forward from the start: panel i's near end pivots on the
// line point start+i*seg*u, so the run begins at the start (a post at the click)
// and grows toward the cursor, ending one panel-length past the last post. The
// returned pos is the node ORIGIN — the line point minus the panel's pivot offset
// (f.anchor) rotated by yaw — so the visible geometry pivots around its end and
// sits centred across its width on the line, whatever the asset's origin. Each
// panel's Y is sampled from the terrain so the run rides the surface, and its yaw
// aligns the panel's run axis with the drag direction.
func (f *fenceTool) segments(editor *SceneryEditor, end Vector3.XYZ) (segs []fenceSeg, runEnd Vector3.XYZ, runYaw Angle.Radians) {
	dx := end.X - f.start.X
	dz := end.Z - f.start.Z
	dist := Float.Sqrt(dx*dx + dz*dz)

	// Number of panels that tile the run: round(dist/seg), at least one.
	count := int(dist/f.seg + 0.5)
	if count < 1 {
		count = 1
	}
	if count > fenceMaxSegments {
		count = fenceMaxSegments
	}

	// Unit run direction and the yaw that turns the panel's run axis (f.angle, in
	// the panel's local XZ) to face along it. A node +Y rotation r sends a local
	// direction at angle a to world angle (r-a) about +X, so aligning the local
	// run axis with the world drag direction needs r = f.angle + atan2(-uz, ux).
	// (For an axis-aligned panel f.angle is 0 or ±90°, recovering the simple case;
	// for a diagonal panel it is ±45°.) A click with no meaningful drag has no
	// direction, so fall back to the preview's yaw (what the hover preview showed).
	var (
		ux, uz Float.X = 1, 0
		yaw    Angle.Radians
	)
	if dist > 1e-4 {
		ux, uz = dx/dist, dz/dist
		yaw = f.angle + Angle.Atan2(-uz, ux)
	} else {
		yaw = f.startYaw
	}

	// Rotate the pivot offset by yaw so subtracting it lands the panel's near-end
	// centre on each line point. Constant across the run since yaw is fixed.
	cos, sin := Angle.Cos(yaw), Angle.Sin(yaw)
	offX := cos*f.anchor.X + sin*f.anchor.Z
	offZ := -sin*f.anchor.X + cos*f.anchor.Z

	segs = make([]fenceSeg, 0, count)
	for i := 0; i < count; i++ {
		t := Float.X(i) * f.seg
		lx := f.start.X + ux*t // line point the panel's near end pivots on
		lz := f.start.Z + uz*t
		// Sample terrain at the pivot (the post), and place the node origin offset
		// back so the geometry pivots there. Origin Y stays on the surface (assets
		// base at y=0).
		py := editor.client.TerrainEditor.HeightAt(Vector3.New(lx, 0, lz))
		segs = append(segs, fenceSeg{pos: Vector3.New(lx-offX, py, lz-offZ), yaw: yaw})
	}
	// Far end of the run (one panel past the last post) — the seamless point to
	// continue from when chaining with Shift.
	ex := f.start.X + ux*Float.X(count)*f.seg
	ez := f.start.Z + uz*Float.X(count)*f.seg
	runEnd = Vector3.New(ex, editor.client.TerrainEditor.HeightAt(Vector3.New(ex, 0, ez)), ez)
	runYaw = yaw
	return segs, runEnd, runYaw
}

// ensureContainer lazily creates the ghost parent under the editor. The editor
// sits at the world origin (it positions its preview via SetGlobalPosition), so
// the container's identity transform makes per-ghost global positions line up.
func (f *fenceTool) ensureContainer(editor *SceneryEditor) {
	if f.ghosts != Node3D.Nil {
		return
	}
	f.ghosts = Node3D.New()
	editor.AsNode().AddChild(f.ghosts.AsNode())
}

func (f *fenceTool) hideGhosts() {
	for _, inst := range f.pool {
		inst.AsNode3D().SetVisible(false)
	}
}

func (f *fenceTool) clearPool() {
	for _, inst := range f.pool {
		inst.AsNode().QueueFree()
	}
	f.pool = f.pool[:0]
}
