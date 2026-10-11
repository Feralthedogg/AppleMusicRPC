use crate::{
    artwork::Cancel,
    model::{Track, normalized},
};
use anyhow::{Context, Result, bail};
use base64::{Engine, engine::general_purpose::STANDARD};
use serde::Deserialize;
use std::{
    io::Read,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

const SCRIPT: &str = include_str!("now_playing.js");

#[derive(Deserialize)]
struct Reply {
    title: String,
    artist: String,
    album: String,
    image_base64: String,
}

impl Reply {
    fn is_bound_to(&self, track: &Track) -> bool {
        !track.title.is_empty()
            && !track.artist.is_empty()
            && normalized(&self.title) == normalized(&track.title)
            && normalized(&self.artist) == normalized(&track.artist)
            && normalized(&self.album) == normalized(&track.album)
    }
}

pub fn read_artwork(track: &Track, cancel: &Cancel) -> Result<Option<Vec<u8>>> {
    cancel.check()?;
    let mut child = Command::new("/usr/bin/osascript")
        .args(["-l", "JavaScript", "-e", SCRIPT])
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .context("starting native artwork callback host")?;
    let stdout = child
        .stdout
        .take()
        .context("native artwork stdout is missing")?;
    // Drain concurrently: a real photo is larger than the OS pipe buffer.
    let reader = thread::spawn(move || {
        let mut bytes = Vec::new();
        stdout.take(12 * 1024 * 1024 + 1).read_to_end(&mut bytes)?;
        Ok::<_, std::io::Error>(bytes)
    });
    let deadline = Instant::now() + Duration::from_secs(4);
    let outcome = loop {
        if let Err(error) = cancel.check() {
            let _ = child.kill();
            let _ = child.wait();
            break Err(error);
        }
        match child.try_wait() {
            Ok(Some(status)) => break Ok(status.success()),
            Ok(None) => {}
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                break Err(error.into());
            }
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            break Ok(false);
        }
        thread::sleep(Duration::from_millis(20));
    };
    let bytes = reader
        .join()
        .map_err(|_| anyhow::anyhow!("native artwork reader failed"))??;
    if !outcome? {
        return Ok(None);
    }
    if bytes.len() > 12 * 1024 * 1024 {
        bail!("native artwork reply exceeds 12 MB");
    }
    let reply: Option<Reply> =
        serde_json::from_slice(&bytes).context("parsing native artwork callback")?;
    let Some(reply) = reply.filter(|reply| reply.is_bound_to(track)) else {
        return Ok(None);
    };
    cancel.check()?;
    let image = STANDARD.decode(reply.image_base64)?;
    if image.len() > 8 * 1024 * 1024 {
        bail!("native artwork exceeds 8 MB");
    }
    Ok(Some(image))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn callback_from_previous_song_or_album_edition_is_discarded() {
        let track = Track {
            title: "One".into(),
            artist: "Metallica".into(),
            album: "Justice".into(),
            ..Track::default()
        };
        let mut reply = Reply {
            title: track.title.clone(),
            artist: track.artist.clone(),
            album: track.album.clone(),
            image_base64: String::new(),
        };
        assert!(reply.is_bound_to(&track));
        reply.title = "Different song".into();
        assert!(!reply.is_bound_to(&track));
        reply.title = track.title.clone();
        reply.album = "Live".into();
        assert!(!reply.is_bound_to(&track));
    }
}
