package internal

import (
	"context"

	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
	"the.quetzal.community/aviary/internal/netstate"
)

func (ui *CloudControl) loginUpdate() (signalling.User, bool) {
	user, err := ui.client.signalling.LookupUser(context.Background())
	if err != nil {
		switch err.Error() {
		case "Unauthorized", "not found":
			// Not signed in / no account yet: the server answered, so we are online.
			// Both are expected states, reflected in the status indicator below —
			// not errors worth reporting to Sentry on every poll.
			netstate.MarkOnline()
		default:
			raiseUnlessOffline(err)
		}
		ui.on_process <- func(cc *CloudControl) {
			if err.Error() == "Unauthorized" {
				UserState.Aviary = signalling.User{}
			}
			cc.set_online_status_indicator(false)
		}
		return signalling.User{}, false
	}
	netstate.MarkOnline()
	ui.on_process <- func(cc *CloudControl) {
		if UserState.Aviary.ID != user.ID {
			replaceSceneTree(ui.AsNode(), NewClientLoading(musical.WorkID(ui.client.record)))
		}
		UserState.Aviary = user
		cc.client.saveUserState()
		cc.set_online_status_indicator(true)
	}
	return user, true
}
