# netgcfg

Decrypt and re-encrypt Netgear router configuration backups (`.cfg`). netgcfg
handles both common Netgear backup formats and auto-detects which one a file uses:

- **Orbi / older devices** — the XOR-cipher scheme (`photos.tar`-wrapped or raw
  header), with config stored as `key=value` pairs.
- **RAX-series and other Broadcom devices** (RAX29, RAX30, R6700/R7000-family,
  etc.) — a small binary container holding an LZW-compressed Broadcom
  `DslCpeConfig` XML tree.

Decrypt a backup to a readable, editable JSON wrapper, edit it, then re-encode it
back into a `.cfg` you can restore to the device.

## Building

You need [Go](https://go.dev/doc/install). Then:

```
git clone https://github.com/ruca-radio/netgcfg
cd netgcfg
go build
```

This builds a `netgcfg` binary in the project directory.

## Usage

### Decrypt

```
./netgcfg -decrypt NETGEAR_RAX29.cfg -out decrypted.json
```

The format is detected automatically. The `config` object holds your device
settings; the `metadata` object **should not be modified** — it carries the
information netgcfg needs to re-encode the file (see
[Wrapper Format](#wrapper-format)).

### Encrypt

After editing `decrypted.json`:

```
./netgcfg -encrypt decrypted.json -out NETGEAR_RAX29_modified.cfg
```

Restore the result to your device from the same web UI you used to back it up.

> **Warning:** editing a config and restoring it can leave your device
> unreachable if a value is wrong. Make a note of your current settings before
> you change anything, and test on a device you can recover easily.

## RAX-series and other Broadcom devices

For Broadcom devices the config is XML, and the JSON wrapper looks like this:

```json
{
    "metadata": {
        "format": "broadcom-lzw",
        "header": "000200203222...",
        "model": "RAX30",
        "firmware": "V1.0.0.0",
        "firmware_major": "V1.0"
    },
    "config": {
        "DslCpeConfig": {
            "@attrs": { "version": "3.0", "bdkConfigIdent": "" },
            "Device": { "..." : "..." }
        }
    }
}
```

- `config` is the XML converted to a JSON object tree. Repeated elements become
  arrays; an element's attributes are stored under an `@attrs` object; its text
  value (when it also has attributes) under `#text`.
- `-raw` stores the decompressed payload verbatim in a Base64 `config_raw` field
  instead. This re-encodes to a byte-identical file.
- `metadata.header` is the container's opaque 40-byte header, which includes a
  32-byte signature netgcfg cannot recompute, so it is preserved verbatim. The
  payload length and JAMCRC checksum are recomputed automatically.

## Wrapper Format

netgcfg decrypts configs into a JSON wrapper rather than the raw binary. For
Orbi-style devices the default wrapper looks like:

```json
{
    "metadata": {
        "header_offset": 655360,
        "stated_magic": 538120740,
        "real_magic": 538120740,
        "rng": "uclibc"
    },
    "config": {
        "qos_list60": "Unreal-Tourment 1 Unreal-Tourment 1 UDP 7777,27960 7783,27960 ---- ----"
    }
}
```

The wrapper is structured this way for two reasons:

1. The metadata would be cumbersome to pass on the CLI every time, so it is
   baked into the wrapper.
2. A JSON config is less error-prone to edit than a binary file, and syntax
   errors are caught before you flash anything to the device.

### Raw Mode

Use `-raw` to keep the decrypted config as raw bytes in a Base64 `config_raw`
field instead of a JSON dictionary. To edit it, decode it
(`jq -r .config_raw < decrypted.json | base64 -d > config_decoded`), edit, then
Base64-encode and put it back in `config_raw`.

## FAQ

**Q. netgcfg isn't working or is returning an error. Help!**

A. Universal device support is hard — some devices have quirks (likely bugs in
Netgear's own code) that need manual investigation. [Open an
issue](https://github.com/ruca-radio/netgcfg/issues/new) with the exact command
you ran, the error message, your device model, and its firmware version.

**Q. I fixed a bug or added a device. How do I contribute?**

A. [Open a pull request](https://github.com/ruca-radio/netgcfg/pulls).

## Credits

netgcfg is a fork of [**Fysac/orbicfg**](https://github.com/Fysac/orbicfg), which
reverse-engineered and implemented the Orbi XOR-cipher format. All of the Orbi
code here — the `cfg` package, the `rand` (uClibc/musl) implementations, and the
JSON wrapper design — is Fysac's work.

The RAX/Broadcom support added in this fork builds on:

- [**joeatwork/python-lzw**](https://github.com/joeatwork/python-lzw) — the
  reference implementation of the TIFF-style LZW variant these devices use.
- [**abrazalaweb.net**](https://abrazalaweb.net/2018/11/descomprimir-archivos-de-configuracion-lzw) —
  who documented the LZW + JAMCRC format for Broadcom router configs.
- [**iscilyas/zyxel-modem-backup-extract**](https://github.com/iscilyas/zyxel-modem-backup-extract) —
  for confirming the container structure on related Broadcom devices.

## Encryption / compression schemes

**Orbi-style devices** encrypt the raw `key=value` config with an
[XOR cipher](https://en.wikipedia.org/wiki/XOR_cipher), seeding uClibc's (or
musl's, on newer devices) [`rand(3)`](https://man7.org/linux/man-pages/man3/rand.3.html)
with a hardcoded integer stored in the backup header.

**Broadcom (RAX-series) devices** wrap their XML config in a small TLV container
and LZW-compress it (TIFF-style codes, MSB-first, 9→12 bits, table flushed at
4094 entries), then attach a JAMCRC (`^crc32`) of the compressed payload.
