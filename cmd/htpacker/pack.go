package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/lwithers/htpack/cmd/htpacker/packer"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v2"
)

var packCmd = &cobra.Command{
	Use:   "pack",
	Short: "creates a packfile from a YAML spec or set of files/dirs",
	RunE: func(c *cobra.Command, args []string) error {
		// convert "out" to an absolute path, so that it will still
		// work after chdir
		out, err := c.Flags().GetString("out")
		if err != nil {
			return err
		}
		out, err = filepath.Abs(out)
		if err != nil {
			return err
		}

		// if "spec" is present, convert to an absolute path
		spec, err := c.Flags().GetString("spec")
		if err != nil {
			return err
		}
		if spec != "" {
			spec, err = filepath.Abs(spec)
			if err != nil {
				return err
			}
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

		// if "spec" is not present, then we expect a list of input
		// files, and we'll build a spec from them
		if spec == "" {
			if len(args) == 0 {
				return errors.New("need --yaml, " +
					"or one or more filenames")
			}
			err = PackFiles(c, args, out)
		} else {
			if len(args) != 0 {
				return errors.New("cannot specify files " +
					"when using --yaml")
			}
			err = PackSpec(c, spec, out)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		return nil
	},
}

func init() {
	packCmd.Flags().StringP("out", "O", "",
		"Output filename")
	packCmd.MarkFlagRequired("out")
	packCmd.Flags().StringP("spec", "y", "",
		"YAML specification file (if not present, just pack files)")
	packCmd.Flags().StringP("chdir", "C", "",
		"Change to directory before searching for input files")
}

func PackFiles(c *cobra.Command, args []string, out string) error {
	ftp, err := filesFromList(args)
	if err != nil {
		return err
	}
	return packer.Pack(ftp, out)
}

func PackSpec(c *cobra.Command, spec, out string) error {
	raw, err := ioutil.ReadFile(spec)
	if err != nil {
		return err
	}

	var ftp packer.FilesToPack
	if err := yaml.UnmarshalStrict(raw, &ftp); err != nil {
		return fmt.Errorf("parsing YAML spec %s: %v", spec, err)
	}

	return packer.Pack(ftp, out)
}
