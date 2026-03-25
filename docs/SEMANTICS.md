# Filesystem semantics

NeoFS is an object store, so some POSIX filesystem operations are implemented with best-effort semantics.

## Paths and directories

- The mount root (`/`) lists **all containers** (by container ID).
- Inside a container, “directories” are **virtual** and inferred from object `FilePath` attributes using `/` separators.

## Writes

- **Create/overwrite** is implemented as **upload-on-close**: data is written to a local temp file and uploaded to NeoFS when the file handle is released.
- Overwrite is **best-effort**: we attempt to delete older objects with the same `FilePath`, but this is not atomic.

## Delete

- `unlink` deletes NeoFS objects whose `FilePath` exactly matches the path.

## Rename

- Rename is implemented as **copy+delete** (not atomic). If the copy succeeds but delete fails, you may temporarily see both paths.
- Cross-container rename returns `EXDEV`.

## Metadata

### Extended attributes (xattr)

- NeoFS object attributes are exposed as xattrs on files:
  - `user.neofs.<AttributeKey>` -> `<AttributeValue>`
- This is **best-effort** and requires a network call (`ObjectHead`) when accessed.

