package aqua

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// What bounds unpacking a download, against an archive that expands
// without end: maxUnpackedBytes what it decompresses to, the entries it
// leaves out and its headers included, and maxEntries how many entries
// there are. Variables for the tests.
var (
	maxUnpackedBytes int64 = 4 << 30
	maxEntries             = 200_000
)

// The compressions a download is read through.
const (
	compressionGzip  = "gz"
	compressionBzip2 = "bz2"
	compressionXZ    = "xz"
	compressionZstd  = "zst"
)

// formatZip is a zip archive.
const formatZip = "zip"

// tarFormats are the formats that hold a tar archive, with the
// compression it is read through. A compression's own name is a format
// too: a compressed file alone.
var tarFormats = map[string]string{
	"tar": "", "tar.gz": compressionGzip, "tgz": compressionGzip, "tar.bz2": compressionBzip2, "tbz": compressionBzip2,
	"tbz2": compressionBzip2, "tar.xz": compressionXZ, "txz": compressionXZ, "tar.zst": compressionZstd,
}

// unpackable reports whether unpack reads format.
func unpackable(format string) bool {
	_, archived := tarFormats[format]
	switch format {
	case FormatRaw, formatZip, compressionGzip, compressionBzip2, compressionXZ, compressionZstd:
		return true
	}
	return archived
}

// unpack lays the download at file, packed as format and named asset, out
// in root: an archive's entries, a compressed file under asset's name less
// its extension, as aqua names it, or the file itself under asset's name.
// It stops between entries once ctx ends.
func unpack(ctx context.Context, file, format, asset, root string) error {
	u := &unpacker{ctx: ctx, root: root, dirs: map[string]bool{".": true}}
	switch format {
	case FormatRaw:
		if err := u.check(asset); err != nil {
			return err
		}
		return os.Rename(file, filepath.Join(root, asset))
	case formatZip:
		return u.zip(file)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	compression, archived := tarFormats[format]
	if !archived {
		compression = format
	}
	r, closer, err := decompress(f, compression)
	if err != nil {
		return err
	}
	defer closer()
	r = &bounded{r: r, left: maxUnpackedBytes}
	if archived {
		return u.tar(r)
	}
	name := strings.TrimSuffix(asset, filepath.Ext(asset))
	if err := u.check(name); err != nil {
		return err
	}
	return u.file(name, r, 0o755)
}

// decompress reads r through compression, "" for none. closer frees what
// reading it took.
func decompress(r io.Reader, compression string) (_ io.Reader, closer func(), _ error) {
	switch compression {
	case "":
		return r, func() {}, nil
	case compressionGzip:
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return zr, func() { _ = zr.Close() }, nil
	case compressionBzip2:
		return bzip2.NewReader(r), func() {}, nil
	case compressionXZ:
		xr, err := xz.NewReader(r)
		return xr, func() {}, err
	case compressionZstd:
		d, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return d, d.Close, nil
	}
	return nil, nil, fmt.Errorf("%s is not a compression the runner reads", compression)
}

// bounded reads r until left bytes are read, then fails.
type bounded struct {
	r    io.Reader
	left int64
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, fmt.Errorf("over %d bytes unpacked", maxUnpackedBytes)
	}
	n, err := b.r.Read(p[:min(int64(len(p)), b.left)])
	b.left -= int64(n)
	return n, err
}

// unpacker writes an archive's entries under root, never through a link.
// Every directory that an entry is written in is one it made or found as a
// directory, not a link. A link that an entry is never replaces such a
// directory.
type unpacker struct {
	ctx     context.Context
	root    string
	dirs    map[string]bool
	written int64
	entries int
}

// check is why name cannot be an entry's path: one that is absolute or leaves
// root.
func (u *unpacker) check(name string) error {
	if !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("entry %q is not a path within the download", name)
	}
	return nil
}

