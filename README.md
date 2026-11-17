# dvm

`dvm` is a cross-platform .NET SDK version manager. It reads Microsoft's
official release metadata, downloads the matching SDK archive, verifies its
published SHA-512 hash, and extracts it into a dvm-managed directory. It can
also update installed major versions and remove superseded versions.

## Requirements

- Go 1.24 or later to build dvm.
- Network access when installing or updating.
- Linux, macOS, or Windows on x64 or ARM64.

## Build

```sh
go build -o dvm .
```

On Windows, the resulting executable is `dvm.exe`.

## Usage

```sh
dvm install 10
dvm install --preview 10
dvm update
dvm list
dvm uninstall 10
dvm uninstall 10.0.404
```

`install 10` installs the latest stable .NET 10 SDK and the runtimes provided
with it, then removes older .NET 10 SDK, runtime, and targeting-pack versions
in the dvm directory. `--preview` opts into the newest prerelease SDK listed
for that major version.

`update` updates every major version with an installed SDK to its latest
release and prunes superseded versions within those majors. Other major
versions remain installed. Preview releases are excluded unless `--preview`
is specified.

`uninstall 10` removes all .NET 10 SDKs, runtimes, host components, manifests,
and packs under the selected directory, including versions installed by other
tools. These components may be needed by apps or other SDKs. `uninstall
10.0.404` removes only that exact SDK and its dvm install marker; it leaves
shared runtimes, packs, and manifests in place. Both commands work offline and
leave other major versions and the shared `dotnet` host executable untouched.

The default install directory is `~/.dotnet` (the user's home directory on
each platform). Set `DVM_DIR` or pass `--dir PATH` to use another directory.
The tool only prunes versioned directories in the selected install directory;
it does not manage system-wide .NET installations.

The .NET host uses the standard `DOTNET_ROOT` and `PATH` environment variables
to locate and run SDKs. dvm does not change shell configuration.

dvm downloads SDK archives directly from the URLs in Microsoft's release
metadata. It does not invoke `dotnet-install` or require Bash or PowerShell.

## License

MIT. See [LICENSE](LICENSE).
