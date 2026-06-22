package pck

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"runtime.link/api/xray"
)

type Flag uint32

const (
	FlagMissing Flag = 1 << 31
)

type File struct {
	Head int64

	Seek int64
	Size int64
	Hash [16]byte
	Flag Flag
}

func (f File) Bytes(src io.ReadWriteSeeker) ([]byte, error) {
	if f.Missing() {
		return nil, fmt.Errorf("cannot read missing file at seek %d", f.Seek)
	}
	if _, err := src.Seek(f.Seek, io.SeekStart); err != nil {
		return nil, xray.New(err)
	}
	buf := make([]byte, f.Size)
	if _, err := io.ReadFull(src, buf); err != nil {
		return nil, xray.New(err)
	}
	return buf, nil
}

func (f File) Missing() bool {
	return f.Flag&FlagMissing != 0
}

func (f File) SetMissing(missing bool, dst io.WriteSeeker) error {
	if _, err := dst.Seek(f.Head, io.SeekStart); err != nil {
		return xray.New(err)
	}
	var val uint32
	if missing {
		val = uint32(FlagMissing)
	}
	if err := binary.Write(dst, binary.LittleEndian, val); err != nil {
		return xray.New(err)
	}
	return nil
}

// Index reads the pck from the given ReadCloser and returns a map of its files
// indexed by their path.
func Index(pck io.ReadSeeker) (map[string]File, error) {
	var magic uint32
	if err := binary.Read(pck, binary.LittleEndian, &magic); err != nil {
		return nil, xray.New(err)
	}
	if magic != 0x43504447 { // 'PCKC'
		return nil, xray.New(errors.New("invalid pck file: bad magic"))
	}
	var (
		version       uint32
		version_major uint32
		version_minor uint32
		version_patch uint32
	)
	if err := errors.Join(
		binary.Read(pck, binary.LittleEndian, &version),
		binary.Read(pck, binary.LittleEndian, &version_major),
		binary.Read(pck, binary.LittleEndian, &version_minor),
		binary.Read(pck, binary.LittleEndian, &version_patch),
	); err != nil {
		return nil, xray.New(err)
	}
	if version != 3 {
		return nil, xray.New(errors.New("invalid pck file: unsupported version"))
	}
	var (
		pack_flags uint32
		file_base  int64
		dir_offset int64
	)
	if err := errors.Join(
		binary.Read(pck, binary.LittleEndian, &pack_flags),
		binary.Read(pck, binary.LittleEndian, &file_base),
		binary.Read(pck, binary.LittleEndian, &dir_offset),
	); err != nil {
		return nil, xray.New(err)
	}
	if _, err := pck.Seek(dir_offset, io.SeekStart); err != nil {
		return nil, xray.New(err)
	}
	var file_count uint32

	if err := binary.Read(pck, binary.LittleEndian, &file_count); err != nil {
		return nil, xray.New(err)
	}
	files := make(map[string]File)
	head := dir_offset + 4
	for i := uint32(0); i < file_count; i++ {
		var name_len uint32
		if err := binary.Read(pck, binary.LittleEndian, &name_len); err != nil {
			return nil, xray.New(err)
		}
		name_buf := make([]byte, name_len)
		if _, err := io.ReadFull(pck, name_buf); err != nil {
			return nil, xray.New(err)
		}
		for j := 0; j < len(name_buf); j++ {
			if name_buf[j] == 0 {
				name_buf = name_buf[:j]
				break
			}
		}
		var f File
		if err := errors.Join(
			binary.Read(pck, binary.LittleEndian, &f.Seek),
			binary.Read(pck, binary.LittleEndian, &f.Size),
			binary.Read(pck, binary.LittleEndian, &f.Hash),
			binary.Read(pck, binary.LittleEndian, &f.Flag),
		); err != nil {
			return nil, xray.New(err)
		}
		head += 4 + int64(name_len) + 32
		f.Head = head
		head += 4
		f.Seek += file_base
		files[string(name_buf)] = f
	}
	return files, nil
}

