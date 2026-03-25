# Config file

`neofs-mount` can read defaults from a TOML config file.

## Default config path

- Linux: `$XDG_CONFIG_HOME/neofs-mount/config.toml` (or `$HOME/.config/neofs-mount/config.toml`)
- macOS: `$HOME/Library/Application Support/neofs-mount/config.toml`

## Override

Use `--config /path/to/config.toml`.

## Example `config.toml`

```toml
endpoint = "s03.neofs.devenv:8080"
# Either a path to a file containing WIF, or a raw WIF string directly:
wallet_key = "/path/to/wallet.key"
mountpoint = "/tmp/neofs"

read_only = false

# Optional:
cache_dir = "/tmp/neofs-cache"
cache_size = 1073741824 # 1GiB
log_level = "info" # debug|info|warn|error

# Optional: hide containers from the mount root by container ID
ignore_container_ids = [
  "CYzn4HPHXRmGW5cwvrqHtosd79kjBtVYAP3ykH6RCCAa",
]
```

For a copy/paste-ready template file in the repo, see:
- `config.toml.example`

## Merge rules

If a value is set in both the config file and CLI flags, the CLI flag wins.

