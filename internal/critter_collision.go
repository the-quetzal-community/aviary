package internal

import (
	"strings"

	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Skeleton3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
)

// CritterCollider keeps a snake's selection box glued to its animated pose.
//
// The "everything" critters are skinned, and import_collision.gd bakes their
// convex hull from the (coiled) bind pose. A quadruped walks roughly in place,
// so that hull still covers it — but a snake's idle/"Side winding" clip undulates
// the body a metre or two away from the bind pose, leaving the hull as a small
// dome stranded where the snake no longer is. A click on the visible snake then
// hits terrain, so it can never be selected. Only the snake and rattlesnake need
// this; everything else is fine with its static hull.
//
// The fix reuses the model's existing CollisionShape3D (rather than adding a new
// body) so its StaticBody's owner still resolves to the critter root — which is
// what client.go's selection raycast walks to. Each tick it refits a box around
// the live skeleton bone positions, so the selectable volume follows the body.
type CritterCollider struct {
	Node.Extension[CritterCollider]

	skeleton Skeleton3D.Instance
	shape    CollisionShape3D.Instance
	box      BoxShape3D.Instance
	accum    Float.X
}

func (c *CritterCollider) Ready() { c.refit() }

func (c *CritterCollider) Process(delta Float.X) {
	// The body undulates continuously, but selection tolerates a little lag, so
	// refit a few times a second rather than every frame.
	c.accum += delta
	if c.accum < 0.1 {
		return
	}
	c.accum = 0
	c.refit()
}

// refit resizes/recentres the box to wrap the current bone positions. Bones live
// in skeleton space; the collision shape's parent chain (StaticBody → mesh →
// skeleton) is all identity, so bone origins map straight onto the shape's local
// frame.
func (c *CritterCollider) refit() {
	n := c.skeleton.GetBoneCount()
	if n == 0 {
		return
	}
	var mn, mx Vector3.XYZ
	for i := range n {
		o := c.skeleton.GetBoneGlobalPose(i).Origin
		if i == 0 {
			mn, mx = o, o
		} else {
			mn, mx = Vector3.Min(mn, o), Vector3.Max(mx, o)
		}
	}
	size := Vector3.Sub(mx, mn)
	center := Vector3.MulX(Vector3.Add(mn, mx), 0.5)
	// Bone origins trace the body's centreline; pad so the box wraps its girth
	// and stays comfortably clickable (and never collapses to a plane).
	const pad = Float.X(0.3)
	c.box.SetSize(Vector3.XYZ{X: size.X + pad, Y: size.Y + pad, Z: size.Z + pad})
	c.shape.AsNode3D().SetPosition(center)
}

// maybeAttachSnakeCollider wires a CritterCollider onto a freshly instantiated
// snake or rattlesnake (both library paths contain "snake"; "worm" and the rest
// don't). No-op for any other model, or if one is already attached.
func maybeAttachSnakeCollider(path string, root Node3D.Instance) {
	if !strings.Contains(path, "snake") {
		return
	}
	if root.AsNode().HasNode("CritterCollider") {
		return
	}
	skel, ok := findSkeleton(root.AsNode())
	if !ok {
		return
	}
	shape, ok := findCollisionShape(root.AsNode())
	if !ok {
		return
	}
	box := BoxShape3D.New()
	shape.SetShape(box.AsShape3D())
	c := new(CritterCollider)
	c.skeleton = skel
	c.shape = shape
	c.box = box
	c.AsNode().SetName("CritterCollider")
	root.AsNode().AddChild(c.AsNode())
}

func findSkeleton(n Node.Instance) (Skeleton3D.Instance, bool) {
	if s, ok := Object.As[Skeleton3D.Instance](n); ok {
		return s, true
	}
	for _, child := range n.GetChildren() {
		if s, ok := findSkeleton(child); ok {
			return s, ok
		}
	}
	return Skeleton3D.Nil, false
}

// findCollisionShape returns the first CollisionShape3D in the tree — the snake's
// only one is the import-baked hull (the AviaryModelLoader body has no shape).
func findCollisionShape(n Node.Instance) (CollisionShape3D.Instance, bool) {
	if cs, ok := Object.As[CollisionShape3D.Instance](n); ok {
		return cs, true
	}
	for _, child := range n.GetChildren() {
		if cs, ok := findCollisionShape(child); ok {
			return cs, ok
		}
	}
	return CollisionShape3D.Nil, false
}
