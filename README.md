# AppleMusicRPC
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](./LICENSE)

AppleMusicRPC is an Apple Music → Discord Rich Presence app.  
On Windows, the Go app reads Apple Music via SMTC. On macOS, the native Rust app reads Music.app through the macOS scripting API. The current track is shown in Discord as a Listening activity.

## Features

- **Runs in the system tray (background)**: Works from the notification area (system tray) after launch.
- **Shows now playing info**: Displays the current track/artist in Discord Rich Presence.
- **Auto-start support (installer option)**: Can register itself to start automatically when you sign in to Windows.
- **Clean install/uninstall**: Aims to provide MSI-based installation/removal, cleaning up startup entries and shortcuts on uninstall.
- **macOS app bundle**: Builds a menu bar `.app` that reads Music.app and talks to Discord through the local IPC socket.
- **Native macOS album covers**: Receives the current photo from Music's native artwork callback, finds the same image in Music's URL cache, and verifies the public image against that photo. Cover selection uses the picture itself rather than catalog IDs.
- **Resilient macOS playback**: Background artwork lookup, track-bound caching, stable progress timestamps, pause/seek handling, and automatic Discord reconnection.

## Installation

### Windows

- Install using the [Windows v1.0.1 MSI installer](https://github.com/Feralthedogg/AppleMusicRPC/releases/download/v1.0.1/AppleMusicRPC-1.0.1-x64.msi).
- After installation, you can launch it from the Start Menu or from the installation folder.
- If auto-start is enabled, it will launch automatically after reboot/sign-in.

### macOS

Requires macOS 12 or later. Building requires Rust 1.88 or later and Xcode Command Line Tools. The built app does not require Go, Rust, or a separate runtime.

Download the [macOS v2.0.1 app for Apple Silicon](https://github.com/Feralthedogg/AppleMusicRPC/releases/download/v2.0.1/AppleMusicRPC-2.0.1-macos-arm64.zip), unzip it, and move `AppleMusicRPC.app` to Applications. This release uses ad hoc signing and is not notarized by Apple. Intel Macs can build the app from source.

Build the app bundle:

```sh
./scripts/build-macos.sh
```

Then open:

```sh
open mac/dist/AppleMusicRPC.app
```

macOS may ask for permission to control **Music**. Allow it so AppleMusicRPC can read the current track.

To keep it running in the background and start it automatically on login:

```sh
./scripts/install-macos-launchagent.sh
```

To remove the background auto-start service:

```sh
./scripts/uninstall-macos-launchagent.sh
```

To update a running app while preserving its configuration and login service:

```sh
./scripts/update-macos.sh
```

The updater finishes compiling and signing before stopping the old process. It saves the previous app under `~/Library/Application Support/Feralthedogg/AppleMusicRPC/backups`, installs the new bundle, and starts it. Menu-bar **Quit** clears Discord presence and stays quit; the login service restarts the app after a crash or at the next login.

The macOS source is in `mac/src`; Windows continues to use `windows/`.

Create a release ZIP and SHA-256 checksum for the build machine's architecture:

```sh
./scripts/package-macos-release.sh
```

## Configuration

- The app uses a `config.json` configuration file.
- Depending on how it’s built/installed, it will typically be placed in one of these locations:
  - `C:\ProgramData\Feralthedogg\AppleMusicRPC\config.json` (recommended)
  - `%AppData%\Feralthedogg\AppleMusicRPC\config.json`
  - `~/Library/Application Support/Feralthedogg/AppleMusicRPC/config.json`
  - `AppleMusicRPC.app/Contents/Resources/config.json`
  - Or the app install folder / development `dist/config.json` path (varies by build)

> Changes to the config are applied after restarting the app.

macOS retains the existing JSON fields. `large_text` is ignored on macOS so Discord displays the album only once, in the artist/album line. `country` selects the Apple storefront; an empty value uses the macOS locale. `fallback_countries` defaults to `["KR", "US", "JP", "GB"]` for releases unavailable in the first storefront. No IP geolocation service is required. Set `AMRPC_CONFIG` or pass `--config PATH` to select a configuration explicitly.

The cover reader calls `MRNowPlayingRequest.requestNowPlayingItemArtworkWithCompletion:` and receives `MRArtwork.imageData` through the system's Apple-signed `osascript` host. Photos stay on the Mac. Music's image URL caches are opened read-only; matching HEIC images are decoded with Apple's image codec, and only verified public Apple image URLs are sent to Discord. This private native API may change with macOS updates; the Music scripting artwork getter remains a fallback.

When the exact photo is absent from Music's URL cache, the resolver compares Apple search images against the actual photo. When neither native reader can supply a photo, it requires exact album metadata. Local/custom covers with no matching public Apple URL use `large_image` (or omit the image when it is empty). The menu's **Retry album cover** bypasses the resolved-cover cache. The Apple Music button is included only when a matching track link is known.

## Usage

1. Start Discord.
2. Play music in Apple Music.
3. Check that AppleMusicRPC appears under your Discord profile activity.

## Troubleshooting

- **Nothing shows up on Discord**
  - Make sure Discord is running.
  - Make sure Discord’s “Activity Status” is enabled.
  - On macOS, make sure AppleMusicRPC has Automation permission for Music in System Settings.
- **Multiple instances are running**
  - Open Task Manager, check for multiple `AppleMusicRPC.exe` processes, end them, then relaunch.
  - On macOS, quit `AppleMusicRPC` from the menu bar item and relaunch.
- **Config file won’t save**
  - Writing to `ProgramData` may require proper permissions (ACL). The installer build is intended to configure this correctly.

macOS diagnostics:

```sh
# Read the current track and verify artwork without publishing to Discord.
mac/dist/AppleMusicRPC.app/Contents/MacOS/AppleMusicRPC --diagnose

# Check Discord's IPC handshake without changing presence.
mac/dist/AppleMusicRPC.app/Contents/MacOS/AppleMusicRPC --probe-discord

# Show the running app's track, verified artwork, and Discord acknowledgement.
mac/dist/AppleMusicRPC.app/Contents/MacOS/AppleMusicRPC --status

cargo test --manifest-path mac/Cargo.toml --locked
cargo clippy --manifest-path mac/Cargo.toml --locked --all-targets -- -D warnings
```

Logs are in `~/Library/Logs/AppleMusicRPC/launchd.err.log`, status in `~/Library/Application Support/Feralthedogg/AppleMusicRPC/status.json`, resolved covers in `~/Library/Caches/Feralthedogg/AppleMusicRPC/artwork-v3.json`, and the local Music image index in the same cache folder. The status includes `image_source` and `verification` for checking whether a cover came from the native callback and Music cache. Music Automation permission is required; if macOS prompts after an update, allow access to Music.

## Platform Status

- Windows: supported through SMTC and named-pipe Discord IPC.
- macOS: native Rust menu-bar app, Music.app scripting and original-artwork verification, Unix-socket Discord IPC.
- Android and other platforms: not implemented.

## License

[BSD 3-Clause License](./LICENSE).
