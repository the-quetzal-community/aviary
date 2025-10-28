package musical

type counter struct {
	value uint64
}

func (c *counter) Member(req Orchestrator) error {
	if !req.Assign {
		c.value++
	}
	return nil
}

func (c *counter) Upload(req DesignUpload) error {
	c.value++
	return nil
}

func (c *counter) Sculpt(req AreaToSculpt) error {
	if req.Commit {
		c.value++
	}
	return nil
}

func (c *counter) Import(req DesignImport) error {
	c.value++
	return nil
}

func (c *counter) Create(req Contribution) error {
	if req.Commit {
		c.value++
	}
	return nil
}

func (c *counter) Attach(req Relationship) error {
	if req.Commit {
		c.value++
	}
	return nil
}

func (c *counter) LookAt(req BirdsEyeView) error {
	return nil
}