// Append allocates missing files into dst from any new files from the
// given index and rewrites the index.
func Append(pck io.ReadWriteSeeker, files map[string]File) error {
	index, err := Index(pck)
	if err != nil {
		return xray.New(err)
	}
	end, err := pck.Seek(0, io.SeekEnd)
	if err != nil {
		return xray.New(err)
	}
	var added = false
	for path, file := range files {
		if exist, ok := index[path]; ok && exist.Hash == file.Hash {
			continue
		}
		file.Seek = end
		file.Flag |= FlagMissing
		end += file.Size
		index[path] = file
		added = true
	}
	if !added {
		return nil
	}
	return writeDirectory(pck, index, end)
}

// writeDirectory writes the file index as a fresh directory at offset `at`
// (the new dir_offset) and commits it by updating the header's dir_offset
// field (offset 32). That single dir_offset write is the atomic commit: until
// it lands the previous directory remains authoritative, so a crash part-way
// through leaves the pck readable at its prior directory.
func writeDirectory(pck io.WriteSeeker, index map[string]File, at int64) error {
	if _, err := pck.Seek(at, io.SeekStart); err != nil {
		return xray.New(err)
	}
	if err := binary.Write(pck, binary.LittleEndian, uint32(len(index))); err != nil {
		return xray.New(err)
	}
	for path, file := range index {
		if err := binary.Write(pck, binary.LittleEndian, uint32(len(path))); err != nil {
			return xray.New(err)
		}
		if _, err := pck.Write([]byte(path)); err != nil {
			return xray.New(err)
		}
		if err := errors.Join(
			binary.Write(pck, binary.LittleEndian, file.Seek),
			binary.Write(pck, binary.LittleEndian, file.Size),
			binary.Write(pck, binary.LittleEndian, file.Hash),
			binary.Write(pck, binary.LittleEndian, file.Flag),
		); err != nil {
			return xray.New(err)
		}
	}
	if _, err := pck.Seek(24, io.SeekStart); err != nil {
		return xray.New(err)
	}
	if err := binary.Write(pck, binary.LittleEndian, uint64(0)); err != nil { // file_base
		return xray.New(err)
	}
	if _, err := pck.Seek(32, io.SeekStart); err != nil {
		return xray.New(err)
	}
	if err := binary.Write(pck, binary.LittleEndian, at); err != nil { // dir_offset
		return xray.New(err)
	}
	return nil
}

// AppendMissing reserves directory slots for cloud files the local pck cannot
// already serve, marking them missing so they are fetched on demand. Unlike
// [Append], a file that is already present locally with bytes (non-missing)
// but whose hash differs from the cloud is LEFT pointing at its existing
// (stale) bytes rather than re-slotted — the caller refreshes those in the
// background and [Promote]s the fresh copy on a later mount, so a mount can
// serve the stale bytes without blocking on a network download. Returns
// whether the directory changed.
func AppendMissing(pck io.ReadWriteSeeker, files map[string]File) (bool, error) {
	index, err := Index(pck)
	if err != nil {
		return false, xray.New(err)
	}
	end, err := pck.Seek(0, io.SeekEnd)
	if err != nil {
		return false, xray.New(err)
	}
	var added bool
	for path, file := range files {
		if exist, ok := index[path]; ok {
			if exist.Hash == file.Hash {
				continue // already current
			}
			if !exist.Missing() {
				continue // stale but usable: keep serving it, refresh in background
			}
		}
		file.Seek = end
		file.Flag |= FlagMissing
		end += file.Size
		index[path] = file
		added = true
	}
	if !added {
		return false, nil
	}
	return true, writeDirectory(pck, index, end)
}

