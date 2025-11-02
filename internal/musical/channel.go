package musical

type channel chan<- encodable

func (ch channel) Member(req Member) error {
	ch <- req
	return nil
}

func (ch channel) Upload(req Upload) error {
	ch <- req
	return nil
}

func (ch channel) Sculpt(req Sculpt) error {
	ch <- req
	return nil
}

func (ch channel) Import(req Import) error {
	ch <- req
	return nil
}

func (ch channel) Change(req Change) error {
	ch <- req
	return nil
}

func (ch channel) Action(req Action) error {
	ch <- req
	return nil
}

func (ch channel) LookAt(req LookAt) error {
	ch <- req
	return nil
}
