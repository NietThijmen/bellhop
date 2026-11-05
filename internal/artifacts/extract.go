package artifacts

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

// symlink is os.Symlink; tests replace it to exercise the copy fallback.
var symlink = os.Symlink

// extractor writes archive entries below root. All writes of regular files,
// directories and hard links happen first; symbolic links are queued and
// created at the end, so no file write can ever traverse a link from the
// archive.
type extractor struct {
	ctx   context.Context
	root  string
	strip string
	links []pendingLink
	count int
}

type pendingLink struct{ name, target string }

// extract unpacks f (size bytes, already verified) into root.
func extract(ctx context.Context, f *os.File, size int64, format, stripPrefix, root string) error {
	x := &extractor{ctx: ctx, root: root, strip: normalizePrefix(stripPrefix)}
	var err error
	switch format {
	case FormatZip:
		err = x.zip(f, size)
	case FormatFile:
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		name := "download"
		if base := filepath.Base(stripPrefix); base != "" && base != "." && base != "/" {
			name = base
		}
		err = x.writeFile(name, 0o644, f)
	case FormatTarGz, FormatTarXz:
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		var r io.Reader
		if format == FormatTarGz {
			gz, gzErr := gzip.NewReader(f)
			if gzErr != nil {
				return fmt.Errorf("open gzip stream: %w", gzErr)
			}
			defer func() { _ = gz.Close() }()
			r = gz
		} else {
			xr, xzErr := xz.NewReader(f)
			if xzErr != nil {
				return fmt.Errorf("open xz stream: %w", xzErr)
			}
			r = xr
		}
		err = x.tar(r)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
	if err != nil {
		return err
	}
	if err := x.createLinks(); err != nil {
		return err
	}
	if x.count == 0 {
		if x.strip != "" {
			return fmt.Errorf("archive has no entries under stripPrefix %q; the manifest entry is wrong, report it", stripPrefix)
		}
		return errors.New("archive is empty; the manifest entry is wrong, report it")
	}
	return nil
}

func normalizePrefix(p string) string {
	p = strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/")
	if p == "" || p == "." {
		return ""
	}
	return path.Clean(p) + "/"
}

// rel maps an archive name to a validated slash path relative to root.
// skip is true for entries outside stripPrefix and for the prefix itself.
func (x *extractor) rel(name string) (rel string, skip bool, err error) {
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return "", false, fmt.Errorf("%w: %q is absolute", ErrUnsafeArchive, name)
	}
	n = strings.TrimPrefix(n, "./")
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", false, fmt.Errorf("%w: %q escapes the install directory", ErrUnsafeArchive, name)
		}
	}
	n = path.Clean(n)
	if x.strip != "" {
		if n+"/" == x.strip {
			return "", true, nil
		}
		if !strings.HasPrefix(n, x.strip) {
			return "", true, nil
		}
		n = strings.TrimPrefix(n, x.strip)
	}
	if n == "." || n == "" {
		return "", true, nil
	}
	if !filepath.IsLocal(filepath.FromSlash(n)) {
		return "", false, fmt.Errorf("%w: %q is not a valid local path on this system", ErrUnsafeArchive, name)
	}
	return n, false, nil
}

func (x *extractor) abs(rel string) string { return filepath.Join(x.root, filepath.FromSlash(rel)) }

// fileMode masks archive permissions to 0755 for anything executable and
// 0644 otherwise; setuid/setgid/sticky and group/other write are dropped.
func fileMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func (x *extractor) mkdir(rel string) error {
	if err := os.MkdirAll(x.abs(rel), 0o755); err != nil { //nolint:gosec // installed binaries are world-readable like a normal install; parent data dir is 0700
		return fmt.Errorf("create directory %s: %w", rel, err)
	}
	x.count++
	return nil
}

func (x *extractor) writeFile(rel string, mode fs.FileMode, r io.Reader) error {
	dst := x.abs(rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // see mkdir
		return fmt.Errorf("create directory for %s: %w", rel, err)
	}
	m := fileMode(mode)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, m)
	if err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	if _, err := io.Copy(f, ctxReader{x.ctx, r}); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	// OpenFile's mode is subject to umask and ignored for existing files.
	if err := os.Chmod(dst, m); err != nil {
		return fmt.Errorf("set mode on %s: %w", rel, err)
	}
	x.count++
	return nil
}

// hardlink copies an already extracted regular file (tar TypeLink).
func (x *extractor) hardlink(rel, linkname string) error {
	src, skip, err := x.rel(linkname)
	if err != nil {
		return err
	}
	if skip {
		return fmt.Errorf("%w: hard link %q points outside stripPrefix", ErrUnsafeArchive, rel)
	}
	st, err := os.Lstat(x.abs(src))
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%w: hard link %q target %q is not an earlier regular file", ErrUnsafeArchive, rel, linkname)
	}
	in, err := os.Open(x.abs(src))
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	return x.writeFile(rel, st.Mode(), in)
}

