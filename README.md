# AppleMusicRPC
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](./LICENSE)

AppleMusicRPC is an Apple Music → Discord Rich Presence app.  
On Windows, it reads the track currently playing in Apple Music via SMTC. On macOS, it reads Music.app via AppleScript. The current track is shown in your Discord status as a Listening activity.

## Features

- **Runs in the system tray (background)**: Works from the notification area (system tray) after launch.
- **Shows now playing info**: Displays the current track/artist in Discord Rich Presence.
- **Auto-start support (installer option)**: Can register itself to start automatically when you sign in to Windows.
- **Clean install/uninstall**: Aims to provide MSI-based installation/removal, cleaning up startup entries and shortcuts on uninstall.
- **macOS app bundle**: Builds a menu bar `.app` that reads Music.app and talks to Discord through the local IPC socket.

## Installation

### Windows

- Install using the provided **MSI installer**.
- After installation, you can launch it from the Start Menu or from the installation folder.
- If auto-start is enabled, it will launch automatically after reboot/sign-in.

### macOS

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

## Configuration

- The app uses a `config.json` configuration file.
- Depending on how it’s built/installed, it will typically be placed in one of these locations:
  - `C:\ProgramData\Feralthedogg\AppleMusicRPC\config.json` (recommended)
  - `%AppData%\Feralthedogg\AppleMusicRPC\config.json`
  - `~/Library/Application Support/Feralthedogg/AppleMusicRPC/config.json`
  - `AppleMusicRPC.app/Contents/Resources/config.json`
  - Or the app install folder / development `dist/config.json` path (varies by build)

> Changes to the config are applied after restarting the app.

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

## Platform Status

- Windows: supported through SMTC and named-pipe Discord IPC.
- macOS: supported through Music.app AppleScript and Unix-socket Discord IPC.
- Android and other platforms: not implemented.

## License

[BSD 3-Clause License](./LICENSE).
