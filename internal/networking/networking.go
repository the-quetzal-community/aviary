// Package networking provides quetzal community networking, available to users with a paid quetzal community plan.
package networking

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"runtime.link/api/xray"
)

// debug enables verbose signalling/ICE logging. Set AVIARY_NET_DEBUG to any
// non-empty value to trace the handshake (ICE server count, candidate exchange,
// connection-state transitions) when diagnosing connection failures.
var debug = os.Getenv("AVIARY_NET_DEBUG") != ""

type Server func(client Client)

type Client struct {
	Recv <-chan []byte
	Send chan<- []byte
	// Done is closed by the networking layer once the peer disconnects, so the
	// server can stop reading and writing instead of blocking on a dead channel.
	Done <-chan struct{}
}

type Code string

type Connectivity struct {
	ice       []webrtc.ICEServer
	community *websocket.Conn

	peer               *webrtc.PeerConnection
	data_channel       *webrtc.DataChannel
	data_channel_ready sync.WaitGroup

	local_recv chan<- []byte
	server     Server

	closed chan struct{} // closed when a session joined via Join disconnects

	Raise func(error)
	Print func(string, ...any)

	Authentication string
}

type iceMessageType string

const (
	iceMessageTypeOffer     iceMessageType = "offer"
	iceMessageTypeAnswer    iceMessageType = "answer"
	iceMessageTypeCandidate iceMessageType = "candidate"
	iceMessageTypeError     iceMessageType = "error"
	iceMessageTypeCode      iceMessageType = "code"
)

type iceMessage struct {
	Type      string                    `json:"type"`
	SessionID string                    `json:"sessionId,omitempty"`
	SDP       webrtc.SessionDescription `json:"sdp,omitzero"`
	Candidate webrtc.ICECandidateInit   `json:"candidate,omitzero"`
	Code      Code                      `json:"code,omitempty"`
	Message   string                    `json:"message,omitempty"`
}

func wsRecv[T any](sock *websocket.Conn) (T, error) {
	mtype, message, err := sock.ReadMessage()
	if err != nil {
		return [1]T{}[0], xray.New(err)
	}
	if mtype != websocket.TextMessage {
		return [1]T{}[0], xray.New(errors.New("unexpected websocket message type"))
	}
	var data T
	if err := json.Unmarshal(message, &data); err != nil {
		return [1]T{}[0], xray.New(err)
	}
	return data, nil
}

func wsSend(sock *websocket.Conn, data any) error {
	message, err := json.Marshal(data)
	if err != nil {
		return xray.New(err)
	}
	if err := sock.WriteMessage(websocket.TextMessage, message); err != nil {
		return xray.New(err)
	}
	return nil
}

// setup basic ICE connectivity common to both clients and servers - this uses the quetzal community ICE servers.
func (c *Connectivity) setup() (err error) {
	c.data_channel_ready.Add(1)
	if debug {
		c.Print("Setting up connectivity...\n")
		defer c.Print("Connectivity setup complete.\n")
	}
	c.community, _, err = websocket.DefaultDialer.Dial("wss://via.quetzal.community/connection", http.Header{
		"Authorization": []string{"Bearer " + c.Authentication},
	})
	if err != nil {
		return xray.New(err)
	}
	ice, err := wsRecv[struct {
		Servers []webrtc.ICEServer `json:"data"`
	}](c.community)
	if err != nil {
		return xray.New(err)
	}
	c.ice = ice.Servers
	if debug {
		c.Print("Received %d ICE server(s) from the signalling service.\n", len(c.ice))
		for _, s := range c.ice {
			c.Print("  ICE server: %v\n", s.URLs)
		}
	}
	if len(c.ice) == 0 {
		// No STUN/TURN means ICE can only try host candidates, which fails across
		// any NAT or between IPv4/IPv6-only peers — the connection then sits in
		// "connecting" until it times out to "failed". Surface it rather than let
		// the caller see only a downstream "connection closed".
		return xray.New(errors.New("signalling service returned no ICE servers (STUN/TURN); cannot traverse NAT"))
	}
	return nil
}

