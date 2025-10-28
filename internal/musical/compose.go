package musical

import "errors"

func Compose(scenes ...UsersSpace3D) UsersSpace3D {
	return composition(scenes)
}

type composition []UsersSpace3D

func (c composition) Member(req Orchestrator) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Member(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Upload(req DesignUpload) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Upload(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Sculpt(req AreaToSculpt) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Sculpt(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Import(req DesignImport) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Import(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Create(req Contribution) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Create(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) Attach(req Relationship) error {
	var errs []error
	for _, scene := range c {
		if err := scene.Attach(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c composition) LookAt(req BirdsEyeView) error {
	var errs []error
	for _, scene := range c {
		if err := scene.LookAt(req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