// clean is an entry's name as a slash-separated path within root, "." for
// root itself.
func (u *unpacker) clean(name string) (string, error) {
	if err := u.ctx.Err(); err != nil {
		return "", err
	}
	u.entries++
	if u.entries > maxEntries {
		return "", fmt.Errorf("over %d entries", maxEntries)
	}
	name = path.Clean(strings.TrimPrefix(name, "./"))
	if name == "." || name == "/" {
		return ".", nil
	}
	if err := u.check(name); err != nil {
		return "", err
	}
	return name, nil
}

// dir makes name, a directory, and every one above it. It makes sure that
// each one that exists is a directory and not a link.
func (u *unpacker) dir(name string) error {
	if u.dirs[name] {
		return nil
	}
	if err := u.dir(path.Dir(name)); err != nil {
		return err
	}
	p := filepath.Join(u.root, filepath.FromSlash(name))
	info, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = os.Mkdir(p, 0o755)
	case err == nil && !info.IsDir():
		err = fmt.Errorf("%s is in the download as a file and a directory", name)
	}
	if err != nil {
		return err
	}
	u.dirs[name] = true
	return nil
}

// replace readies name, an entry other than a directory, to be written:
// its directory made, and an earlier entry of its name that is not a
// directory removed, as a later entry replaces it.
func (u *unpacker) replace(name string) (string, error) {
	if err := u.dir(path.Dir(name)); err != nil {
		return "", err
	}
	p := filepath.Join(u.root, filepath.FromSlash(name))
	info, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return p, nil
	case err != nil:
		return "", err
	case info.IsDir():
		return "", fmt.Errorf("%s is in the download as a directory and a file", name)
	}
	return p, os.Remove(p)
}

// file writes r to name, a regular file with mode's permissions, less the
// set-ID bits and others' writes.
func (u *unpacker) file(name string, r io.Reader, mode fs.FileMode) error {
	p, err := u.replace(name)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm()&0o755|0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxUnpackedBytes-u.written+1))
	u.written += n
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && u.written > maxUnpackedBytes {
		err = fmt.Errorf("over %d bytes unpacked", maxUnpackedBytes)
	}
	return err
}

// symlink makes name a link to target, which must lead to a path within
// root from name's directory.
func (u *unpacker) symlink(name, target string) error {
	if path.IsAbs(target) || !filepath.IsLocal(filepath.FromSlash(path.Join(path.Dir(name), target))) {
		return fmt.Errorf("link %s leads out of the download", name)
	}
	p, err := u.replace(name)
	if err != nil {
		return err
	}
	return os.Symlink(target, p)
}

// hardlink makes name a second name of target, an entry written already.
func (u *unpacker) hardlink(name, target string) error {
	target = path.Clean(strings.TrimPrefix(target, "./"))
	if err := u.check(target); err != nil {
		return err
	}
	src := filepath.Join(u.root, filepath.FromSlash(target))
	if info, err := os.Lstat(src); err != nil || !info.Mode().IsRegular() || !u.dirs[path.Dir(target)] {
		return fmt.Errorf("link %s is to %s, not a file earlier in the download", name, target)
	}
	p, err := u.replace(name)
	if err != nil {
		return err
	}
	return os.Link(src, p)
}

// tar lays out the tar archive r. Entries other than directories, files
// and links are left out.
func (u *unpacker) tar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := u.clean(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = u.dir(name)
		case tar.TypeReg:
			err = u.file(name, tr, h.FileInfo().Mode())
		case tar.TypeSymlink:
			err = u.symlink(name, h.Linkname)
		case tar.TypeLink:
			err = u.hardlink(name, h.Linkname)
		}
		if err != nil {
			return err
		}
	}
}

// zip lays out the zip archive at file, as tar does.
func (u *unpacker) zip(file string) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		name, err := u.clean(f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = u.dir(name)
		case mode&fs.ModeSymlink != 0:
			err = u.zipSymlink(name, f)
		case mode.IsRegular():
			err = u.zipFile(name, f)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *unpacker) zipFile(name string, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return u.file(name, rc, f.Mode())
}

// zipSymlink makes name the link a zip entry holds the target of.
func (u *unpacker) zipSymlink(name string, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	target, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return err
	}
	return u.symlink(name, string(target))
}
