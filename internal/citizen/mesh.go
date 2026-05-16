package citizen

// Citizen holds the runtime state of a character being edited: an
// immutable copy of the base mesh's vertex positions, a library of loaded
// targets keyed by name, the current slider weight applied to each, and
// the per-slot equipped clothing designs.
//
// This type is pure data — no Godot bindings. The editor layer
// (editor_citizen.go) owns the actual ArrayMesh and pushes Recompute()'s
// output into it. Keeping the math here means it tests on its own.
type Citizen struct {
	base      []Vec3
	final     []Vec3 // working buffer, reused across Recompute calls
	targets   map[string]*Target
	weights   map[string]float32
	dressings map[string]string // slot → res:// design path
}

// New creates a Citizen with the given base mesh vertex positions. The
// caller retains ownership of base; Citizen takes a defensive copy so it
// stays valid even if the caller mutates the source slice.
func New(base []Vec3) *Citizen {
	c := &Citizen{
		base:      make([]Vec3, len(base)),
		final:     make([]Vec3, len(base)),
		targets:   make(map[string]*Target),
		weights:   make(map[string]float32),
		dressings: make(map[string]string),
	}
	copy(c.base, base)
	copy(c.final, base)
	return c
}

// AddTarget registers a target so it can be referenced by SetWeight. A
// target re-added under the same name replaces the previous entry.
func (c *Citizen) AddTarget(t *Target) {
	c.targets[t.Name] = t
}

// AddTargets is a convenience for AddTarget over a slice.
func (c *Citizen) AddTargets(ts []*Target) {
	for _, t := range ts {
		c.AddTarget(t)
	}
}

// SetWeight sets the slider weight for a named target. Weight 0 (or
// negative weights for one-way targets) are treated as inactive and
// dropped from the working set. Returns true iff the stored value changed.
func (c *Citizen) SetWeight(name string, weight float32) bool {
	cur := c.weights[name]
	if cur == weight {
		return false
	}
	if weight == 0 {
		delete(c.weights, name)
	} else {
		c.weights[name] = weight
	}
	return true
}

// Weight returns the current weight for a named target (0 if unset).
func (c *Citizen) Weight(name string) float32 {
	return c.weights[name]
}

// Weights returns a snapshot of all non-zero target weights. Suitable for
// serializing the character to disk as a slider-value config.
func (c *Citizen) Weights() map[string]float32 {
	m := make(map[string]float32, len(c.weights))
	for k, v := range c.weights {
		m[k] = v
	}
	return m
}

// SetDressing records the design equipped in a slot. Pass an empty
// design to unequip. Returns true iff the value actually changed.
func (c *Citizen) SetDressing(slot, design string) bool {
	cur := c.dressings[slot]
	if cur == design {
		return false
	}
	if design == "" {
		delete(c.dressings, slot)
	} else {
		c.dressings[slot] = design
	}
	return true
}

// Dressing returns the design equipped in a slot, or "" if unequipped.
func (c *Citizen) Dressing(slot string) string {
	return c.dressings[slot]
}

// Dressings returns a snapshot of all equipped clothing as a map of
// slot → design path. Suitable for serializing the character to disk
// alongside its slider weights.
func (c *Citizen) Dressings() map[string]string {
	m := make(map[string]string, len(c.dressings))
	for k, v := range c.dressings {
		m[k] = v
	}
	return m
}

// Recompute applies every weighted target to the base mesh and returns
// the resulting vertex array. The slice is owned by Citizen and reused
// across calls; callers must copy before retaining or sending across
// goroutines.
func (c *Citizen) Recompute() []Vec3 {
	copy(c.final, c.base)
	for name, w := range c.weights {
		t, ok := c.targets[name]
		if !ok || w == 0 {
			continue
		}
		for _, d := range t.Deltas {
			if d.Index < 0 || d.Index >= len(c.final) {
				continue
			}
			c.final[d.Index].X += w * d.Offset.X
			c.final[d.Index].Y += w * d.Offset.Y
			c.final[d.Index].Z += w * d.Offset.Z
		}
	}
	return c.final
}
