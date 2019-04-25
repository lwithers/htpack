/*
Packserver is a standalone HTTP server that serves up one or more pack files.
*/
package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lwithers/htpack"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "packserver",
	Short: "packserver is an HTTP server which serves .htpack files",
	Long: `packserver can efficiently serve a pre-packed file tree over HTTP(S).
The files must first have been prepared using the ‘htpacker’ tool.

In order to use HTTPS, specify the --key (or -k) flag. This should name a
PEM-encoded key file. This file may also contain the certificate; if not, then
pass the --cert (or -c) flag in addition.

Pack files may be specified as "/prefix=file", or just as "file" (which implies
"/=file"). Any /prefix present in the request URL will be stripped off before
searching the .htpack for the named file. Only one .htpack file can be served
at a particular prefix, and serving matches the longest (most specific)
prefixes first.`,
	RunE: run,
}

func main() {
	rootCmd.Flags().StringP("bind", "b", ":8080",
		"Address to listen on / bind to")
	rootCmd.Flags().StringP("key", "k", "",
		"Path to PEM-encoded HTTPS key")
	rootCmd.Flags().StringP("cert", "c", "",
		"Path to PEM-encoded HTTPS cert")
	rootCmd.Flags().StringSliceP("header", "H", nil,
		"Extra headers; use flag once for each, in form -H header=value")
	rootCmd.Flags().String("header-file", "",
		"Path to text file containing one line for each header=value to add")
	rootCmd.Flags().String("index-file", "",
		"Name of index file (index.html or similar)")
	rootCmd.Flags().Duration("expiry", 0,
		"Tell client how long it can cache data for; 0 means no caching")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c *cobra.Command, args []string) error {
	bindAddr, err := c.Flags().GetString("bind")
	if err != nil {
		return err
	}

	// parse TLS arguments
	keyFile, err := c.Flags().GetString("key")
	if err != nil {
		return err
	}
	certFile, err := c.Flags().GetString("cert")
	if err != nil {
		return err
	}
	switch {
	case keyFile == "" && certFile == "":
		// nothing to do
	case keyFile == "":
		return errors.New("cannot specify --cert without --key")
	case certFile == "":
		certFile = keyFile
	}

	// parse extra headers
	extraHeaders := make(http.Header)
	hdrs, err := c.Flags().GetStringSlice("header")
	if err != nil {
		return err
	}
	for _, hdr := range hdrs {
		pos := strings.IndexRune(hdr, '=')
		if pos == -1 {
			return fmt.Errorf("header %q must be in form "+
				"name=value", hdr)
		}
		extraHeaders.Add(hdr[:pos], hdr[pos+1:])
	}

	hdrfile, err := c.Flags().GetString("header-file")
	if err != nil {
		return err
	}
	if err := loadHeaderFile(hdrfile, extraHeaders); err != nil {
		fmt.Fprintln(os.Stderr, "--header-file:", err)
		os.Exit(1)
	}

	// parse expiry time
	//  NB: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control
	expiry, err := c.Flags().GetDuration("expiry")
	if err != nil {
		return err
	}
	if expiry <= 0 {
		extraHeaders.Set("Cache-Control", "no-store")
	} else {
		extraHeaders.Set("Cache-Control",
			fmt.Sprintf("public, max-age=%d", expiry/1e9))
	}

	// optional index file
	//  NB: this is set below, as the handlers are instantiated
	indexFile, err := c.Flags().GetString("index-file")
	if err != nil {
		return err
	}

	// verify .htpack specifications
	if len(args) == 0 {
		return errors.New("must specify one or more .htpack files")
	}

	packPaths := make(map[string]string)
	for _, arg := range args {
		prefix, packfile := "/", arg
		if pos := strings.IndexRune(arg, '='); pos != -1 {
			prefix, packfile = arg[:pos], arg[pos+1:]
		}

		prefix = filepath.Clean(prefix)
		if prefix[0] != '/' {
			return fmt.Errorf("%s: prefix must start with '/'", arg)
		}

		if other, used := packPaths[prefix]; used {
			return fmt.Errorf("%s: prefix %q already used by %s",
				arg, prefix, other)
		}
		packPaths[prefix] = packfile
	}

	// load packfiles, registering handlers as we go
	for prefix, packfile := range packPaths {
		packHandler, err := htpack.New(packfile)
		if err != nil {
			return err
		}
		if indexFile != "" {
			packHandler.SetIndex(indexFile)
		}

		handler := &addHeaders{
			extraHeaders: extraHeaders,
			handler:      packHandler,
		}

		if prefix != "/" {
			http.Handle(prefix+"/",
				http.StripPrefix(prefix, handler))
		} else {
			http.Handle("/", handler)
		}
	}

	// main server loop
	if keyFile == "" {
		err = http.ListenAndServe(bindAddr, nil)
	} else {
		err = http.ListenAndServeTLS(bindAddr, certFile, keyFile, nil)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return nil
}

func loadHeaderFile(hdrfile string, extraHeaders http.Header) error {
	if hdrfile == "" {
		return nil
	}

	f, err := os.Open(hdrfile)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var lineNum int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lineNum++
		if line == "" {
			continue
		}

		pos := strings.IndexRune(line, '=')
		if pos == -1 {
			return fmt.Errorf("%s: line %d: not in form "+
				"header=value", hdrfile, lineNum)
		}
		extraHeaders.Add(line[:pos], line[pos+1:])
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: %v", hdrfile, err)
	}
	return nil
}

type addHeaders struct {
	extraHeaders http.Header
	handler      http.Handler
}

func (ah *addHeaders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for name, values := range ah.extraHeaders {
		w.Header()[name] = append(w.Header()[name], values...)
	}
	ah.handler.ServeHTTP(w, r)
}
