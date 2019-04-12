package packer

import (
	"bufio"
	"crypto/sha512"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"

	"github.com/foobaz/go-zopfli/zopfli"
	"github.com/lwithers/htpack/packed"
	"github.com/lwithers/pkg/writefile"
)

var BrotliPath string = "brotli"

type FilesToPack map[string]FileToPack

type FileToPack struct {
	Filename           string `yaml:"filename"`
	ContentType        string `yaml:"content_type"`
	DisableCompression bool   `yaml:"disable_compression"`
	DisableGzip        bool   `yaml:"disable_gzip"`
	DisableBrotli      bool   `yaml:"disable_brotli"`

	uncompressed, gzip, brotli packInfo
}

type packInfo struct {
	present     bool
	offset, len uint64
}

const (
	// minCompressionSaving means we'll only use the compressed version of
	// the file if it's at least this many bytes smaller than the original.
	// Chosen somewhat arbitrarily; we have to add an HTTP header, and the
	// decompression overhead is not zero.
	minCompressionSaving = 128

	// minCompressionFraction means we'll only use the compressed version of
	// the file if it's at least (origSize>>minCompressionFraction) bytes
	// smaller than the original. This is a guess at when the decompression
	// overhead outweighs the time saved in transmission.
	minCompressionFraction = 7 // i.e. files must be at least 1/128 smaller
)

// Pack a file.
func Pack(filesToPack FilesToPack, outputFilename string) error {
	finalFname, outputFile, err := writefile.New(outputFilename)
	if err != nil {
		return err
	}
	defer writefile.Abort(outputFile)
	packer := &packWriter{f: outputFile}

	// write initial header (will rewrite offset/length when known)
	hdr := &packed.Header{
		Magic:           packed.Magic,
		Version:         packed.VersionInitial,
		DirectoryOffset: 1,
		DirectoryLength: 1,
	}
	m, _ := hdr.Marshal()
	packer.Write(m)

	dir := packed.Directory{
		Files: make(map[string]*packed.File),
	}

	for path, fileToPack := range filesToPack {
		info, err := packOne(packer, fileToPack)
		if err != nil {
			return err
		}
		dir.Files[path] = &info
	}

	// write the directory
	if m, err = dir.Marshal(); err != nil {
		err = fmt.Errorf("marshaling directory object: %v", err)
		return err
	}

	packer.Pad()
	hdr.DirectoryOffset = packer.Pos()
	hdr.DirectoryLength = uint64(len(m))
	if _, err := packer.Write(m); err != nil {
		return err
	}

	// write header at start of file
	m, _ = hdr.Marshal()
	if _, err = outputFile.WriteAt(m, 0); err != nil {
		return err
	}

	// all done!
	return writefile.Commit(finalFname, outputFile)
}

func packOne(packer *packWriter, fileToPack FileToPack) (info packed.File, err error) {
	// implementation detail: write files at a page boundary
	if err = packer.Pad(); err != nil {
		return
	}

	// open and mmap input file
	f, err := os.Open(fileToPack.Filename)
	if err != nil {
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return
	}

	data, err := unix.Mmap(int(f.Fd()), 0, int(fi.Size()),
		unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		err = fmt.Errorf("mmap %s: %v", fileToPack.Filename, err)
		return
	}
	defer unix.Munmap(data)

	info.Etag = etag(data)
	info.ContentType = fileToPack.ContentType
	if info.ContentType == "" {
		info.ContentType = http.DetectContentType(data)
	}

	// copy the uncompressed version
	fileData := &packed.FileData{
		Offset: packer.Pos(),
		Length: uint64(len(data)),
	}
	if _, err = packer.CopyFrom(f, fi); err != nil {
		return
	}
	info.Uncompressed = fileData

	if fileToPack.DisableCompression {
		return
	}

	// gzip compression
	if !fileToPack.DisableGzip {
		if err = packer.Pad(); err != nil {
			return
		}
		fileData = &packed.FileData{
			Offset: packer.Pos(),
		}
		fileData.Length, err = packOneGzip(packer, data,
			info.Uncompressed.Length)
		if err != nil {
			return
		}
		if fileData.Length > 0 {
			info.Gzip = fileData
		}
	}

	// brotli compression
	if BrotliPath != "" && !fileToPack.DisableBrotli {
		if err = packer.Pad(); err != nil {
			return
		}
		fileData = &packed.FileData{
			Offset: packer.Pos(),
		}
		fileData.Length, err = packOneBrotli(packer,
			fileToPack.Filename, info.Uncompressed.Length)
		if err != nil {
			return
		}
		if fileData.Length > 0 {
			info.Brotli = fileData
		}
	}

	return
}

