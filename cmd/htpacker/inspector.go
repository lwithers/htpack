package main

import (
	"fmt"
	"os"

	"github.com/lwithers/htpack/internal/packed"
)

// Inspect a packfile.
//  TODO: verify etag; verify integrity of compressed data.
//  TODO: skip Gzip/Brotli if not present; print ratio.
func Inspect(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr, dir, err := packed.Load(f)
	if hdr != nil {
		fmt.Printf("Header: %#v\n", hdr)
	}
	if dir != nil {
		fmt.Printf("%d files:\n", len(dir.Files))
		for path, info := range dir.Files {
			fmt.Printf(" • %s\n"+
				"    · Etag:         %s\n"+
				"    · Content type: %s\n"+
				"    · Uncompressed: %s (offset %d)\n"+
				"    · Gzipped:      %s (offset %d)\n"+
				"    · Brotli:       %s (offset %d)\n",
				path, info.Etag, info.ContentType,
				printSize(info.Uncompressed.Length), info.Uncompressed.Offset,
				printSize(info.Gzip.Length), info.Gzip.Offset,
				printSize(info.Brotli.Length), info.Brotli.Offset,
			)
		}
	}
	return err
}

func printSize(size uint64) string {
	switch {
	case size < 1<<10:
		return fmt.Sprintf("%d bytes", size)
	case size < 1<<15:
		return fmt.Sprintf("%.2f KiB", float64(size)/(1<<10))
	case size < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	case size < 1<<25:
		return fmt.Sprintf("%.2f MiB", float64(size)/(1<<20))
	case size < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	case size < 1<<35:
		return fmt.Sprintf("%.2f GiB", float64(size)/(1<<30))
	default:
		return fmt.Sprintf("%.1f GiB", float64(size)/(1<<30))
	}
}
