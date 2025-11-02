package internal

import (
	"sort"

	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
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
}

func (ar *ActionRenderer) Ready() {
	ar.playing = "Idle"
	ar.AsNode().SetProcess(false)
}

func (ar *ActionRenderer) Add(action musical.Action) {
	ar.actions = append(ar.actions, action)
	sort.Slice(ar.actions, func(i, j int) bool {
		return ar.actions[i].Timing < ar.actions[j].Timing
	})
	ar.AsNode().SetProcess(true)
}

func (ar *ActionRenderer) play(name string) {
	if ar.playing == name {
		return
	}
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	player := Object.To[AnimationPlayer.Instance](parent.AsNode().GetNode("AnimationPlayer"))
	player.AsAnimationMixer().GetAnimation(name).SetLoopMode(Animation.LoopLinear)
	player.PlayNamed(name)
	ar.playing = name
}

func (ar *ActionRenderer) Process(delta Float.X) {
	action := ar.actions[ar.current]
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	for ar.client.time.Now()-action.Timing >= musical.Timing(action.Period) {
		parent.SetPosition(action.Target)
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
	// angle between initial and target
	parent.SetRotation(Euler.Radians{
		Y: Angle.Atan2(
			action.Target.X-ar.Initial.X,
			action.Target.Z-ar.Initial.Z,
		),
	})
	parent.SetPosition(Vector3.Lerp(ar.Initial, action.Target, Float.X(ar.client.time.Now()-action.Timing)/Float.X(action.Period)))
}
