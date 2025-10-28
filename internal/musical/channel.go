package musical

type channel chan<- encodable

func (ch channel) Member(req Orchestrator) error {
	ch <- req
	return nil
}

func (ch channel) Upload(req DesignUpload) error {
	ch <- req
	return nil
}

func (ch channel) Sculpt(req AreaToSculpt) error {
	ch <- req
	return nil
}

func (ch channel) Import(req DesignImport) error {
	ch <- req
	return nil
}

func (ch channel) Create(req Contribution) error {
	ch <- req
	return nil
}

func (ch channel) Attach(req Relationship) error {
	ch <- req
	return nil
}

func (ch channel) LookAt(req BirdsEyeView) error {
	ch <- req
	return nil
}
