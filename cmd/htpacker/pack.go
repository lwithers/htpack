package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/lwithers/htpack/packer"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v2"
)

var packCmd = &cobra.Command{
	Use:   "pack",
	Short: "creates a packfile from a YAML spec or set of files/dirs",
	RunE: func(c *cobra.Command, args []string) error {
		spec, err := c.Flags().GetString("spec")
		if err != nil {
			return err
		}

		if spec == "" {
			if len(args) == 0 {
				return errors.New("need --yaml, " +
					"or one or more filenames")
			}
			err = PackFiles(c, args)
		} else {
			if len(args) != 0 {
				return errors.New("cannot specify files " +
					"when using --yaml")
			}
			err = PackSpec(c, spec)
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

func PackFiles(c *cobra.Command, args []string) error {
	// TODO
	return errors.New("not implemented yet")
}

func PackSpec(c *cobra.Command, spec string) error {
	raw, err := ioutil.ReadFile(spec)
	if err != nil {
		return err
	}

	var ftp packer.FilesToPack
	if err := yaml.UnmarshalStrict(raw, &ftp); err != nil {
		return fmt.Errorf("parsing YAML spec %s: %v", spec, err)
	}

	// TODO: chdir

	out, _ := c.Flags().GetString("out")
	return packer.Pack(ftp, out)
}
