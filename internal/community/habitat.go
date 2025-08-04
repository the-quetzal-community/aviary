package community

import (
	"sync"

	"runtime.link/xyz"
)

type Habitat struct {
	mutex sync.RWMutex

	Objects map[xyz.Pair[string, Object]]Object

	clients []*Log
}

func (s *Habitat) AddClient(client *Log) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.clients = append(s.clients, client)
}

func (s *Habitat) DelClient(client *Log) {
	for i, c := range s.clients {
		if c == client {
			s.mutex.Lock()
			defer s.mutex.Unlock()

			s.clients = append(s.clients[:i], s.clients[i+1:]...)
			return
		}
	}
}

func (s *Habitat) Log() *Log {
	return &Log{
		InsertObject: func(design string, initial Object) {
			s.mutex.Lock()
			defer s.mutex.Unlock()
			if s.Objects == nil {
				s.Objects = make(map[xyz.Pair[string, Object]]Object)
			}
			s.Objects[xyz.NewPair(design, initial)] = Object{}
			for _, writer := range s.clients {
				writer.InsertObject(design, initial)
			}
		},
		UpdateObject: func(design string, initial Object, deltas Object) {
			s.mutex.Lock()
			defer s.mutex.Unlock()
			if s.Objects == nil {
				s.Objects = make(map[xyz.Pair[string, Object]]Object)
			}
			existing := s.Objects[xyz.NewPair(design, initial)]
			existing.apply(deltas)
			s.Objects[xyz.NewPair(design, initial)] = existing
		},
	}
}
