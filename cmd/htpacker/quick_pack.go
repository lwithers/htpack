package main

import (
	"fmt"
	"io/ioutil"
	"os"

	yaml "gopkg.in/yaml.v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := ioutil.ReadFile("in.yaml")
	if err != nil {
		return err
	}

	var ftp FilesToPack
	if err := yaml.UnmarshalStrict(raw, &ftp); err != nil {
		return err
	}

	return Pack(ftp, "out.htpack")
}
