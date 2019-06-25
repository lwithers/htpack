package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lwithers/htpack/cmd/htpacker/packer"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v2"
)

var yamlCmd = &cobra.Command{
	Use:   "yaml",
	Short: "Build YAML spec from list of files/dirs",
	Long: `Generates a YAML specification from a list of files and directories.
The specification is suitable for passing to pack.

File names will be mapped as follows:
 • if you specify a file, it will appear be served as "/filename";
 • if you specify a directory, its contents will be merged into "/", such that a
   directory with contents "a", "b", and "c/d" will cause entries "/a", "/b" and
   "/c/d" to be served.
`,
	RunE: func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.New("must specify one or more files/directories")
		}

		// convert "out" to absolute path, in case we need to chdir
		out, err := c.Flags().GetString("out")
		if err != nil {
			return err
		}
		out, err = filepath.Abs(out)
		if err != nil {
			return err
		}

		// chdir if required
		chdir, err := c.Flags().GetString("chdir")
		if err != nil {
			return err
		}
		if chdir != "" {
			if err = os.Chdir(chdir); err != nil {
				return err
			}
		}

		if err := MakeYaml(args, out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	yamlCmd.Flags().StringP("out", "O", "",
		"Output filename")
	yamlCmd.MarkFlagRequired("out")
	yamlCmd.Flags().StringP("chdir", "C", "",
		"Change to directory before searching for input files")
}

func MakeYaml(args []string, out string) error {
	ftp, err := filesFromList(args)
	if err != nil {
		return err
	}

	raw, err := yaml.Marshal(ftp)
	if err != nil {
		return fmt.Errorf("failed to marshal %T to YAML: %v", ftp, err)
	}

	return ioutil.WriteFile(out, raw, 0666)
}

func filesFromList(args []string) (packer.FilesToPack, error) {
	ftp := make(packer.FilesToPack)

	// NB: we don't use filepath.Walk since:
	//  (a) we don't care about lexical order; just do it quick
	//  (b) we want to dereference symlinks
	for _, arg := range args {
		if err := filesFromListR(arg, arg, ftp); err != nil {
			return nil, err
		}
	}
	return ftp, nil
}

func filesFromListR(prefix, arg string, ftp packer.FilesToPack) error {
	f, err := os.Open(arg)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	switch {
	case fi.Mode().IsDir():
		// readdir
		fnames, err := f.Readdirnames(0) // 0 ⇒ everything
		if err != nil {
			return err
		}
		for _, fname := range fnames {
			fullname := filepath.Join(arg, fname)
			if err = filesFromListR(prefix, fullname, ftp); err != nil {
				return err
			}
		}
		return nil

	case fi.Mode().IsRegular():
		// sniff content type
		buf := make([]byte, 512)
		n, err := f.Read(buf)
		if err != nil {
			return err
		}
		buf = buf[:n]
		ctype := http.DetectContentType(buf)

		// augmented rules for JS / CSS / etc.
		switch {
		case strings.HasPrefix(ctype, "text/plain"):
			switch filepath.Ext(arg) {
			case ".css":
				ctype = "text/css"
			case ".js":
				ctype = "application/javascript"
			case ".json":
				ctype = "application/json"
			case ".svg":
				ctype = "image/svg+xml"
			}

		case strings.HasPrefix(ctype, "text/xml"):
			switch filepath.Ext(arg) {
			case ".svg":
				ctype = "image/svg+xml"
			}
		}

		// pack
		srvName := strings.TrimPrefix(arg, prefix)
		if srvName == "" {
			srvName = filepath.Base(arg)
		}
		if !strings.HasPrefix(srvName, "/") {
			srvName = "/" + srvName
		}
		ftp[srvName] = packer.FileToPack{
			Filename:    arg,
			ContentType: ctype,
		}
		return nil

	default:
		return fmt.Errorf("%s: not file/dir (mode %x)", arg, fi.Mode())
	}
}
