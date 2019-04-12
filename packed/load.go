package packed

import (
	fmt "fmt"
	"os"
	"path"
	"strings"
)

const (
	// Magic number to identify .htpack files.
	Magic = 0xb6e61a4b415ed33b

	// VersionInitial is the version number used by the initial packed
	// format. Loading a file with a higher version number will cause an
	// error to be returned.
	VersionInitial = 1
)

// Load a ready-packed file.
func Load(f *os.File) (*Header, *Directory, error) {
	hdr, err := loadHeader(f)
	if err != nil {
		return nil, nil, err
	}

	dir, err := loadDirectory(f, hdr)
	if le, ok := err.(*LoadError); ok {
		// augment error
		le.Magic = hdr.Magic
		le.Version = hdr.Version
	}
	return hdr, dir, err // we may have a partial dir
}

// loadHeader retrieves and decodes the header from the start of the file. It
// ensures the magic number and the version number match. Errors are returned
// as type LoadError.
func loadHeader(f *os.File) (*Header, error) {
	raw := make([]byte, 36)
	if _, err := f.ReadAt(raw, 0); err != nil {
		return nil, &LoadError{
			Cause:      IOError,
			Underlying: err,
		}
	}

	hdr := new(Header)
	if err := hdr.Unmarshal(raw); err != nil {
		return nil, &LoadError{
			Cause:      HeaderUnmarshalError,
			Underlying: err,
		}
	}

	switch {
	case hdr.Magic != Magic:
		return nil, &LoadError{
			Cause:   MagicMismatch,
			Magic:   hdr.Magic,
			Version: hdr.Version,
		}
	case hdr.Version < VersionInitial:
		return nil, &LoadError{
			Cause:   VersionTooOld,
			Magic:   hdr.Magic,
			Version: hdr.Version,
		}
	case hdr.Version > VersionInitial:
		return nil, &LoadError{
			Cause:   VersionTooNew,
			Magic:   hdr.Magic,
			Version: hdr.Version,
		}
	}

	return hdr, nil
}

// loadDirectory reads the directory from a file. The directory is checked
// for consistency (offsets, filenames) but not integrity (file data is not
// read/checksummed).
func loadDirectory(f *os.File, hdr *Header) (*Directory, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, &LoadError{
			Cause:      IOError,
			Underlying: err,
		}
	}
	fileSize := uint64(fi.Size())

	if hdr.DirectoryOffset+hdr.DirectoryLength > fileSize {
		return nil, &LoadError{
			Cause: BadOffsetError,
		}
	}

	raw := make([]byte, hdr.DirectoryLength)
	if _, err := f.ReadAt(raw, int64(hdr.DirectoryOffset)); err != nil {
		return nil, &LoadError{
			Cause:      IOError,
			Underlying: err,
		}
	}

	dir := new(Directory)
	if err := dir.Unmarshal(raw); err != nil {
		return nil, &LoadError{
			Cause:      DirectoryUnmarshalError,
			Underlying: err,
		}
	}

	return dir, checkDirectory(dir, fileSize)
}

// checkDirectory verifies the consistency of the htpack file (offsets,
// filenames). It does not verify integrity (checksums).
func checkDirectory(dir *Directory, fileSize uint64) error {
	files := map[string]struct{}{}

	for filename, info := range dir.Files {
		var err error

		// validate filename (not duplicate, canonical, etc)
		if _, dup := files[filename]; dup {
			err = fmt.Errorf("duplicate path %q", filename)
		}
		files[filename] = struct{}{}

		if !path.IsAbs(filename) {
			err = fmt.Errorf("relative path %q", filename)
		}
		if path.Clean(filename) != filename {
			err = fmt.Errorf("non-canonical path %q", filename)
		}
		if err != nil {
			return &LoadError{
				Cause:      InvalidPath,
				Underlying: err,
				Path:       filename,
			}
		}

		// ensure uncompressed data is present
		if info.Uncompressed == nil {
			return &LoadError{
				Cause: MissingUncompressed,
				Path:  filename,
			}
		}

		// validate offsets
		checkOffset(&err, filename, info.Uncompressed, fileSize)
		checkOffset(&err, filename, info.Gzip, fileSize)
		checkOffset(&err, filename, info.Brotli, fileSize)
		if err != nil {
			return &LoadError{
				Cause: BadOffsetError,
			}
		}
	}

	return nil
}