func etag(in []byte) string {
	h := sha512.New384()
	h.Write(in)
	return fmt.Sprintf(`"1--%x"`, h.Sum(nil))
}

func packOneGzip(packer *packWriter, data []byte, uncompressedSize uint64,
) (uint64, error) {
	// write via temporary file
	tmpfile, err := ioutil.TempFile("", "")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	// compress
	opts := zopfli.DefaultOptions()
	if len(data) > (10 << 20) { // 10MiB
		opts.NumIterations = 5
	}

	buf := bufio.NewWriter(tmpfile)
	if err = zopfli.GzipCompress(&opts, data, buf); err != nil {
		return 0, err
	}
	if err = buf.Flush(); err != nil {
		return 0, err
	}

	// copy into packfile
	return packer.CopyIfSaving(tmpfile, uncompressedSize)
}

func packOneBrotli(packer *packWriter, filename string, uncompressedSize uint64,
) (uint64, error) {
	// write via temporary file
	tmpfile, err := ioutil.TempFile("", "")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	// compress via commandline
	cmd := exec.Command(BrotliPath, "--input", filename,
		"--output", tmpfile.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("brotli: %v (process reported: %s)", err, out)
		return 0, err
	}

	// copy into packfile
	return packer.CopyIfSaving(tmpfile, uncompressedSize)
}

type packWriter struct {
	f   *os.File
	err error
}

func (pw *packWriter) Write(buf []byte) (int, error) {
	if pw.err != nil {
		return 0, pw.err
	}
	n, err := pw.f.Write(buf)
	pw.err = err
	return n, err
}

func (pw *packWriter) Pos() uint64 {
	pos, err := pw.f.Seek(0, os.SEEK_CUR)
	if err != nil {
		pw.err = err
	}
	return uint64(pos)
}

func (pw *packWriter) Pad() error {
	if pw.err != nil {
		return pw.err
	}

	pos, err := pw.f.Seek(0, os.SEEK_CUR)
	if err != nil {
		pw.err = err
		return pw.err
	}

	pos &= 0xFFF
	if pos == 0 {
		return pw.err
	}

	if _, err = pw.f.Seek(4096-pos, os.SEEK_CUR); err != nil {
		pw.err = err
	}
	return pw.err
}

func (pw *packWriter) CopyIfSaving(in *os.File, uncompressedSize uint64) (uint64, error) {
	if pw.err != nil {
		return 0, pw.err
	}

	fi, err := in.Stat()
	if err != nil {
		pw.err = err
		return 0, pw.err
	}
	sz := uint64(fi.Size())

	if sz+minCompressionSaving > uncompressedSize {
		return 0, nil
	}
	if sz+(uncompressedSize>>minCompressionFraction) > uncompressedSize {
		return 0, nil
	}

	return pw.CopyFrom(in, fi)
}

func (pw *packWriter) CopyFrom(in *os.File, fi os.FileInfo) (uint64, error) {
	if pw.err != nil {
		return 0, pw.err
	}

	var off int64
	remain := fi.Size()
	for remain > 0 {
		var amt int
		if remain > (1 << 30) {
			amt = (1 << 30)
		} else {
			amt = int(remain)
		}

		amt, err := unix.Sendfile(int(pw.f.Fd()), int(in.Fd()), &off, amt)
		remain -= int64(amt)
		if err != nil {
			pw.err = fmt.Errorf("sendfile (copying data to "+
				"htpack): %v", err)
			return uint64(off), pw.err
		}
	}

	return uint64(off), nil
}
