package musical

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"

	"runtime.link/api/xray"
)

type Connection interface {
	io.Closer

	Send([]byte) error
	Recv() ([]byte, error)
}

type Networking struct {
	Instructions Connection
	MediaUploads Connection
	ErrorReports ErrorReporter
}

type ErrorReporter interface {
	ReportError(error)
}

func (network Networking) send(val encodable, media bool) error {
	packet, err := encode(val)
	if err != nil {
		return xray.New(err)
	}
	if media {
		if err := network.MediaUploads.Send(packet); err != nil {
			return xray.New(err)
		}
	} else {
		if err := network.Instructions.Send(packet); err != nil {
			return xray.New(err)
		}
	}
	return nil
}

func Join(network Networking, userID Unique, replica UsersSpace3D) (UsersSpace3D, error) {
	scene := client{network}
	go scene.handle(replica)
	return scene, nil
}

type client struct {
	Networking
}

func (c client) Member(req Member) error { return c.send(req, false) }
func (c client) Upload(req Upload) error { return c.send(req, true) }
func (c client) Sculpt(req Sculpt) error { return c.send(req, false) }
func (c client) Import(req Import) error { return c.send(req, false) }
func (c client) Change(req Change) error { return c.send(req, false) }
func (c client) Action(req Action) error { return c.send(req, false) }
func (c client) LookAt(req LookAt) error { return c.send(req, false) }

func (c client) handle(replica UsersSpace3D) {
	go func() {
		for {
			packet, err := c.MediaUploads.Recv()
			if err != nil {
				c.ErrorReports.ReportError(xray.New(err))
				return
			}
			req, err := decode(bytes.NewReader(packet))
			if err != nil {
				c.ErrorReports.ReportError(xray.New(err))
				return
			}
			switch v := req.(type) {
			case Upload:
				replica.Upload(v)
			default:
				return
			}
		}
	}()
	for {
		packet, err := c.Instructions.Recv()
		fmt.Println("PACKET")
		if err != nil {
			c.ErrorReports.ReportError(xray.New(err))
			return
		}
		req, err := decode(bytes.NewReader(packet))
		if err != nil {
			c.ErrorReports.ReportError(xray.New(err))
			return
		}
		fmt.Println(req)
		switch v := req.(type) {
		case Member:
			replica.Member(v)
		case Sculpt:
			replica.Sculpt(v)
		case Import:
			replica.Import(v)
		case Change:
			replica.Change(v)
		case Action:
			replica.Action(v)
		case LookAt:
			replica.LookAt(v)
		default:
			return
		}
	}
}

func Host(network iter.Seq[Networking], initial Unique, storage Storage, replica UsersSpace3D, reports ErrorReporter) (UsersSpace3D, chan<- Unique, error) {
	var srv = server{
		initial: initial,
		storage: storage,
		replica: replica,
		clients: make(chan Networking),
		changes: make(chan Unique),
		request: make(chan encodable),
		reports: reports,
	}
	go func() {
		for client := range network {
			srv.clients <- client
		}
		close(srv.clients)
	}()
	go srv.run()
	return channel(srv.request), srv.changes, nil
}

type server struct {
	initial Unique
	storage Storage
	replica UsersSpace3D
	reports ErrorReporter
	clients chan Networking
	changes chan Unique
	request chan encodable
}

func (srv server) run() {
	var assign Author
	var authors = make(map[Author]Member)
	var clients = make(map[Networking]Author)
	var current = srv.initial
	var tracker counter

	store, err := srv.storage.Open(current)
	if err != nil {
		srv.reports.ReportError(xray.New(err))
		return
	}
	defer func() {
		store.Close()
	}()
	mus3, err := newStorage(store, 0, Compose(&tracker, srv.replica))
	if err != nil {
		srv.reports.ReportError(xray.New(err))
		return
	}

	fmt.Println("WAITING FOR CLIENTS")
	for {
		select {
		case client, ok := <-srv.clients:
			fmt.Println("NEW CLIENT")
			if !ok {
				return
			}
			assign++
			orc, ok := authors[assign]
			if !ok {
				orc = Member{
					Record: current,
					Number: tracker.value,
					Author: assign,
					Assign: true,
				}
				authors[assign] = orc
			}
			if err := client.send(orc, false); err == nil {
				clients[client] = assign
				go srv.handle(assign, client, current, tracker.value)
			} else {
				srv.reports.ReportError(xray.New(err))
			}
		case scene, ok := <-srv.changes:
			if !ok {
				return
			}
			current = scene
			tracker.value = 0
			store.Close()
			store, err := srv.storage.Open(current)
			if err != nil {
				srv.reports.ReportError(xray.New(err))
				return
			}
			mus3, err = newStorage(store, 0, Compose(&tracker, srv.replica))
			if err != nil {
				srv.reports.ReportError(xray.New(err))
				return
			}
		case req := <-srv.request:
			switch v := req.(type) {
			case Member:
				mus3.Member(v)
			case Upload:
				mus3.Upload(v)
			case Sculpt:
				mus3.Sculpt(v)
			case Import:
				mus3.Import(v)
			case Change:
				mus3.Change(v)
			case Action:
				mus3.Action(v)
			case LookAt:
				mus3.LookAt(v)
			}
			for client := range clients {
				if err := client.send(req, false); err != nil {
					delete(clients, client)
				}
			}
		}
	}
}

func (srv server) handle(author Author, network Networking, current Unique, catchup uint64) {
	go func() {
		file, err := srv.storage.Open(current)
		if err != nil {
			srv.reports.ReportError(xray.New(err))
			return
		}
		defer file.Close()
		if _, err := newStorage(file, int(catchup), client{network}); err != nil {
			srv.reports.ReportError(xray.New(err))
			return
		}
	}()
	go func() {
		defer network.MediaUploads.Close()
		for {
			packet, err := network.MediaUploads.Recv()
			if err != nil {
				srv.reports.ReportError(xray.New(err))
				return
			}
			req, err := decode(bytes.NewReader(packet))
			if err != nil {
				srv.reports.ReportError(xray.New(err))
				return
			}
			if !req.validateAuthor(author) {
				srv.reports.ReportError(xray.New(errors.New("invalid author for request")))
				continue
			}
			srv.request <- req
		}
	}()
	defer network.Instructions.Close()
	for {
		packet, err := network.Instructions.Recv()
		if err != nil {
			srv.reports.ReportError(xray.New(err))
			return
		}
		req, err := decode(bytes.NewReader(packet))
		if err != nil {
			srv.reports.ReportError(xray.New(err))
			return
		}
		if !req.validateAuthor(author) {
			srv.reports.ReportError(xray.New(errors.New("invalid author for request")))
			continue
		}
		srv.request <- req
	}
}
