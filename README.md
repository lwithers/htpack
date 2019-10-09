# HTTP resource pack server

A common scenario is that you have a set of static resources that you want to
serve up quickly via HTTP (for example: stylesheets, WASM).

This package provides a `net/http`-compatible `http.Handler` to do so, with
support for:
- compression
  - gzip
  - brotli, if you have the external compression binary available at pack time
  - does not yet support Transfer-Encoding, only Accept-Encoding/Content-Encoding
- etags
- ranges

The workflow is as follows:
- (optional) build YAML file describing files to serve
- run htpacker tool to produce a single .htpack file
- create `htpack.Handler` pointing at .htpack file

The handler can easily be combined with middleware (`http.StripPrefix` etc.).

## Range handling notes

Too many bugs have been found with range handling and composite ranges, so the
handler only accepts a single range within the limits of the file. Anything
else will be ignored.

The interaction between range handling and compression also seems a little
ill-defined; as we have pre-compressed data, however, we can consistently
serve the exact same byte data for compressed files.
