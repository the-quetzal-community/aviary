package internal

import (
	"context"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/SceneTree"
	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
)

func (ui *CloudControl) loginUpdate() (signalling.User, bool) {
	user, err := ui.client.signalling.LookupUser(context.Background())
	if err != nil {
		Engine.Raise(err)
		ui.on_process <- func(cc *CloudControl) {
			if err.Error() == "Unauthorized" {
				UserState.Aviary = signalling.User{}
			}
			cc.set_online_status_indicator(false)
		}
		return signalling.User{}, false
	}
	ui.on_process <- func(cc *CloudControl) {
		if UserState.Aviary.ID != user.ID {
			fresh := NewClientLoading(musical.WorkID(ui.client.record))
			for _, child := range SceneTree.Get(ui.AsNode()).Root().AsNode().GetChildren() {
				child.QueueFree()
			}
			SceneTree.Add(fresh)
		}
		UserState.Aviary = user
		cc.client.saveUserState()
		cc.set_online_status_indicator(true)
	}
	return user, true
}
