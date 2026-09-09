---
id: "0347"
title: Make Read bounded, cancellable, and safe for special or binary files
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §8 Tools and access policy
---

## Description
Replace Read's whole-file text allocation with bounded streaming, cancellation checks, special-file rejection, and binary metadata while preserving native image/PDF support.

## Acceptance criteria
- Use bounded/streaming reads; document source-byte and line limits.
- Reject unsupported non-regular files without blocking and provide a useful shell inspection alternative.
- Check cancellation and bound resources; validate the opened descriptor rather than relying on pre-open stat to prevent replacement races.
- Detect ordinary binary content and return size/type metadata, preserving supported native images/PDFs.
- Cover huge files, long lines, special files, cancellation, binaries, UTF-8/CRLF, and windowed reads.
- Coordinate future revision hashing with 0325 so partial reads remain memory-bounded and any extra full-file scan is explicit.

## Outcome
Implemented bounded streaming (8 MiB source scan, 64 KiB retained per line, 2,000 code points rendered per line, 2,000 returned lines, 128 KiB text output), bounded media/directory reads, nonblocking open plus descriptor validation, binary metadata, and cooperative cancellation. Spec documents that in-progress filesystem calls cannot be portably interrupted. Added the explicit streaming revision-scan constraint to 0325.

Focused tests cover the requested cases, including exact-buffer-size unterminated lines and cancellation during a source read. Tools tests, race tests, and vet passed; independent review accepted the revision. The full Go suite, tools race tests, and tools vet also passed against the isolated commit tree; a reviewer rerun in the shared tree encountered an unrelated session-test git cross-device-link failure.

Commit subject: Make Read bounded and safe for special and binary files.
