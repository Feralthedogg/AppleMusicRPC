use crate::{
    artwork::{Cancel, Fingerprint, Meta, Resolver},
    config::{self, Config},
    discord,
    model::{Playback, Track},
    music, native,
    presence::{self, Timeline},
};
use anyhow::Result;
use objc2::{MainThreadMarker, rc::autoreleasepool};
use serde::Serialize;
use serde_json::Value;
use std::{
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc,
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

pub fn now() -> f64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs_f64()
}

struct Lookup {
    generation: u64,
    track: Track,
    reference: Option<Fingerprint>,
    bypass_cache: bool,
}
struct LookupResult {
    generation: u64,
    result: Result<Option<Meta>>,
}

#[derive(Serialize)]
pub struct Status {
    version: &'static str,
    implementation: &'static str,
    pid: u32,
    running: bool,
    config_path: PathBuf,
    track: Option<Track>,
    native_artwork: bool,
    artwork_state: String,
    artwork: Option<Meta>,
    discord_connected: bool,
    discord_error: Option<String>,
    music_error: Option<String>,
    discord_acknowledged: Option<Value>,
    updated_at: u64,
}

pub fn run(
    config: Config,
    config_path: PathBuf,
    mtm: MainThreadMarker,
    stop: Arc<AtomicBool>,
    run_for: Option<Duration>,
) -> Result<()> {
    let provider = music::Provider::new(mtm)?;
    let retry = Arc::new(AtomicBool::new(false));
    let generation = Arc::new(AtomicU64::new(1));
    let mut resolver = Resolver::new(&config, config::cache_path()?)?;
    let (lookup_tx, lookup_rx) = mpsc::channel::<Lookup>();
    let (result_tx, result_rx) = mpsc::channel::<LookupResult>();
    let lookup_generation = generation.clone();
    let lookup_stop = stop.clone();
    let artwork_thread = thread::spawn(move || {
        while let Ok(mut request) = lookup_rx.recv() {
            for latest in lookup_rx.try_iter() {
                request = latest;
            }
            let cancel = Cancel {
                generation: request.generation,
                current: lookup_generation.clone(),
                stop: lookup_stop.clone(),
            };
            if cancel.check().is_err() {
                continue;
            }
            let result = resolver.resolve(
                &request.track,
                request.reference.as_ref(),
                &cancel,
                request.bypass_cache,
            );
            let _ = result_tx.send(LookupResult {
                generation: request.generation,
                result,
            });
        }
    });
    let rpc = discord::Service::start(
        config.discord_app_id.clone(),
        stop.clone(),
        config.force_refresh_ms,
    );
    let menu = native::Menu::new(mtm, stop.clone(), retry.clone());
    let status_path = config::status_path()?;
    let mut status = Status {
        version: env!("CARGO_PKG_VERSION"),
        implementation: "Rust",
        pid: std::process::id(),
        running: true,
        config_path,
        track: None,
        native_artwork: false,
        artwork_state: "waiting".into(),
        artwork: None,
        discord_connected: false,
        discord_error: None,
        music_error: None,
        discord_acknowledged: None,
        updated_at: 0,
    };
    let mut reference: Option<Fingerprint> = None;
    let mut timeline = Timeline::default();
    let mut next_poll = Instant::now();
    let mut next_lookup = Instant::now();
    let mut next_artwork = Instant::now();
    let mut next_status = Instant::now();
    let mut missing_since: Option<Instant> = None;
    let mut pending = false;
    let started = Instant::now();
    let mut last_sent: Option<Value> = None;
    eprintln!(
        "AppleMusicRPC {} (Rust) started; PID {}; storefronts {}",
        env!("CARGO_PKG_VERSION"),
        std::process::id(),
        config.countries().join(", ")
    );

    while !stop.load(Ordering::Relaxed)
        && run_for.is_none_or(|duration| started.elapsed() < duration)
    {
        let clock = Instant::now();
        let mut publish = false;
        if clock >= next_poll {
            next_poll = clock + Duration::from_millis(config.poll_ms);
            let poll = autoreleasepool(|_| provider.poll());
            let track = match poll {
                Ok(Some(track)) => {
                    missing_since = None;
                    status.music_error = None;
                    Some(track)
                }
                Ok(None) => {
                    let since = missing_since.get_or_insert(clock);
                    if since.elapsed() >= Duration::from_secs(12) {
                        Some(Track::default())
                    } else {
                        None
                    }
                }
                Err(error) => {
                    let message = error.to_string();
                    if status.music_error.as_ref() != Some(&message) {
                        eprintln!("Music: {message}");
                    }
                    status.music_error = Some(message);
                    let since = missing_since.get_or_insert(clock);
                    if since.elapsed() >= Duration::from_secs(12) {
                        Some(Track::default())
                    } else {
                        None
                    }
                }
            };
            if let Some(track) = track {
                if track.status == Playback::Stopped
                    || (track.status == Playback::Paused && !config.show_paused)
                {
                    if status.track.is_some() {
                        generation.fetch_add(1, Ordering::Relaxed);
                        status.track = None;
                        status.artwork = None;
                        status.native_artwork = false;
                        status.artwork_state = "waiting".into();
                        reference = None;
                        pending = false;
                        timeline = Timeline::default();
                        publish = true;
                    }
                } else {
                    let changed = status
                        .track
                        .as_ref()
                        .is_none_or(|previous| previous.key() != track.key());
                    let status_changed = status
                        .track
                        .as_ref()
                        .is_none_or(|previous| previous.status != track.status);
                    if changed {
                        generation.fetch_add(1, Ordering::Relaxed);
                        status.artwork = None;
                        status.discord_acknowledged = None;
                        reference = None;
                        status.native_artwork = false;
                        pending = false;
                        next_lookup = clock;
                        next_artwork = clock;
                        status.artwork_state = if config.use_album_art {
                            "searching"
                        } else {
                            "disabled"
                        }
                        .into();
                        eprintln!(
                            "Track: {} — {} / {} [{}]",
                            track.artist, track.title, track.album, track.persistent_id
                        );
                    }
                    publish |= timeline.update(&track, now(), changed) || changed || status_changed;
                    status.track = Some(track);
                }
            }
        }
        let force_retry = retry.swap(false, Ordering::Relaxed);
        if let Some(track) = &status.track
            && config.use_album_art
        {
            if force_retry {
                generation.fetch_add(1, Ordering::Relaxed);
                pending = false;
                next_lookup = clock;
                next_artwork = clock;
            }
            if clock >= next_artwork && (reference.is_none() || force_retry) {
                next_artwork = clock + Duration::from_secs(5);
                match autoreleasepool(|_| provider.artwork(track)) {
                    Ok(Some(data)) => match Fingerprint::from_bytes(&data) {
                        Ok(fingerprint) => {
                            if reference.as_ref() != Some(&fingerprint) {
                                reference = Some(fingerprint);
                                status.native_artwork = true;
                                generation.fetch_add(1, Ordering::Relaxed);
                                pending = false;
                                next_lookup = clock;
                                // A metadata-only cover must be reverified when Music supplies its image.
                                if status.artwork.is_some() {
                                    status.artwork = None;
                                    publish = true;
                                }
                            }
                        }
                        Err(error) => eprintln!("Music cover: {error}"),
                    },
                    Ok(None) => {}
                    Err(error) => eprintln!("Music cover: {error}"),
                }
            }
            if !pending && clock >= next_lookup && (status.artwork.is_none() || force_retry) {
                let request = Lookup {
                    generation: generation.load(Ordering::Relaxed),
                    track: track.clone(),
                    reference: reference.clone(),
                    bypass_cache: force_retry,
                };
                lookup_tx.send(request)?;
                status.artwork_state = "searching".into();
                pending = true;
                next_lookup = clock + Duration::from_secs(60);
            }
        }
        for result in result_rx.try_iter() {
            // Never let a late HTTP response attach one song's art to the next song.
            if result.generation != generation.load(Ordering::Relaxed) || status.track.is_none() {
                continue;
            }
            pending = false;
            match result.result {
                Ok(Some(meta)) => {
                    if meta.image_source == "mediaremote-artwork-callback" {
                        status.native_artwork = true;
                    }
                    eprintln!(
                        "Cover verified: {} / {} / track {} ({}; difference {:?})",
                        meta.album,
                        meta.country,
                        meta.track_id,
                        meta.verification,
                        meta.image_difference
                    );
                    status.artwork_state = meta.verification.clone();
                    status.artwork = Some(meta);
                    publish = true;
                }
                Ok(None) => {
                    status.artwork_state = "no verified match".into();
                    eprintln!("No verified album cover; keeping fallback artwork");
                }
                Err(error) => {
                    status.artwork_state = format!("lookup failed: {error}");
                    eprintln!("Album cover: {error}");
                }
            }
        }
        if publish {
            let activity = status.track.as_ref().map(|track| {
                presence::activity(&config, track, status.artwork.as_ref(), &timeline)
            });
            if activity != last_sent {
                rpc.sender
                    .send(discord::Request::Activity(activity.clone()))?;
                last_sent = activity;
            }
            next_status = clock;
        }
        for event in rpc.events.try_iter() {
            if event.error != status.discord_error
                && let Some(error) = &event.error
            {
                eprintln!("Discord: {error}");
            }
            status.discord_connected = event.connected;
            status.discord_error = event.error;
            if event.requested == last_sent {
                status.discord_acknowledged = event.acknowledged;
            }
            next_status = clock;
        }
        if clock >= next_status {
            next_status = clock + Duration::from_secs(5);
            status.updated_at = now() as u64;
            if let Err(error) = config::atomic_json(&status_path, &status) {
                eprintln!("Status file: {error}");
            }
            let track_text = status
                .track
                .as_ref()
                .map(|t| {
                    let prefix = if t.status == Playback::Paused {
                        "Paused · "
                    } else {
                        ""
                    };
                    format!("{prefix}{} — {}", t.artist, t.title)
                })
                .unwrap_or_else(|| "Waiting for Music…".into());
            menu.update(
                &track_text,
                if status.discord_connected {
                    "Discord: connected"
                } else {
                    "Discord: waiting for connection"
                },
                &format!("Album cover: {}", status.artwork_state),
            );
        }
        menu.pump();
    }
    stop.store(true, Ordering::Relaxed);
    generation.fetch_add(1, Ordering::Relaxed);
    let _ = rpc.sender.send(discord::Request::Quit);
    drop(lookup_tx);
    let _ = rpc.thread.join();
    let _ = artwork_thread.join();
    status.running = false;
    status.discord_connected = false;
    status.updated_at = now() as u64;
    status.discord_acknowledged = None;
    status.track = None;
    status.artwork = None;
    let _ = config::atomic_json(&status_path, &status);
    eprintln!("AppleMusicRPC stopped");
    Ok(())
}
