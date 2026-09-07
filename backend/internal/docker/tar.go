package docker

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// tarWalk walks `root` and writes a tar header + body for every
// regular file, using paths relative to `root` (e.g. "Dockerfile"
// instead of "/tmp/src/Dockerfile").
func tarWalk(root, dir string, w *tar.Writer) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return writeDir(w, rel, d)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks, devices, etc.
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := w.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, f)
		_ = f.Close()
		if copyErr != nil {
			return fmt.Errorf("copy %s: %w", path, copyErr)
		}
		return nil
	})
}

func writeDir(w *tar.Writer, rel string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel) + "/"
	return w.WriteHeader(hdr)
}
