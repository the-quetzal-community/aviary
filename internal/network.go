package internal

import (
	"net/http"
	"time"

	"graphics.gd/classdb/Engine"
	"the.quetzal.community/aviary/internal/netstate"
)

// raiseUnlessOffline reports err unless it is an expected no-connectivity
// failure, in which case it only flips [netstate] offline (the red indicators
// in the downloader and the cloud control) — being offline is a normal state,
// not a Sentry event. Returns true if err was an offline failure.
func raiseUnlessOffline(err error) bool {
	if netstate.Check(err) {
		return true
	}
	Engine.Raise(err)
	return false
}

// onlineProbeInterval is how often, while offline, we check whether the
// internet has come back.
const onlineProbeInterval = 5 * time.Second

var probeClient = &http.Client{Timeout: 5 * time.Second}

// StartOnlineProbe watches for connectivity to return while [netstate] is
// offline, so the indicators clear and downloads resume without a restart.
func StartOnlineProbe() {
	go netstate.Watch(ShuttingDown, onlineProbeInterval, func() error {
		// Any HTTP answer at all means the host is reachable again.
		resp, err := probeClient.Head("https://vpk.quetzal.community/")
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	})
}
