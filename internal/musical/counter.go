package musical

type counter struct {
	value uint64
}

func (c *counter) Member(req Member) error {
	if !req.Assign {
		c.value++
	}
	return nil
}

func (c *counter) Upload(req Upload) error {
	c.value++
	return nil
}

func (c *counter) Sculpt(req Sculpt) error {
	if req.Commit {
		c.value++
	}
	return nil
}

func (c *counter) Import(req Import) error {
	c.value++
	return nil
}

func (c *counter) Change(req Change) error {
	if req.Commit {
		c.value++
	}
	return nil
}

func (c *counter) Action(req Action) error {
	if req.Commit {
		c.value++
	}
	return nil
}

func (c *counter) LookAt(req LookAt) error {
	return nil
}
