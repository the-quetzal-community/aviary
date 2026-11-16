package netstate

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestIsOffline(t *testing.T) {
	// A listener closed straight away gives a port that refuses connections.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, refused := http.Get("http://" + addr + "/")
	if refused == nil {
		t.Fatal("expected a refused connection")
	}

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"refused dial", refused, true},
		{"wrapped refused dial", fmt.Errorf("probe: %w", refused), true},
		{"dns", &net.DNSError{Err: "no such host", Name: "vpk.quetzal.community"}, true},
		{"flattened dial timeout", errors.New(`Get "https://vpk.quetzal.community/preview.pck": dial tcp 104.21.37.25:443: i/o timeout`), true},
		{"velopack reqwest", errors.New("error sending request for url (https://vpk.quetzal.community/releases.linux.json)"), true},
		{"signalling 404", errors.New("not found"), false},
		{"signalling 401", errors.New("Unauthorized"), false},
		{"bad status", errors.New("range probe failed: status 500"), false},
		{"eof", errors.New("EOF"), false},
	} {
		if got := IsOffline(tc.err); got != tc.want {
			t.Errorf("%s: IsOffline(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestTransitions(t *testing.T) {
	MarkOnline()
	gen := Generation()
	if Check(errors.New("not found")) || !Online() || Generation() != gen {
		t.Fatal("a server error must not mark the state offline")
	}
	if !Check(errors.New("dial tcp: i/o timeout")) || Online() || Generation() != gen+1 {
		t.Fatal("a dial timeout must mark the state offline once")
	}
	MarkOffline()
	if Generation() != gen+1 {
		t.Fatal("repeating the same state must not bump the generation")
	}
	MarkOnline()
	if !Online() || Generation() != gen+2 {
		t.Fatal("MarkOnline must restore the state")
	}
}

func TestWatchRecovers(t *testing.T) {
	MarkOffline()
	stop := make(chan struct{})
	defer close(stop)
	fails := 2
	go Watch(stop, time.Millisecond, func() error {
		if fails > 0 {
			fails--
			return errors.New("dial tcp: i/o timeout")
		}
		return nil
	})
	deadline := time.Now().Add(2 * time.Second)
	for !Online() {
		if time.Now().After(deadline) {
			t.Fatal("Watch did not mark the state online after a successful probe")
		}
		time.Sleep(time.Millisecond)
	}
}
