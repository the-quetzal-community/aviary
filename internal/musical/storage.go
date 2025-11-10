package musical

import (
	"errors"
	"io"
	"io/fs"
	"math"

	"runtime.link/api/xray"
)

type Storage interface {
	Open(WorkID) (fs.File, error)
}

const MagicHeader = "the.quetzal.community/musical.Users3DScene@v0.1"

// Storage implements [UsersSpace3D] via a [io.ReadWriteSeeker].
// This would typically be a .mu3s file on the local filesystem.
//
// Instructions that are loaded from the [io.ReadWriteSeeker] or
// called on the returned [UsersSpace3D] are also broadcast to the
// client (so that the scene can be rendered).
//
// Note: only instructions with their 'Commit' field set to true
// will be written to the [io.ReadWriteSeeker].
func newStorage(mus3 fs.File, limit int, client UsersSpace3D) (UsersSpace3D, error) {
	var store = storage{reader: mus3, client: client}
	w, writable := mus3.(io.Writer)
	if writable {
		store.writer = w
	}
	store.limits.entity = make(map[Author]uint16)
	store.limits.design = make(map[Author]uint16)

	stat, err := mus3.Stat()
	if err != nil {
		return nil, xray.New(err)
	}

	var header [len(MagicHeader)]byte
	if _, err := io.ReadFull(store.reader, header[:]); err != nil && !errors.Is(err, io.EOF) {
		return nil, xray.New(err)
	} else if err == nil {
		if string(header[:]) != MagicHeader {
			return nil, xray.New(errors.New("invalid musical.Users3DScene file"))
		}
	}
	n, err := store.decode(limit)
	if err != nil {
		return nil, xray.New(err)
	}
	if stat.Size() == 0 && n == 0 && writable {
		if _, err := w.Write([]byte(MagicHeader)); err != nil {
			return nil, xray.New(err)
		}
	}
	return store, nil
}

type storage struct {
	reader io.Reader
	writer io.Writer
	client UsersSpace3D

	limits struct {
		entity map[Author]uint16
		design map[Author]uint16
	}
}

func (mus3 storage) Member(req Member) error {
	if req.Assign {
		return nil
	}
	mus3.client.Member(req)
	buf, err := encode(req)
	if err != nil {
		return xray.New(err)
	}
	if _, err := mus3.writer.Write(buf); err != nil {
		return xray.New(err)
	}
	return nil
}

func (mus3 storage) Upload(file Upload) error {
	if mus3.limits.design[file.Design.Author] < uint16(file.Design.Number) {
		return nil
	}
	stat, err := file.Upload.Stat()
	if err != nil {
		return xray.New(err)
	}
	name := stat.Name()
	if len(name) > math.MaxUint16 {
		return xray.New(errors.New("file name too long"))
	}
	mus3.client.Upload(file)
	buf, err := encode(file)
	if err != nil {
		return xray.New(err)
	}
	if _, err := mus3.writer.Write(buf); err != nil {
		return xray.New(err)
	}
	return nil
}

func (mus3 storage) Sculpt(brush Sculpt) error {
	mus3.client.Sculpt(brush)
	if !brush.Commit {
		return nil
	}
	buf, err := encode(brush)
	if err != nil {
		return xray.New(err)
	}
	if _, err := mus3.writer.Write(buf); err != nil {
		return xray.New(err)
	}
	return nil
}

func (mus3 storage) Import(uri Import) error {
	if len(uri.Import) > math.MaxUint16 {
		return xray.New(errors.New("import URI too long"))
	}
	mus3.client.Import(uri)
	buf, err := encode(uri)
	if err != nil {
		return xray.New(err)
	}
	if _, err := mus3.writer.Write(buf); err != nil {
		return xray.New(err)
	}
	return nil
}

func (mus3 storage) Change(con Change) error {
	mus3.client.Change(con)
	if !con.Commit {
		return nil
	}
	buf, err := encode(con)
	if err != nil {
		return xray.New(err)
	}
	if _, err := mus3.writer.Write(buf); err != nil {
		return xray.New(err)
	}
	return nil
}

func (mus3 storage) Action(rel Action) error {
	mus3.client.Action(rel)
	if !rel.Commit {
		return nil
	}
	buf, err := encode(rel)
	if err != nil {
		return xray.New(err)
	}
	if _, err := mus3.writer.Write(buf); err != nil {
		return xray.New(err)
	}
	return nil
}

func (mus3 storage) LookAt(view LookAt) error {
	mus3.client.LookAt(view)
	return nil
}

func (mus3 storage) decode(limit int) (int, error) {
	var n int
	for n = 0; limit == 0 || n < limit; n++ {
		packet, err := decode(mus3.reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, xray.New(err)
		}
		switch packet := packet.(type) {
		case Member:
			mus3.client.Member(packet)
		case Upload:
			mus3.client.Upload(packet)
		case Sculpt:
			mus3.client.Sculpt(packet)
		case Import:
			mus3.client.Import(packet)
		case Change:
			mus3.client.Change(packet)
		case Action:
			mus3.client.Action(packet)
		case LookAt:
			return n, xray.New(errors.New("unexpected LookAt entry in storage"))
		default:
			return n, xray.New(errors.New("unknown entry type"))
		}
	}
	return n, nil
}
