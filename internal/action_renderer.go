package internal

import (
	"math"

	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type ActionRenderer struct {
	Node.Extension[ActionRenderer]

	Initial Vector3.XYZ

	playing string
	current int
	actions []musical.Action

	client *Client

	CurrentUp      Vector3.XYZ
	CurrentForward Vector3.XYZ
}

func (ar *ActionRenderer) Ready() {
	ar.playing = "Idle"
	ar.AsNode().SetProcess(false)
}

func (ar *ActionRenderer) Add(action musical.Action) {
	if len(ar.actions) > 0 {
		if action.Timing < ar.actions[len(ar.actions)-1].Timing {
			return
		}
		if action.Cancel {
			previous := ar.actions[ar.current]
			ar.Initial = Vector3.Lerp(ar.Initial, previous.Target, Float.X(ar.client.time.Now()-previous.Timing)/Float.X(previous.Period))
			ar.actions = ar.actions[0:0:cap(ar.actions)]
			ar.current = 0
		}
	}
	ar.actions = append(ar.actions, action)
	ar.AsNode().SetProcess(true)
}

func (ar *ActionRenderer) play(name string) {
	if ar.playing == name {
		return
	}
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	if parent.AsNode().HasNode("AnimationPlayer") {
		player := Object.To[AnimationPlayer.Instance](parent.AsNode().GetNode("AnimationPlayer"))
		player.AsAnimationMixer().GetAnimation(name).SetLoopMode(Animation.LoopLinear)
		player.PlayNamed(name)
	}
	ar.playing = name
}

func (ar *ActionRenderer) Process(delta Float.X) {
	action := ar.actions[ar.current]
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	for ar.client.time.Now()-action.Timing >= musical.Timing(action.Period) {
		pos := action.Target
		pos.Y = ar.client.TerrainRenderer.tile.HeightAt(pos)
		parent.SetPosition(pos)
		ar.Initial = action.Target
		ar.current++
		if ar.current >= len(ar.actions) {
			ar.AsNode().SetProcess(false)
			ar.play("Idle")
			ar.actions = ar.actions[0:0:cap(ar.actions)]
			ar.current = 0
			return
		}
		action = ar.actions[ar.current]
	}
	ar.play("Walk")
	dir := Vector3.Sub(action.Target, ar.Initial)
	dir.Y = 0
	pos := Vector3.Lerp(ar.Initial, action.Target, Float.X(ar.client.time.Now()-action.Timing)/Float.X(action.Period))
	pos.Y = ar.client.TerrainRenderer.tile.HeightAt(pos)
	parent.SetPosition(pos)
	ar.OrientModel(parent, pos, dir, ar.client.TerrainRenderer.tile.NormalAt(pos), delta)
}

// OrientModel aligns the model's up direction with the terrain normal while preserving the facing direction based on movement.
func (ar *ActionRenderer) OrientModel(model Node3D.Instance, pos Vector3.XYZ, movementDir Vector3.XYZ, normal Vector3.XYZ, delta Float.X) {
	// Normalize the normal to get the target up direction
	targetUp := Vector3.Normalized(normal)
	if Vector3.LengthSquared(targetUp) == 0 {
		targetUp = Vector3.XYZ{Y: 1}
	}

	// Smoothly interpolate the current up towards the target up
	if Vector3.LengthSquared(ar.CurrentUp) == 0 {
		ar.CurrentUp = Vector3.XYZ{Y: 1}
	}
	ar.CurrentUp = Vector3.Lerp(ar.CurrentUp, targetUp, Float.X(12)*delta)
	ar.CurrentUp = Vector3.Normalized(ar.CurrentUp)

	// Project the movement direction onto the tangent plane for target forward
	proj := Vector3.Dot(movementDir, targetUp)
	targetProjectedForward := Vector3.Sub(movementDir, Vector3.MulX(targetUp, proj))
	targetProjectedForwardLengthSq := Vector3.LengthSquared(targetProjectedForward)

	if targetProjectedForwardLengthSq > 0 {
		targetProjectedForward = Vector3.Normalized(targetProjectedForward)
	} else {
		// Fallback: Use an arbitrary direction in the tangent plane
		var arbitrary Vector3.XYZ
		ux := math.Abs(float64(targetUp.X))
		uy := math.Abs(float64(targetUp.Y))
		uz := math.Abs(float64(targetUp.Z))
		min := math.Min(ux, math.Min(uy, uz))
		if ux == min {
			arbitrary = Vector3.XYZ{X: 1}
		} else if uy == min {
			arbitrary = Vector3.XYZ{Y: 1}
		} else {
			arbitrary = Vector3.XYZ{Z: 1}
		}
		perp := Vector3.Cross(targetUp, arbitrary)
		perpLengthSq := Vector3.LengthSquared(perp)
		if perpLengthSq > 0 {
			targetProjectedForward = Vector3.Normalized(perp)
		} else {
			// Extremely rare fallback
			targetProjectedForward = Vector3.XYZ{X: 1}
		}
	}

	// Smoothly interpolate the current forward towards the target forward
	if Vector3.LengthSquared(ar.CurrentForward) == 0 {
		ar.CurrentForward = Vector3.XYZ{Z: 1} // Assume default forward is +Z
	}
	ar.CurrentForward = Vector3.Lerp(ar.CurrentForward, targetProjectedForward, Float.X(12)*delta)
	ar.CurrentForward = Vector3.Normalized(ar.CurrentForward)

	// Use LookAt to set the orientation (assumes model faces +Z locally to fix backwards walking)
	globalPos := model.GlobalPosition()
	target := Vector3.Add(globalPos, ar.CurrentForward)
	model.MoreArgs().LookAt(target, ar.CurrentUp, true)
}
