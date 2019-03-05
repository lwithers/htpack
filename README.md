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
- build YAML file describing files to serve
- run htpacker tool to produce a single .htpack file
- create `htpack.Handler` pointing at .htpack file

Only the minimal header processing necessary for correctness (Content-Length,
etc.) is carried out by `htpack.Handler`; the handler can be combined with
middleware for further processing (adding headers, `http.StripPrefix`, etc.).
