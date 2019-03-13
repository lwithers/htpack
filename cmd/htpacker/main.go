package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "htpacker",
	Short: "htpacker packs static files into a blob that can be served efficiently over HTTP",
	Long: `Creates .htpack files comprising one or more static assets, and
compressed versions thereof. A YAML specification of files to pack may be
provided or generated on demand; or files and directories can be listed as
arguments.`,
}

func main() {
	rootCmd.AddCommand(packCmd)
	//rootCmd.AddCommand(yamlCmd)
	rootCmd.AddCommand(inspectCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
