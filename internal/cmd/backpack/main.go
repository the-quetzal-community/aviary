package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"runtime.link/api/xray"

	"the.quetzal.community/aviary/internal/pck"
)

// backpack [src] [dst]
//
//	copies .import and .remap files form 'src' .pck into the 'dst' .pck
func backpack(src_pck, dst_pck string) error {
	src, err := os.OpenFile(src_pck, os.O_RDONLY, 0644)
	if err != nil {
		return xray.New(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(dst_pck, os.O_RDWR, 0644)
	if err != nil {
		return xray.New(err)
	}
	defer dst.Close()
	index, err := pck.Index(src)
	if err != nil {
		return xray.New(err)
	}
	exist, err := pck.Index(dst)
	if err != nil {
		return xray.New(err)
	}
	for path := range index {
		if _, ok := exist[path]; ok || !(strings.HasSuffix(path, ".import") || strings.HasSuffix(path, ".remap")) || strings.HasPrefix(path, "preview/") {
			delete(index, path)
		}
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return xray.New(err)
	}
	if err := pck.Append(dst, index); err != nil {
		return xray.New(err)
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return xray.New(err)
	}
	exist, err = pck.Index(dst)
	if err != nil {
		return xray.New(err)
	}
	for path := range index {
		if err := pck.Remap(dst, src, exist[path], index[path]); err != nil {
			return xray.New(err)
		}
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return xray.New(err)
	}
	exist, err = pck.Index(dst)
	if err != nil {
		return xray.New(err)
	}
	return nil
}

func main() {
	if len(os.Args) != 3 {
		panic("usage: backpack [src.pck] [dst.pck]")
	}
	if err := backpack(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