// Promote repoints the given paths at slots already written into the pck and
// commits a fresh directory marking them present. It is used to atomically
// swap in background-refreshed bytes at startup, before the pck is mounted.
// Paths not already in the directory are ignored. The slots' Seek/Size/Hash
// are taken from updates; any FlagMissing is cleared.
func Promote(pck io.ReadWriteSeeker, updates map[string]File) error {
	index, err := Index(pck)
	if err != nil {
		return xray.New(err)
	}
	var changed bool
	for path, slot := range updates {
		if _, ok := index[path]; !ok {
			continue
		}
		slot.Flag = 0
		index[path] = slot
		changed = true
	}
	if !changed {
		return nil
	}
	end, err := pck.Seek(0, io.SeekEnd)
	if err != nil {
		return xray.New(err)
	}
	return writeDirectory(pck, index, end)
}

// WriteManifest serializes a set of file slots (only Seek/Size/Hash are
// recorded) to w. It is used to persist a completed background refresh so a
// later run can [Promote] the slots atomically; see CommunityResourceLoader.
func WriteManifest(w io.Writer, slots map[string]File) error {
	if err := binary.Write(w, binary.LittleEndian, uint32(len(slots))); err != nil {
		return xray.New(err)
	}
	for path, slot := range slots {
		if err := errors.Join(
			binary.Write(w, binary.LittleEndian, uint32(len(path))),
			binary.Write(w, binary.LittleEndian, []byte(path)),
			binary.Write(w, binary.LittleEndian, slot.Seek),
			binary.Write(w, binary.LittleEndian, slot.Size),
			binary.Write(w, binary.LittleEndian, slot.Hash),
		); err != nil {
			return xray.New(err)
		}
	}
	return nil
}

// ReadManifest reads back a set of file slots written by [WriteManifest].
func ReadManifest(r io.Reader) (map[string]File, error) {
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, xray.New(err)
	}
	slots := make(map[string]File, count)
	for i := uint32(0); i < count; i++ {
		var n uint32
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return nil, xray.New(err)
		}
		name := make([]byte, n)
		if _, err := io.ReadFull(r, name); err != nil {
			return nil, xray.New(err)
		}
		var slot File
		if err := errors.Join(
			binary.Read(r, binary.LittleEndian, &slot.Seek),
			binary.Read(r, binary.LittleEndian, &slot.Size),
			binary.Read(r, binary.LittleEndian, &slot.Hash),
		); err != nil {
			return nil, xray.New(err)
		}
		slots[string(name)] = slot
	}
	return slots, nil
}

// Create makes a new empty pck file in the given WriteSeeker.
func Create(pck io.WriteSeeker) error {
	return errors.Join(
		binary.Write(pck, binary.LittleEndian, uint32(0x43504447)), // 0 magic
		binary.Write(pck, binary.LittleEndian, uint32(3)),          // 4 version
		binary.Write(pck, binary.LittleEndian, uint32(4)),          // 8 version_major
		binary.Write(pck, binary.LittleEndian, uint32(5)),          // 12 version_minor
		binary.Write(pck, binary.LittleEndian, uint32(1)),          // 16 version_patch
		binary.Write(pck, binary.LittleEndian, uint32(1<<1)),       // 20 pack_flags
		binary.Write(pck, binary.LittleEndian, int64(0)),           // 24 file_base
		binary.Write(pck, binary.LittleEndian, int64(40)),          // 32 dir_offset
		binary.Write(pck, binary.LittleEndian, uint32(0)),          // 40 file_count
	)
}

// Remap a file from a src pck, to the given file in the dst pck, the files
// must have the same size and hash. Updates the index to mark the file as present.
func Remap(dst io.WriteSeeker, src io.ReadSeeker, next, prev File) error {
	if next.Size != prev.Size {
		return xray.New(errors.New("cannot remap file: size mismatch"))
	}
	if next.Hash != prev.Hash {
		return xray.New(errors.New("cannot remap file: hash mismatch"))
	}
	if _, err := src.Seek(prev.Seek, io.SeekStart); err != nil {
		return xray.New(err)
	}
	if _, err := dst.Seek(next.Seek, io.SeekStart); err != nil {
		return xray.New(err)
	}
	if _, err := io.CopyN(dst, src, next.Size); err != nil {
		return xray.New(err)
	}
	if next.Head > 0 {
		next.SetMissing(false, dst)
	}
	return nil
}
