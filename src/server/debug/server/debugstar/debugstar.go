// Package debugstar lets parts of the debug dump machinery be used by Starlark scripts.  It's a
// separate package to avoid linking the debug server into pachctl for local execution.
package debugstar

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pachyderm/pachyderm/v2/src/internal/errors"
	"github.com/pachyderm/pachyderm/v2/src/internal/log"
	ourstar "github.com/pachyderm/pachyderm/v2/src/internal/starlark"
	"go.starlark.net/starlark"
)

// BuiltinScripts are the scripts loaded from starlark/.
var BuiltinScripts = map[string]string{}

//go:embed starlark/*.star
var builtin embed.FS

func init() {
	if err := fs.WalkDir(builtin, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrap(err, "initial")
		}
		if !d.Type().IsRegular() {
			return nil
		}
		script, err := fs.ReadFile(builtin, path)
		if err != nil {
			return errors.Wrap(err, "read")
		}
		BuiltinScripts[path] = string(script)
		return nil
	}); err != nil {
		panic(fmt.Sprintf("unable to load builtin scripts; %v", err))
	}

	testEnv := &Env{FS: new(InteractiveDumpFS)}
	ourstar.RegisterPersonality("debugdump", testEnv.Options())
}

// reader is an io.Reader that can come from Starlark.
type reader struct{ io.Reader }

var _ starlark.Unpacker = (*reader)(nil)

func (r *reader) Unpack(v starlark.Value) error {
	switch x := v.(type) {
	case starlark.String:
		r.Reader = strings.NewReader(string(x))
	case starlark.Bytes:
		// starlark.Bytes is actually a Go string.
		r.Reader = strings.NewReader(string(x))
	case io.Reader:
		r.Reader = x
	default:
		return errors.Errorf("starlark type %v (%#v) is not a string, bytestring, or an io.Reader", v.Type(), v)
	}
	return nil
}

// DumpFS is the part of debug/server.DumpFS that we care about.  This interface breaks a cycle
// between this package and the debug/server package.
type DumpFS interface {
	Write(string, func(io.Writer) error) error
}

// starlarkDumpFS is a DumpFS as a callable Starlark object.
type starlarkDumpFS struct {
	DumpFS
	frozen bool
}

var _ starlark.Value = (*starlarkDumpFS)(nil)
var _ starlark.Callable = (*starlarkDumpFS)(nil)

func (s *starlarkDumpFS) Freeze()               { s.frozen = true }
func (s *starlarkDumpFS) Hash() (uint32, error) { return 0, errors.New("dumpfs not hashable") }
func (s *starlarkDumpFS) Truth() starlark.Bool  { return true }
func (s *starlarkDumpFS) String() string        { return "<dumpfs>" }
func (s *starlarkDumpFS) Type() string          { return "referencelist" }
func (s *starlarkDumpFS) Name() string          { return "dump" }

// CallInternal implements starlark.Callable.
// Starlark signature: dump(name, content)
func (s *starlarkDumpFS) CallInternal(t *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if s.frozen {
		return nil, errors.New("dump is frozen")
	}
	var filename string
	var r reader
	if err := starlark.UnpackArgs("dump", args, kwargs, "filename", &filename, "content", &r); err != nil {
		return nil, errors.Wrap(err, "unpack args")
	}

	// Canonicalize the filename before passing to the underlying FS; avoid any directory
	// traversal by cleaning "/" + filename (path.Clean("../../a") == "../../a", but
	// path.Clean("/../../a") == "/a"), and then removing any leading /.  This means that
	// dump("/a"), dump("../../../a"), and dump ("a") all produce the same files in every FS
	// format.  See testdata/test.star for the test cases.
	if len(filename) > 0 && filename[0] != '/' {
		filename = "/" + filename
	}
	filename = strings.TrimPrefix(path.Clean(filename), "/")

	if err := s.DumpFS.Write(filename, func(w io.Writer) error {
		if _, err := io.Copy(w, r); err != nil {
			return errors.Wrap(err, "copy")
		}
		return nil
	}); err != nil {
		return nil, errors.Wrap(err, "write underlying")
	}
	return starlark.None, nil
}

// InteractiveDumpFS is the dump FS used for manual invocations of dump scripts with starpach.  It
// writes to a temporary directory.
type InteractiveDumpFS struct {
	Base string
	n    int
}

var _ DumpFS = (*InteractiveDumpFS)(nil)

