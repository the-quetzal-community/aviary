package musical

type Stubbed = stubbed
type stubbed struct{}

func (Stubbed) Member(Member) error { return nil }
func (Stubbed) Upload(Upload) error { return nil }
func (Stubbed) Sculpt(Sculpt) error { return nil }
func (Stubbed) Import(Import) error { return nil }
func (Stubbed) Change(Change) error { return nil }
func (Stubbed) Action(Action) error { return nil }
func (Stubbed) LookAt(LookAt) error { return nil }
