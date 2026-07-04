package internal

import (
	"strings"

	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// critter_instance.go is the SPIKE for the "user design" feature: it proves a
// critter creation can be reconstructed and instanced *outside* the CritterEditor
// singleton, purely from captured data. It reuses the same CritterBody bridge the
// editor uses, but with no editor/world state — which is exactly what a
// musical-data-backed user design needs in order to be placeable in the scenery
// editor (or anywhere) and replayable on a peer.
//
// Scope of the spike: a frozen reconstruction (static pose, no gait/eye
// animation drivers). The capture format here (CritterCreation) is a stand-in
// for what the real bookmark bundle will carry as musical records; see
// CritterEditor.CaptureCreation.

// CritterCreation is a self-contained description of a built critter: the folded
// procedural skeleton (bones + legs + macro weights) plus the attached parts and
// their library URIs/anchors. It is everything needed to rebuild the creature
// with no reference to the authoring editor.
type CritterCreation struct {
	Bones   []critter.Bone
	Legs    []critter.Leg
	Weights map[string]float32
	Parts   []CritterPartRef
}

// CritterPartRef is one attached part: the design URI to instantiate and the
// surface/leg anchor it rides on.
type CritterPartRef struct {
	URI    string
	Anchor PartAnchor
}

// buildCritterInstance reconstructs a CritterCreation into a standalone Node3D
// subtree with no dependency on CritterEditor: it rebuilds the procedural body
// from the captured skeleton via the CritterBody bridge, then re-attaches each
// part at its anchor. The returned wrapper owns its own MeshInstance3D and the
// Skeleton3D that AttachCritterBody spawns beside it, so several reconstructed
// critters can coexist in one scene (the "../Skeleton3D" skin path resolves
// per-instance). The caller adds the wrapper to the tree.
//
// heightAt is the terrain-surface probe handed to the CritterAnimator so the
// legs can respond to vertical root motion (the possession jump); previews and
// other terrain-less contexts pass nil.
func buildCritterInstance(cc CritterCreation, heightAt func(Vector3.XYZ) Float.X) (Node3D.Instance, error) {
	wrapper := Node3D.New()
	mi := MeshInstance3D.New()
	// mi must have a parent before AttachCritterBody so the Skeleton3D it spawns
	// lands under THIS wrapper (scoping the "../Skeleton3D" skin path) — mirroring
	// how CritterEditor.ensureLoaded parents mi before attaching the body.
	wrapper.AsNode().AddChild(mi.AsNode())

	c := critter.New()
	c.Restore(cc.Bones, cc.Legs, cc.Weights)
	body, err := AttachCritterBody(mi, c)
	if err != nil {
		wrapper.AsNode().QueueFree()
		return Node3D.Nil, err
	}
	// Coalesce the per-part attaches into a single rebuild at the end.
	body.PauseRebuild()
	for _, p := range cc.Parts {
		attachCreationPart(&body, p)
	}
	body.ResumeRebuild()
	// A user creation has no baked animation clips, but possession and the
	// walk-here animator both look up a child named "AnimationPlayer" and call
	// methods on it. Attach an empty one so those paths find a valid player and
	// simply resolve no clips (empty animation list) — the critter moves without
	// nil-guards scattered through the animation code. Real gait/idle animation is
	// a later step (the standalone animator).
	anim := AnimationPlayer.New()
	anim.AsNode().SetName("AnimationPlayer")
	wrapper.AsNode().AddChild(anim.AsNode())
	// Decouple from the TerrainEditor subtree's ProcessMode. Placed creations live
	// under TerrainEditor, which StartEditing flips to ProcessModeDisabled whenever
	// it isn't the active editor (i.e. always while in the scenery editor, where you
	// possess/walk them) — that would freeze the CritterAnimator's gait. Pausable
	// keeps it ticking regardless (only a real tree pause stops it), matching how
	// maybeAttachEntityAnimator decouples library mobile entities.
	wrapper.AsNode().SetProcessMode(Node.ProcessModePausable)
	// Hand the body to a CritterAnimator so the legs step procedurally while the
	// creation moves (possession / walk-here) — the same gait the critter editor's
	// control view uses. Its Ready (when the wrapper enters the tree) builds the
	// animated leg renders.
	attachCritterAnimator(wrapper.AsNode3D(), body, heightAt)
	// Stamp the whole subtree's scene-owner to the wrapper. PackedScene.Instantiate
	// sets owners automatically, but this tree is built by hand — without owners
	// the viewport selection raycast (which selects collider.Owner()) can't resolve
	// a click on the creature back to the registered entity, so a placed creation
	// would be unselectable / unmovable. The body's own collision sits on layer
	// 1<<1 (skipped by the selection mask) so only the parts' pickable collision
	// drives selection; both now resolve to the wrapper.
	setSubtreeOwner(wrapper.AsNode(), wrapper.AsNode())
	return wrapper.AsNode3D(), nil
}

// NOTE: creations are instanced by building fresh each time (buildCritterInstance),
// not via PackedScene. PackedScene.Pack does not round-trip the procedural skinned
// body — the rigid parts survive but the skinned ArrayMesh+Skeleton3D+Skin collapse
// to nothing — so there is no pack/cache step; see Client.buildDesignNode.

// attachCreationPart instantiates one part design and attaches it at its anchor,
// covering the same kinds tryAttachChange handles: procedural parts (built in
// code), mod:// drop-ins, .obj static meshes, and ordinary library PackedScenes.
func attachCreationPart(body *CritterBody, p CritterPartRef) {
	if p.URI == "" {
		return
	}
	if part := newProceduralPart(p.URI); part != nil {
		body.AttachPartNode(p.Anchor, part.Node())
		return
	}
	if isModPath(p.URI) {
		if scene, ok := loadModPackedScene(p.URI); ok {
			body.AttachPart(p.Anchor, scene)
		}
		return
	}
	if strings.HasSuffix(p.URI, ".obj") {
		if node := loadStaticObjNode(p.URI); node != Node3D.Nil {
			body.AttachPartNode(p.Anchor, node)
		}
		return
	}
	if scene := LoadSync[PackedScene.Instance](p.URI); scene != PackedScene.Nil {
		body.AttachPart(p.Anchor, scene)
	}
}
