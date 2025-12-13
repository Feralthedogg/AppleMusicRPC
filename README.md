# AppleMusicRPC
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](./LICENSE)

AppleMusicRPC is an Apple Music → Discord Rich Presence app.  
On Windows, it reads the track currently playing in Apple Music and shows it in your Discord status (Listening).

## Features

- **Runs in the system tray (background)**: Works from the notification area (system tray) after launch.
- **Shows now playing info**: Displays the current track/artist in Discord Rich Presence.
- **Auto-start support (installer option)**: Can register itself to start automatically when you sign in to Windows.
- **Clean install/uninstall**: Aims to provide MSI-based installation/removal, cleaning up startup entries and shortcuts on uninstall.

## Installation

- Install using the provided **MSI installer**.
- After installation, you can launch it from the Start Menu or from the installation folder.
- If auto-start is enabled, it will launch automatically after reboot/sign-in.

## Configuration

- The app uses a `config.json` configuration file.
- Depending on how it’s built/installed, it will typically be placed in one of these locations:
  - `C:\ProgramData\Feralthedogg\AppleMusicRPC\config.json` (recommended)
  - Or the app install folder / user AppData path (varies by build)

> Changes to the config are applied after restarting the app.

## Usage

1. Start Discord.
2. Play music in Apple Music.
3. Check that AppleMusicRPC appears under your Discord profile activity.

## Troubleshooting

- **Nothing shows up on Discord**
  - Make sure Discord is running.
  - Make sure Discord’s “Activity Status” is enabled.
- **Multiple instances are running**
  - Open Task Manager, check for multiple `AppleMusicRPC.exe` processes, end them, then relaunch.
  - Single-instance (mutex) behavior may vary depending on the build/installer configuration.
- **Config file won’t save**
  - Writing to `ProgramData` may require proper permissions (ACL). The installer build is intended to configure this correctly.

## Roadmap

Windows is the primary target for now.  
In the future, support will be expanded so it can be used across **Android, macOS, and other platforms**.

## License

[BSD 3-Clause License](./LICENSE).
