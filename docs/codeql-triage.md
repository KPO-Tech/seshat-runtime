# CodeQL: how its alerts were read

CodeQL (default setup, Go and the workflows; Rust is not supported by the default setup) was switched on on 2026-11-07 and opened 43 alerts
on `main`. They were read one by one the same day. This page says what was found, so that nobody has to read them again, and how to tell a new
alert from one that is already known.

## Fixed

- **`go/zipslip` and `go/unsafe-unzip-symlink`, `tui/internal/titlemodel/install.go`.** The archives of the title model are checked against a
  SHA-256 fixed in the program before they are opened, so a tampered download never gets here. The extraction was still weaker than it looked:
  a name was checked as text, and a link as text. A link to `.` followed by a link to `s/..` is inside the folder as text and outside on disk, and
  a file written through it lands outside. Now the folder that holds each entry is resolved with every link followed and must stay inside, a
  link is followed on disk where it exists, and an entry is never written over a link. The size of a zip entry is a field of the archive: a value
  above 2^63 wrapped around into a negative number and went through the limit; it is refused, and what is really written is counted against what
  is left. `extract_links_test.go` fails without each of these (the link tests need symbolic links, so they run on Linux and are skipped on a
  Windows account that cannot make them).
- **`go/incorrect-integer-conversion`, `cmd/grpc/main.go`.** `SESHAT_GRPC_MAX_CONCURRENT_RPCS` was converted to the unsigned 32-bit number the
  server takes; 5000000000 wrapped around to 705032704, and a multiple of 2^32 to 0. It is capped at `MaxInt32`.
- **`go/incomplete-url-scheme-check`, `internal/htmltext/converter.go`.** Only `javascript:` was dropped from a link. The text of a page is turned
  into Markdown that is shown to a person, so `data:`, `vbscript:` and `file:` were let through, and so was a scheme written with a tab inside
  (`jav&#x09;ascript:`, which a browser reads as `javascript:`). Only web, mail, phone and relative addresses are kept; the words of any other
  link stay.

## Not defects, kept as they are

- **`go/incorrect-integer-conversion` in `internal/pdfread` and `internal/pdftext` (24 alerts).** They convert numbers read from a PDF between
  integer sizes. The conversion from 64 bits to `int` loses nothing on every platform released (amd64 and arm64: see the build matrix of
  `release.yml`); the conversions to `uint32` and `uint16` of an object number are guarded by `int64(uint32(t1)) == t1` on the line before; the
  bytes of an object number written in `read.go` are the truncation the PDF encryption algorithm asks for. Truncating a number on a 32-bit build
  would give a wrong page number, not a memory error, and there is no 32-bit build.
- **`go/weak-sensitive-data-hashing` and `go/weak-cryptographic-algorithm`, `internal/pdfread/read.go` (8 alerts).** MD5 and RC4 are what the PDF
  standard security handler (ISO 32000) uses to open a file that is already encrypted. They are used to decrypt, never to protect anything new,
  and a reader that does not implement them cannot read the file.
- **`go/weak-sensitive-data-hashing`, `internal/pdfsmart/decrypt.go`.** SHA-256 of the password and the file, used as the key of an in-memory
  cache of unlocked files. It is not stored and not compared to anything.
- **`go/allocation-size-overflow` (4 alerts: `engine/state.go`, `tools/registry/policy.go`, `vector/chroma_store.go`, `runtimepath.go`).** The
  capacity is `len(a)+len(b)` of two slices or maps that already exist in memory; the sum cannot overflow an `int` because the two cannot hold
  that many elements.

## A new alert

Look at the alert's rule in the lists above first. If it is not there, or the file is a new one, it is new: read it as these were, and add it
here, fixed or kept with its reason.