// queueLink validates a symlink and defers its creation. Targets must be
// relative, may use ".." only as leading components, and must stay inside
// the root when resolved from the link's directory. Combined with refusing
// to create a link below another link, every link resolves inside root.
func (x *extractor) queueLink(rel, target string) error {
	t := target
	if t == "" || strings.HasPrefix(t, "/") || strings.ContainsAny(t, `\:`) {
		return fmt.Errorf("%w: symlink %q -> %q must be relative", ErrUnsafeArchive, rel, target)
	}
	parts := strings.Split(t, "/")
	up, seenName := 0, false
	for _, p := range parts {
		switch p {
		case "..":
			if seenName {
				return fmt.Errorf("%w: symlink %q -> %q uses '..' after a name", ErrUnsafeArchive, rel, target)
			}
			up++
		case ".", "":
		default:
			seenName = true
		}
	}
	depth := strings.Count(rel, "/") // directories between root and the link
	if up > depth {
		return fmt.Errorf("%w: symlink %q -> %q escapes the install directory", ErrUnsafeArchive, rel, target)
	}
	x.links = append(x.links, pendingLink{name: rel, target: path.Clean(t)})
	x.count++
	return nil
}

func (x *extractor) createLinks() error {
	for _, l := range x.links {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		// Refuse to create a link through another link.
		dir := path.Dir(l.name)
		for d := dir; d != "." && d != "/"; d = path.Dir(d) {
			if st, err := os.Lstat(x.abs(d)); err == nil && st.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%w: %q is below symlink %q", ErrUnsafeArchive, l.name, d)
			}
		}
		dst := x.abs(l.name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // see mkdir
			return fmt.Errorf("create directory for %s: %w", l.name, err)
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("replace %s: %w", l.name, err)
		}
		err := symlink(filepath.FromSlash(l.target), dst)
		if err == nil {
			continue
		}
		// Symlinks need Developer Mode or admin rights on Windows; fall back
		// to copying what the link points at.
		srcRel := path.Join(dir, l.target)
		src := x.abs(srcRel)
		slog.Debug("symlink failed; copying target instead", "link", l.name, "target", l.target, "err", err)
		if srcRel == "." || strings.HasPrefix(l.name+"/", srcRel+"/") {
			slog.Warn("skipping archive symlink that points at its own parent and cannot be created", "link", l.name, "target", l.target, "err", err)
			continue
		}
		st, statErr := os.Stat(src)
		if statErr != nil {
			slog.Warn("skipping archive symlink whose target is missing and that cannot be created", "link", l.name, "target", l.target, "err", err)
			continue
		}
		if st.IsDir() {
			err = os.CopyFS(dst, os.DirFS(src))
		} else {
			err = copyFile(src, dst, st.Mode())
		}
		if err != nil {
			return fmt.Errorf("copy %s in place of symlink: %w", l.name, err)
		}
	}
	return nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, fileMode(mode))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (x *extractor) tar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		}
		rel, skip, err := x.rel(h.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = x.mkdir(rel)
		case tar.TypeReg:
			err = x.writeFile(rel, h.FileInfo().Mode(), tr)
		case tar.TypeSymlink:
			err = x.queueLink(rel, h.Linkname)
		case tar.TypeLink:
			err = x.hardlink(rel, h.Linkname)
		default:
			slog.Warn("skipping unsupported tar entry", "name", h.Name, "type", string(h.Typeflag))
		}
		if err != nil {
			return err
		}
	}
}

func (x *extractor) zip(f io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return fmt.Errorf("read zip: %w", err)
	}
	for _, zf := range zr.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		rel, skip, err := x.rel(zf.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			err = x.mkdir(rel)
		case mode&fs.ModeSymlink != 0:
			err = x.zipLink(rel, zf)
		case mode.IsRegular():
			err = x.zipFile(rel, zf)
		default:
			slog.Warn("skipping unsupported zip entry", "name", zf.Name, "mode", mode.String())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *extractor) zipFile(rel string, zf *zip.File) error {
	rc, err := zf.Open()
	if err != nil {
		return fmt.Errorf("open %s in zip: %w", zf.Name, err)
	}
	defer func() { _ = rc.Close() }()
	return x.writeFile(rel, zf.Mode(), rc)
}

func (x *extractor) zipLink(rel string, zf *zip.File) error {
	rc, err := zf.Open()
	if err != nil {
		return fmt.Errorf("open %s in zip: %w", zf.Name, err)
	}
	defer func() { _ = rc.Close() }()
	target, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return fmt.Errorf("read symlink %s in zip: %w", zf.Name, err)
	}
	return x.queueLink(rel, string(target))
}

// ctxReader aborts a copy once ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
