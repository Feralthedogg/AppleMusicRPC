#![cfg(target_os = "macos")]

mod app;
mod artwork;
mod config;
mod discord;
mod model;
mod music;
mod native;
mod presence;
mod single_instance;

use anyhow::{Context, Result, bail};
use objc2::{MainThreadMarker, rc::autoreleasepool};
use serde_json::json;
use std::{
    env, fs,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64},
    },
    time::Duration,
};

fn run() -> Result<()> {
    let mut args = env::args().skip(1);
    let mut explicit = None;
    let mut diagnose = false;
    let mut status = false;
    let mut probe = false;
    let mut run_for = None;
    while let Some(argument) = args.next() {
        match argument.as_str() {
            "--version" | "-V" => {
                println!("AppleMusicRPC {} (Rust, macOS)", env!("CARGO_PKG_VERSION"));
                return Ok(());
            }
            "--help" | "-h" => {
                println!(
                    "AppleMusicRPC {}\n\nUsage: AppleMusicRPC [--config PATH] [--run-for SECONDS]\n       AppleMusicRPC --diagnose [--config PATH]\n       AppleMusicRPC --probe-discord [--config PATH]\n       AppleMusicRPC --status\n\n--diagnose reads Music and verifies its album cover without changing Discord.\n--probe-discord performs only the IPC handshake.\n--status reads the running app's status.json.",
                    env!("CARGO_PKG_VERSION")
                );
                return Ok(());
            }
            "--config" => {
                explicit = Some(PathBuf::from(
                    args.next().context("--config requires a path")?,
                ))
            }
            "--diagnose" => diagnose = true,
            "--status" => status = true,
            "--probe-discord" => probe = true,
            "--run-for" => {
                run_for = Some(Duration::from_secs(
                    args.next().context("--run-for requires seconds")?.parse()?,
                ))
            }
            _ => bail!("unknown argument: {argument}"),
        }
    }
    if status {
        println!("{}", fs::read_to_string(config::status_path()?)?);
        return Ok(());
    }
    let mtm = MainThreadMarker::new().context("app must start on the main thread")?;
    let (config, path) = config::Config::load(explicit.as_deref(), &music::locale_country(mtm))?;
    if probe {
        discord::Client::new(config.discord_app_id).connect()?;
        println!("Discord IPC handshake accepted");
        return Ok(());
    }
    let stop = Arc::new(AtomicBool::new(false));
    let signal_stop = stop.clone();
    ctrlc::set_handler(move || signal_stop.store(true, std::sync::atomic::Ordering::Relaxed))?;
    if diagnose {
        let provider = music::Provider::new(mtm)?;
        let track = provider.poll()?;
        if let Some(track) = track.filter(|t| t.status != model::Playback::Stopped) {
            let data = provider.artwork(&track)?;
            let fingerprint = data
                .as_deref()
                .map(artwork::Fingerprint::from_bytes)
                .transpose()?;
            let cancel = artwork::Cancel {
                generation: 1,
                current: Arc::new(AtomicU64::new(1)),
                stop,
            };
            let meta = if config.use_album_art {
                artwork::Resolver::new(&config, config::cache_path()?)?.resolve(
                    &track,
                    fingerprint.as_ref(),
                    &cancel,
                    true,
                )?
            } else {
                None
            };
            let after = provider.poll()?;
            let still_current = after
                .as_ref()
                .is_some_and(|current| current.key() == track.key());
            println!(
                "{}",
                serde_json::to_string_pretty(
                    &json!({"version": env!("CARGO_PKG_VERSION"), "config_path": path, "track": track, "native_artwork_bytes": data.as_ref().map(Vec::len), "artwork": meta, "still_current": still_current})
                )?
            );
        } else {
            println!("Music is stopped or unavailable");
        }
        return Ok(());
    }
    let Some(_lock) = single_instance::Lock::acquire()? else {
        eprintln!("AppleMusicRPC is already running");
        return Ok(());
    };
    app::run(config, path, mtm, stop, run_for)
}

fn main() {
    if let Err(error) = autoreleasepool(|_| run()) {
        eprintln!("AppleMusicRPC: {error:#}");
        std::process::exit(1);
    }
}