func (c *Connectivity) Send(data []byte) {
	if debug {
		c.Print("Waiting for data channel to be ready...\n")
	}
	c.data_channel_ready.Wait()
	if debug {
		c.Print("Sending data on data channel\n")
	}
	c.data_channel.Send(data)
	if debug {
		c.Print("Data sent.\n")
	}
}

// Done is closed when a session joined via Join disconnects. It is nil before
// Join is called (and on the host side), so selecting on it simply blocks.
func (c *Connectivity) Done() <-chan struct{} { return c.closed }

func (c *Connectivity) Join(code Code, updates chan<- []byte) error {
	if err := c.setup(); err != nil {
		return xray.New(err)
	}
	signalling, _, err := websocket.DefaultDialer.Dial("wss://via.quetzal.community/code/"+string(code), http.Header{
		"Authorization": []string{"Bearer " + c.Authentication},
	})
	if err != nil {
		return xray.New(err)
	}
	message, err := wsRecv[iceMessage](signalling)
	if err != nil {
		return xray.New(err)
	}
	c.peer, err = webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: c.ice,
	})
	if err != nil {
		return xray.New(err)
	}
	c.closed = make(chan struct{})
	var closeOnce sync.Once
	var sessionDown atomic.Bool
	// closeSession tears the joined session down exactly once, signalling the
	// server (via c.closed) so its Recv/Send unblock, and releasing the peer and
	// signalling socket. Safe to call from the data-channel and connection-state
	// callbacks, which may race on an abrupt disconnect. sessionDown lets the
	// signalling read loop tell our own close (a benign "use of closed network
	// connection") apart from a real signalling error, so the join surfaces the
	// genuine failure reason rather than that symptom.
	closeSession := func() {
		closeOnce.Do(func() {
			sessionDown.Store(true)
			close(c.closed)
			signalling.Close()
			go c.peer.Close()
		})
	}
	var issues = make(chan error, 2)
	// mutex serialises every write to the signalling socket. Gorilla allows only
	// one concurrent writer, but the trickle-ICE candidate callback (below) and
	// the answer send race otherwise — a real data race that corrupts the
	// websocket framing and silently breaks the handshake.
	var mutex sync.Mutex
	c.peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		mutex.Lock()
		defer mutex.Unlock()
		if candidate == nil {
			return // no more candidates
		}
		if debug {
			c.Print("ICE candidate: %s\n", candidate.ToJSON().Candidate)
		}
		msg := iceMessage{
			Type:      string(iceMessageTypeCandidate),
			SessionID: message.SessionID,
			Candidate: candidate.ToJSON(),
		}
		data, err := json.Marshal(msg)
		if err != nil {
			issues <- xray.New(err)
			return
		}
		if err := signalling.WriteMessage(websocket.TextMessage, data); err != nil {
			issues <- xray.New(err)
			return
		}
	})
	var data_channels = make(chan *webrtc.DataChannel, 1)
	c.peer.OnDataChannel(func(ch *webrtc.DataChannel) {
		if debug {
			c.Print("Data channel opened: %s\n", ch.Label())
		}
		data_channels <- ch
		ch.OnMessage(func(msg webrtc.DataChannelMessage) {
			if debug {
				c.Print("Received message on data channel: %s\n", string(msg.Data))
			}
			select {
			case updates <- msg.Data:
			case <-c.closed:
			}
		})
		ch.OnClose(func() {
			closeSession()
		})
	})
	c.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if debug {
			c.Print("Connection state changed: %s\n", state.String())
		}
		switch state {
		case webrtc.PeerConnectionStateFailed:
			// Push the real reason before tearing down, so the join reports the ICE
			// failure rather than the closed-socket read error it triggers.
			select {
			case issues <- fmt.Errorf("peer connection failed: ICE could not establish a path to the host (NAT/TURN); %d ICE server(s) configured", len(c.ice)):
			default:
			}
			closeSession()
		case webrtc.PeerConnectionStateClosed:
			closeSession()
		}
	})
	if err := c.peer.SetRemoteDescription(message.SDP); err != nil {
		return xray.New(err)
	}
	answer, err := c.peer.CreateAnswer(nil)
	if err != nil {
		return xray.New(err)
	}
	if err := c.peer.SetLocalDescription(answer); err != nil {
		return xray.New(err)
	}
	go func() {
		for {
			msg, err := wsRecv[iceMessage](signalling)
			if err != nil {
				if errors.Is(err, websocket.ErrCloseSent) || sessionDown.Load() {
					// Our own closeSession shut the socket; the real reason (ICE
					// failure / data-channel close) is already queued on issues.
					return
				}
				issues <- xray.New(err)
				return
			}
			if debug {
				c.Print("Received message: %s\n", msg.Type)
			}
			switch msg.Type {
			case string(iceMessageTypeCandidate):
				if err := c.peer.AddICECandidate(msg.Candidate); err != nil {
					c.Raise(xray.New(err))
				}
			case string(iceMessageTypeError):
				issues <- fmt.Errorf("error from signalling server: %s", msg.Message)
				return
			default:
				issues <- fmt.Errorf("unexpected message type: %s", msg.Type)
				return
			}
		}
	}()
	mutex.Lock()
	err = wsSend(signalling, iceMessage{
		Type:      string(iceMessageTypeAnswer),
		SessionID: message.SessionID,
		SDP:       *c.peer.LocalDescription(),
	})
	mutex.Unlock()
	if err != nil {
		return xray.New(err)
	}
	for {
		select {
		case ch := <-data_channels:
			c.data_channel = ch
			c.data_channel_ready.Done()
			return nil
		case err := <-issues:
			if err != nil {
				return err
			}
		}
	}
}

