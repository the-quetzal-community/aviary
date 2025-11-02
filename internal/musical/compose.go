package musical

import "errors"

func Compose(scenes ...UsersSpace3D) UsersSpace3D {
	return composition(scenes)
}

type composition []UsersSpace3D

func (c composition) Member(req Member) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Member(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Upload(req Upload) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Upload(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Sculpt(req Sculpt) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Sculpt(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Import(req Import) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Import(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Change(req Change) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Change(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Action(req Action) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Action(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) LookAt(req LookAt) error {
	var errs []error
	for _, scene := range c {
		if err := scene.LookAt(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