func (fs *InteractiveDumpFS) Write(filename string, f func(w io.Writer) error) (retErr error) {
	if fs.Base == "" {
		dest, err := os.MkdirTemp("", "debug-dump-*")
		if err != nil {
			return errors.Wrap(err, "create tempdir")
		}
		fs.Base = dest
	}
	fullname := filepath.Join(fs.Base, filename)
	if err := os.MkdirAll(filepath.Dir(fullname), 0o755); err != nil {
		return errors.Wrapf(err, "create parent directory for file %v", fullname)
	}
	fh, err := os.Create(fullname)
	if err != nil {
		return errors.Wrapf(err, "create file %v", fullname)
	}
	defer errors.Close(&retErr, fh, "close file %v", fullname)
	if err := f(fh); err != nil {
		return errors.Wrap(err, "run callback")
	}
	if fs.n == 0 {
		fmt.Fprintf(os.Stderr, "Note: debug dump files from this session available in %v.\n", fs.Base)
	}
	fs.n++
	return nil
}

// LocalDumpFS is the dump FS used for `pachctl debug dump --local` invocations.  It writes to a tgz
// file in the current directory.
type LocalDumpFS struct {
	tw   *tar.Writer
	gzw  *gzip.Writer
	w    io.WriteCloser
	name string
}

var _ DumpFS = (*LocalDumpFS)(nil)
var _ io.Closer = (*LocalDumpFS)(nil)

// Name returns the name of the file created for the archive; printed by pachctl.
func (fs *LocalDumpFS) Name() string {
	return fs.name
}

func (fs *LocalDumpFS) Write(filename string, f func(io.Writer) error) error {
	// We have to buffer the content so we can write the correct TAR header.
	buf := new(bytes.Buffer)
	if err := f(buf); err != nil {
		return errors.Wrap(err, "runcallback")
	}
	// We do the actual write before creating the archive so that if the first callback fails,
	// we don't have an annoying empty file around on disk.
	if fs.w == nil {
		name := fmt.Sprintf("debug-dump-%v.tgz", time.Now().In(time.UTC).Format("20060102T150405Z"))
		fh, err := os.Create(name)
		if err != nil {
			return errors.Wrap(err, "create output file")
		}
		fs.w = fh
		fs.name = name
	}
	if fs.gzw == nil {
		fs.gzw = gzip.NewWriter(fs.w)
	}
	if fs.tw == nil {
		fs.tw = tar.NewWriter(fs.gzw)
	}
	if err := fs.tw.WriteHeader(&tar.Header{
		Name:    filename,
		Size:    int64(buf.Len()),
		Mode:    0o666,
		ModTime: time.Now(),
	}); err != nil {
		return errors.Wrapf(err, "write header for file %v", filename)
	}
	if _, err := io.Copy(fs.tw, buf); err != nil {
		return errors.Wrap(err, "copy result into archive")
	}
	// Flush now, so that failing scripts produce the maximum possible output.
	if err := fs.tw.Flush(); err != nil {
		return errors.Wrap(err, "flush archive")
	}
	if err := fs.gzw.Flush(); err != nil {
		return errors.Wrap(err, "flush gzip")
	}
	if w, ok := fs.w.(interface{ Sync() error }); ok {
		if err := w.Sync(); err != nil {
			return errors.Wrap(err, "sync disk file")
		}
	}
	return nil
}

func (fs *LocalDumpFS) Close() (retErr error) {
	if w := fs.tw; w != nil {
		errors.Close(&retErr, w, "close archive")
	}
	if w := fs.gzw; w != nil {
		errors.Close(&retErr, w, "close gzip")
	}
	if w := fs.w; w != nil {
		errors.Close(&retErr, w, "close disk file")
	}
	return
}

// Env is the parts of the debug dump service available to Starlark scripts.
type Env struct {
	FS DumpFS
}

// RunStarlark executes the provided script.
func (e *Env) RunStarlark(rctx context.Context, name string, script string) (retErr error) {
	ctx, done := log.SpanContext(rctx, fmt.Sprintf("starlark(%v)", name))
	defer done(log.Errorp(&retErr))
	defer func() {
		if err := recover(); err != nil {
			errors.JoinInto(&retErr, errors.Errorf("starlark evaluation panicked: %v", err))
		}
	}()
	if _, err := ourstar.RunScript(ctx, name, script, e.Options()); err != nil {
		return errors.Wrap(err, "RunScript")
	}
	return nil
}

// Options returns the starlark options for this environment.
func (e *Env) Options() ourstar.Options {
	return ourstar.Options{
		Predefined: starlark.StringDict{
			"dump": &starlarkDumpFS{DumpFS: e.FS},
		},
	}
}