func checkOffset(perr *error, filename string, data *FileData, fileSize uint64) {
	if *perr != nil || data == nil {
		return
	}
	if data.Offset+data.Length > fileSize {
		*perr = &LoadError{
			Cause: BadOffsetError,
			Path:  filename,
		}
	}
}

// LoadError reports a problem interpreting the header of a pack file.
type LoadError struct {
	// Cause of the error.
	Cause ErrorCause

	// Underlying may be set if there is some more information about the
	// error (e.g. I/O error).
	Underlying error

	// Magic as read from the file.
	Magic uint64

	// Version as read from the file.
	Version uint64

	// Path of an individual file within the pack, if relevant.
	Path string
}

// ErrorCause enumerates the possible reasons for failure.
type ErrorCause int

const (
	// HeaderUnmarshalError means we could not unmarshal the protobuf object
	// at the head of the file.
	HeaderUnmarshalError ErrorCause = iota

	// DirectoryUnmarshalError means we could not unmarshal the protobuf
	// object holding the directory contents.
	DirectoryUnmarshalError

	// IOError indicates we could not read a protobuf object.
	IOError

	// MagicMismatch occurs if the recorded magic number does not match
	// the well-known constant Magic.
	MagicMismatch

	// VersionTooNew indicates that the file has a version number ahead of
	// what this package can parse.
	VersionTooNew

	// VersionTooOld indicates that the file has a version number older than
	// thisk package can parse.
	VersionTooOld

	// BadOffsetError means that the header or directory has indicated a
	// position that lies outside of the file.
	BadOffsetError

	// InvalidPath is returned for a duplicate or otherwise invalid path.
	// Underlying is set to a free-form string error describing the path.
	InvalidPath

	// MissingUncompressed indicates that a file in the pack does not have
	// an uncompressed version present, which is mandatory.
	MissingUncompressed
)

// Desc returns a description of the error cause.
func (le *LoadError) Desc() string {
	switch le.Cause {
	case HeaderUnmarshalError:
		return "not a .htpack file (header not valid packed.Header)"
	case DirectoryUnmarshalError:
		return "file corrupt (directory not valid packed.Directory)"
	case IOError:
		return "error reading from file"
	case MagicMismatch:
		return "magic number does not match"
	case VersionTooNew:
		return "version too new"
	case VersionTooOld:
		return "version too old"
	case BadOffsetError:
		return "file corrupt/truncated (offset past end of file)"
	case InvalidPath:
		return "filename invalid"
	case MissingUncompressed:
		return "missing uncompressed version"
	default:
		return "unknown error"
	}
}

// Error returns a concise description of the error.
func (le *LoadError) Error() string {
	var b strings.Builder

	b.WriteString(le.Desc())

	var underlying, magic, version, path bool
	switch le.Cause {
	case HeaderUnmarshalError, IOError:
		underlying = true
	case MagicMismatch:
		magic = true
	case VersionTooNew, VersionTooOld:
		version = true
	case BadOffsetError:
		path = le.Path != ""
	case InvalidPath, MissingUncompressed:
		path = true
	}

	if underlying {
		b.WriteString(" [")
		b.WriteString(le.Underlying.Error())
		b.WriteString("]")
	}
	if magic {
		fmt.Fprintf(&b, " (found magic 0x%X, expected 0x%X)",
			le.Magic, uint64(Magic))
	}
	if version {
		fmt.Fprintf(&b, " (found version %d; oldest supported: "+
			"%d, newest: %d",
			le.Version, VersionInitial, VersionInitial)
	}
	if path {
		fmt.Fprintf(&b, " (path %q)", le.Path)
	}

	return b.String()
}
