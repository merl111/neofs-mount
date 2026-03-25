# Release checklist

This project does not assume git tags or CI. A manual release can be produced locally.

## Build binaries

```bash
make tidy
make test
make dist VERSION=0.1.0
```

Artifacts will be placed in `dist/` and checksums in `dist/SHA256SUMS`.

## Notes (macOS)

End users must install macFUSE to mount via FUSE.

