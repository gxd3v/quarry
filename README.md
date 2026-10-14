# Quarry

A desktop database IDE for PostgreSQL, Oracle, MongoDB and Redis, built with Go, Wails and Svelte.

- **Explorer and editing:** a schema explorer, a SQL/mongosh/Redis editor with autocomplete, and results in a fast virtual grid.
- **Table views:** filters, server-side ordering, inline editing applied in one transaction, and streamed CSV/JSON/SQL export and CSV import.
- **Plans and terminals:** visual EXPLAIN plans, LISTEN/NOTIFY, and an embedded terminal running psql, sqlplus, mongosh or redis-cli.
- **Connections:** SSH tunnels, production safeguards, read-only connections, and passwords in the system keyring.
- **Look and updates:** Catppuccin themes and automatic updates.

## Install

### Quick install

**Linux (x86_64) and macOS**

```sh
curl -fsSL https://raw.githubusercontent.com/gxd3v/quarry/main/install.sh | sh
```

| Platform | Location | Also adds |
|---|---|---|
| Linux | `~/.local/share/quarry` | a `quarry` command in `~/.local/bin` and an app menu entry |
| macOS | `~/Applications/Quarry.app` | |

**Windows (x64)**, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/gxd3v/quarry/main/install.ps1 | iex
```

This installs to `%LOCALAPPDATA%\Programs\Quarry` and adds Quarry to the Start menu.

Neither installer needs administrator rights. Both verify the download against the release's `SHA256SUMS`. Running an installer again updates an existing install.

Options, as environment variables:

| Variable | Effect |
|---|---|
| `QUARRY_VERSION` | release tag to install, e.g. `v0.1.0` (default: latest) |
| `QUARRY_INSTALL_DIR` | install folder |
| `QUARRY_NO_SHORTCUT=1` | Windows only: no Start menu shortcut |

### Manual install

Every [release](https://github.com/gxd3v/quarry/releases/latest) has these files:

| Platform | File | Steps |
|---|---|---|
| Debian, Ubuntu | `quarry_<version>_amd64.deb` | `sudo apt install ./quarry_<version>_amd64.deb` |
| Fedora, RHEL, openSUSE | `quarry-<version>.x86_64.rpm` | `sudo dnf install ./quarry-<version>.x86_64.rpm` |
| Any Linux | `Quarry-x86_64.AppImage` | `chmod +x Quarry-x86_64.AppImage && ./Quarry-x86_64.AppImage` |
| Any Linux | `quarry-linux-amd64.tar.gz` | Extract, then run `./quarry`. |
| Windows | `quarry-windows-amd64-installer.exe` | Run the installer (installs for all users). |
| Windows | `quarry-windows-amd64.zip` | Extract, then run `quarry.exe` (portable). |
| macOS | `quarry-macos-universal.zip` | Extract and move `Quarry.app` to Applications. |

### Requirements

- **Linux:** GTK 3 and WebKitGTK 4.1.

  | Distribution | Command |
  |---|---|
  | Debian, Ubuntu | `sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0` |
  | Fedora | `sudo dnf install gtk3 webkit2gtk4.1` |
  | Arch | `sudo pacman -S gtk3 webkit2gtk-4.1` |

  The `.deb` and `.rpm` packages pull these in themselves. The AppImage also needs FUSE (`libfuse2`), or it can be run with `--appimage-extract-and-run`.
- **Windows 10 or 11:** the WebView2 runtime, which is built into Windows 11 and recent Windows 10 updates.
- **macOS 11 or later.** The app is not notarized. The quick installer clears the quarantine flag. A manual install needs a right-click on the app, then **Open**, the first time.
- **Native CLIs (optional):** the embedded terminal runs `psql`, `sql`/`sqlplus`, `mongosh` or `redis-cli` from `PATH`. The `shell_path` connection option points to another location.

## Updates

Quarry checks for a new release every 30 seconds. When one is out, it shows a small notice in the status bar, with the release notes and **Update and restart**.

The update replaces the app, restarts it and restores the open tabs, scripts and connections. Copies installed with a package manager (`.deb`, `.rpm`) are updated through that package manager.

## Uninstall

| Platform | Remove |
|---|---|
| Linux (quick install) | `~/.local/share/quarry`, `~/.local/bin/quarry` and `~/.local/share/applications/quarry.desktop` |
| Windows (quick install) | `%LOCALAPPDATA%\Programs\Quarry` and the Start menu shortcut |
| macOS | `Quarry.app` |

Settings and connections live in the user configuration folder; history and the saved session live in the state folder:

| Platform | Configuration | State |
|---|---|---|
| Linux | `~/.config/quarry` | `~/.local/state/quarry` |
| Windows | `%APPDATA%\quarry` | `%LOCALAPPDATA%\quarry` |
| macOS | `~/Library/Application Support/quarry` | its `state` subfolder |

## Development

Requirements: Go 1.27, Node 24, Wails v2 (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0`) and, on Linux, `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`.

```sh
wails dev                          # run with hot reload
go vet ./... && go test ./...      # backend checks
cd frontend && npm run check && npx vitest run
```

On Linux, add `-tags webkit2_41` to `wails dev` and `wails build`.

## License

[MIT](LICENSE)
