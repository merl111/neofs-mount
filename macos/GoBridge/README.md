# Go ↔ File Provider C bridge

This module builds a **static archive** (`libneofsfp.a`) consumed by the Xcode targets in `../NeoFSMount/`.

```bash
cd macos/GoBridge
export CGO_ENABLED=1 GOOS=darwin GOARCH=arm64   # or amd64
go build -buildmode=c-archive -o libneofsfp.a .
```

The C API is declared in `../NeoFSMount/Shared/neofs_fp_api.h` and must stay aligned with `fp_export.go`.

Requires **Go on macOS** with **CGO** and **Xcode command-line tools** (for the Darwin `clang` toolchain).
