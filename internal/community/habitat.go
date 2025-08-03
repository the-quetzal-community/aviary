package community

import "sync"

type Habitat struct {
	mutex sync.RWMutex

	Regions []Region
	Designs []Design

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
		InsertRegion: s.InsertRegion,
		InsertDesign: s.InsertDesign,
		InsertObject: s.InsertObject,
		InsertWalker: s.InsertWalker,
		ResizeRegion: s.ResizeRegion,
		UpdateObject: s.UpdateObject,
		UpdateWalker: s.UpdateWalker,
		PrintMessage: func(msg string) {
			s.mutex.RLock()
			defer s.mutex.RUnlock()

			for _, writer := range s.clients {
				writer.PrintMessage(msg)
			}
		},
	}
}

func (s *Habitat) InsertRegion(extents [2]uint8) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Regions = append(s.Regions, Region{Extents: extents})
	for _, writer := range s.clients {
		writer.InsertRegion(extents)
	}
}

func (s *Habitat) InsertDesign(design Design) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Designs = append(s.Designs, design)
	for _, writer := range s.clients {
		writer.InsertDesign(design)
	}
}

func (s *Habitat) InsertObject(region int, object Object) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if region < 0 || region >= len(s.Regions) {
		return
	}
	s.Regions[region].Objects = append(s.Regions[region].Objects, object)
	for _, writer := range s.clients {
		writer.InsertObject(region, object)
	}
}

func (s *Habitat) InsertWalker(region int, walker Walker) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if region < 0 || region >= len(s.Regions) {
		return
	}
	s.Regions[region].Walkers = append(s.Regions[region].Walkers, walker)
	for _, writer := range s.clients {
		writer.InsertWalker(region, walker)
	}
}

func (s *Habitat) ResizeRegion(region int, id int, extents [2]uint8) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if region < 0 || region >= len(s.Regions) || id < 0 || id >= len(s.Regions[region].Objects) {
		return
	}
	s.Regions[region].Extents = extents
	for _, writer := range s.clients {
		writer.ResizeRegion(region, id, extents)
	}
}

func (s *Habitat) UpdateObject(region int, id int, object Object) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if region < 0 || region >= len(s.Regions) || id < 0 || id >= len(s.Regions[region].Objects) {
		return
	}
	s.Regions[region].Objects[id] = object
	for _, writer := range s.clients {
		writer.UpdateObject(region, id, object)
	}
}

func (s *Habitat) UpdateWalker(region int, id int, walker Walker) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if region < 0 || region >= len(s.Regions) || id < 0 || id >= len(s.Regions[region].Walkers) {
		return
	}
	s.Regions[region].Walkers[id] = walker
	for _, writer := range s.clients {
		writer.UpdateWalker(region, id, walker)
	}
}