// offerTimeout bounds how long a parked offer sits before its slot is recycled,
// so a host that nobody joins keeps refreshing its offer rather than wedging on
// one stale one. offerGrace is how long the half-open peer is kept alive past
// that, so an answer already in flight (the joiner grabbed the offer just before
// it expired) still completes instead of being dropped.
//
// Crucially, the timer only ever reclaims an *idle* offer — one no joiner has
// engaged. The moment a joiner sends its first candidate or answer (see engage),
// the timer is cancelled and that peer's lifetime is governed by its
// connection-state teardown instead. This is what fixes the join that "times out
// after a while": previously the timer could fire mid-handshake (e.g. when a
// long-idle host's offer had aged near the timeout just as someone joined and
// the scene was still loading), delete the session, and strand the answer.
const (
	offerTimeout = 30 * time.Second
	offerGrace   = 10 * time.Second
)

func (c *Connectivity) addPeers(sock *websocket.Conn) {
	type peerState struct {
		conn     *webrtc.PeerConnection
		timer    *time.Timer
		resolved bool // the offer slot has already been handed back for this session
		engaged  bool // a joiner has sent a candidate/answer — don't time this peer out
	}
	var mutex sync.Mutex
	var pending = make(map[string]*peerState)
	var make_offer = make(chan struct{}, 1)
	var stop = make(chan struct{})

	// sendSig serialises writes to the signalling socket. Gorilla allows only one
	// concurrent writer, but the offer send and every per-peer ICE-candidate
	// callback (one set per joiner) all write to sock — without this they race,
	// corrupting the framing and breaking the handshake for everyone.
	var writeMu sync.Mutex
	sendSig := func(msg iceMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsSend(sock, msg)
	}
	make_offer <- struct{}{}

	// returnToken hands the single offer slot back so the next peer can be
	// offered. Non-blocking, and safe after shutdown (make_offer is never closed).
	returnToken := func() {
		select {
		case make_offer <- struct{}{}:
		default:
		}
	}
	// claim resolves the offer for a session exactly once. The winning caller
	// owns returning the offer slot; later callers (a disconnect after the answer
	// already arrived, or a timeout that lost the race to an answer) get false
	// and leave the slot alone.
	claim := func(sessionID string) bool {
		mutex.Lock()
		defer mutex.Unlock()
		ps, ok := pending[sessionID]
		if !ok || ps.resolved {
			return false
		}
		ps.resolved = true
		if ps.timer != nil {
			ps.timer.Stop()
		}
		return true
	}
	// engage marks that a joiner has begun the handshake for a session (its first
	// candidate or answer arrived) and cancels the offer/grace timer, so the timer
	// can never reclaim or close a peer that's mid-handshake. An engaged peer is
	// torn down only by its connection-state callback (Failed/Closed). Independent
	// of resolved: the slot may already have been recycled while the answer was in
	// flight, but the peer must still be kept and completed.
	engage := func(sessionID string) {
		mutex.Lock()
		defer mutex.Unlock()
		ps, ok := pending[sessionID]
		if !ok || ps.engaged {
			return
		}
		ps.engaged = true
		if ps.timer != nil {
			ps.timer.Stop()
		}
	}

	go func() {
		defer close(stop)
		for {
			msg, err := wsRecv[iceMessage](sock)
			if err != nil {
				if !errors.Is(err, websocket.ErrCloseSent) {
					c.Raise(xray.New(err))
				}
				return
			}
			if debug {
				c.Print("Received message: %s\n", msg.Type)
			}
			switch msg.Type {
			case string(iceMessageTypeAnswer):
				engage(msg.SessionID) // a joiner is handshaking; don't let the timer reclaim it
				mutex.Lock()
				ps, ok := pending[msg.SessionID]
				mutex.Unlock()
				if !ok {
					c.Raise(fmt.Errorf("received answer for unknown session ID: %s", msg.SessionID))
					continue
				}
				if err := ps.conn.SetRemoteDescription(msg.SDP); err != nil {
					c.Raise(xray.New(err))
					continue
				}
				if claim(msg.SessionID) {
					returnToken() // a peer claimed the offer; allow the next one
				}
			case string(iceMessageTypeCandidate):
				engage(msg.SessionID) // a joiner is handshaking; don't let the timer reclaim it
				mutex.Lock()
				ps, ok := pending[msg.SessionID]
				mutex.Unlock()
				if !ok {
					c.Raise(fmt.Errorf("received candidate for unknown session ID: %s", msg.SessionID))
					continue
				}
				if err := ps.conn.AddICECandidate(msg.Candidate); err != nil {
					c.Raise(xray.New(err))
					continue
				}
			default:
				c.Raise(fmt.Errorf("unexpected message type: %s", msg.Type))
			}
		}
	}()

	for {
		select {
		case <-stop:
			return // signalling socket closed; existing peers keep running
		case <-make_offer:
		}

		peer, err := webrtc.NewPeerConnection(webrtc.Configuration{
			ICEServers: c.ice,
		})
		if err != nil {
			c.Raise(err)
			time.Sleep(time.Second)
			returnToken()
			continue
		}
		sessionID := uuid.NewString()
		client_recv := make(chan []byte, 1)
		client_send := make(chan []byte, 1)
		done := make(chan struct{})
		var closeOnce sync.Once

		// cleanup tears the peer down exactly once: it signals the server (via
		// done) so its Recv/Send unblock, drops the routing entry, and releases
		// the connection. teardown additionally reclaims the offer slot when the
		// peer dies before it ever answered.
		cleanup := func() {
			closeOnce.Do(func() {
				mutex.Lock()
				delete(pending, sessionID)
				mutex.Unlock()
				close(done)
				go peer.Close()
			})
		}
		teardown := func() {
			if claim(sessionID) {
				returnToken()
			}
			cleanup()
		}

		ch, err := peer.CreateDataChannel("data", nil)
		if err != nil {
			c.Raise(xray.New(err))
			go peer.Close()
			returnToken()
			time.Sleep(time.Second)
			continue
		}
		ch.OnMessage(func(msg webrtc.DataChannelMessage) {
			if debug {
				c.Print("Received message on data channel")
			}
			select {
			case client_recv <- msg.Data:
			case <-done:
			}
		})
		ch.OnClose(func() {
			if debug {
				c.Print("Data channel closed.\n")
			}
			teardown()
		})
		offer, err := peer.CreateOffer(nil)
		if err != nil {
			c.Raise(xray.New(err))
			go peer.Close()
			returnToken()
			time.Sleep(time.Second)
			continue
		}
		ps := &peerState{conn: peer}
		mutex.Lock()
		pending[sessionID] = ps
		mutex.Unlock()
		if err := sendSig(iceMessage{
			Type:      string(iceMessageTypeOffer),
			SessionID: sessionID,
			SDP:       offer,
		}); err != nil {
			c.Raise(xray.New(err))
			teardown()
			time.Sleep(time.Second)
			continue
		}
		peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
			if debug {
				if candidate != nil {
					c.Print("ICE candidate: %s\n", candidate.ToJSON().Candidate)
				}
			}
			if candidate == nil {
				return // no more candidates
			}
			msg := iceMessage{
				Type:      string(iceMessageTypeCandidate),
				SessionID: sessionID,
				Candidate: candidate.ToJSON(),
			}
			if err := sendSig(msg); err != nil {
				c.Raise(xray.New(err))
			}
		})
		ch.OnOpen(func() {
			go func() {
				for {
					select {
					case msg, ok := <-client_send:
						if !ok {
							return
						}
						if err := ch.Send(msg); err != nil {
							c.Raise(xray.New(err))
							return
						}
					case <-done:
						return
					}
				}
			}()
			go c.server(Client{
				Recv: client_recv,
				Send: client_send,
				Done: done,
			})
		})
		peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			if debug {
				c.Print("Peer connection state changed: %s\n", state.String())
			}
			switch state {
			case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
				teardown()
			}
		})
		mutex.Lock()
		if !ps.resolved && !ps.engaged {
			ps.timer = time.AfterFunc(offerTimeout, func() {
				mutex.Lock()
				if ps.resolved || ps.engaged {
					mutex.Unlock() // a joiner already claimed/engaged this offer
					return
				}
				// Idle offer: free the slot now so new joiners can be offered, but
				// keep the peer for offerGrace so an answer racing the timeout still
				// lands (engage will cancel this grace timer). Only if still nobody
				// has engaged after the grace do we close the abandoned peer.
				ps.resolved = true
				ps.timer = time.AfterFunc(offerGrace, func() {
					mutex.Lock()
					engaged := ps.engaged
					mutex.Unlock()
					if engaged {
						return // a late answer arrived; teardown owns this peer now
					}
					if debug {
						c.Print("Offer %s expired with no joiner; reclaiming.\n", sessionID)
					}
					cleanup()
				})
				mutex.Unlock()
				returnToken()
			})
		}
		mutex.Unlock()
		if err := peer.SetLocalDescription(offer); err != nil {
			c.Raise(xray.New(err))
			teardown()
			time.Sleep(time.Second)
			continue
		}
	}
}

func (c *Connectivity) Host(updates chan<- []byte, server Server) (Code, error) {
	c.server = server
	if err := c.setup(); err != nil {
		return "", err
	}
	signalling, _, err := websocket.DefaultDialer.Dial("wss://via.quetzal.community/code", http.Header{
		"Authorization": []string{"Bearer " + c.Authentication},
	})
	if err != nil {
		return "", xray.New(err)
	}
	msg, err := wsRecv[iceMessage](signalling)
	if err != nil {
		return "", xray.New(err)
	}
	if msg.Type != string(iceMessageTypeCode) {
		return "", fmt.Errorf("unexpected message type: %s", msg.Type)
	}
	go c.addPeers(signalling)
	return msg.Code, nil
}
