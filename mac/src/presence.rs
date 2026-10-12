use crate::{
    artwork::Meta,
    config::Config,
    model::{Playback, Track},
};
use serde_json::{Value, json};

#[derive(Default)]
pub struct Timeline {
    start: Option<f64>,
    previous_status: Playback,
    seek_streak: u8,
    zero_since: Option<f64>,
}

impl Timeline {
    pub fn update(&mut self, track: &Track, now: f64, changed: bool) -> bool {
        if track.status != Playback::Playing {
            self.start = None;
            self.previous_status = track.status;
            self.seek_streak = 0;
            return false;
        }
        let mut updated = false;
        if changed || self.start.is_none() || self.previous_status != Playback::Playing {
            self.start = Some(now - track.position);
            self.seek_streak = 0;
            self.zero_since = None;
            updated = true;
        } else {
            let predicted = now - self.start.unwrap_or(now);
            // Filter a single zero-position glitch, but honour an actual seek to 0.
            let zero_glitch = if track.position == 0.0 && predicted > 5.0 {
                let since = self.zero_since.get_or_insert(now);
                now - *since < 2.5
            } else {
                self.zero_since = None;
                false
            };
            if !zero_glitch {
                let difference = (predicted - track.position).abs();
                if difference > 1.5 {
                    self.seek_streak += 1;
                } else {
                    self.seek_streak = 0;
                }
                if difference > 4.0 || self.seek_streak >= 2 {
                    self.start = Some(now - track.position);
                    self.seek_streak = 0;
                    updated = true;
                }
            }
        }
        self.previous_status = track.status;
        updated
    }
}

fn truncate(value: &str) -> String {
    value.chars().take(128).collect()
}

pub fn activity(config: &Config, track: &Track, meta: Option<&Meta>, timeline: &Timeline) -> Value {
    let metadata = match (track.artist.is_empty(), track.album.is_empty()) {
        (false, false) => format!("{} • {}", track.artist, track.album),
        (false, true) => track.artist.clone(),
        _ => track.album.clone(),
    };
    let state = if track.status == Playback::Paused {
        if metadata.is_empty() {
            "⏸ Paused".into()
        } else {
            format!("⏸ Paused • {metadata}")
        }
    } else {
        metadata
    };
    let mut activity = json!({"name": "Apple Music", "type": 2, "details": truncate(&track.title), "state": truncate(&state)});
    if track.status == Playback::Playing
        && let Some(start) = timeline.start
    {
        let start = start.floor() as u64;
        let mut timestamps = json!({"start": start});
        if track.duration > 0.0 {
            timestamps["end"] = json!(start + track.duration.round() as u64);
        }
        activity["timestamps"] = timestamps;
    }
    let image = meta
        .map(|m| m.cover_url.as_str())
        .unwrap_or(&config.large_image);
    if !image.is_empty() {
        // Discord displays large_text as a separate album line for Listening
        // activities. The album is already present in state, so omit that line.
        activity["assets"] = json!({"large_image": image});
    }
    if let Some(meta) = meta
        && !meta.track_url.is_empty()
    {
        activity["buttons"] = json!([{"label": "Play On Apple Music", "url": meta.track_url}]);
    }
    activity
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pause_keeps_song_and_cover_and_resume_restores_progress() {
        let config = Config::default();
        let mut track = Track {
            status: Playback::Playing,
            title: "Tornado of Souls".into(),
            artist: "Megadeth".into(),
            album: "Rust In Peace".into(),
            position: 24.0,
            duration: 319.0,
            ..Track::default()
        };
        let meta = Meta {
            cover_url: "https://is1-ssl.mzstatic.com/image/thumb/Music/cover/512x512bb.jpg".into(),
            track_url: String::new(),
            album: track.album.clone(),
            track_id: 0,
            collection_id: 0,
            country: "KR".into(),
            verification: "native-photo-and-music-cache".into(),
            image_difference: Some(0.0),
            image_source: "mediaremote-artwork-callback".into(),
        };
        let mut timeline = Timeline::default();
        timeline.update(&track, 1000.0, true);
        let playing = activity(&config, &track, Some(&meta), &timeline);
        track.status = Playback::Paused;
        timeline.update(&track, 1001.0, false);
        let paused = activity(&config, &track, Some(&meta), &timeline);
        assert_eq!(paused["details"], playing["details"]);
        assert_eq!(paused["assets"], playing["assets"]);
        assert_eq!(paused["state"], "⏸ Paused • Megadeth • Rust In Peace");
        assert!(paused.get("timestamps").is_none());
        assert!(paused["assets"].get("large_text").is_none());
        track.status = Playback::Playing;
        timeline.update(&track, 1100.0, false);
        let resumed = activity(&config, &track, Some(&meta), &timeline);
        assert_eq!(resumed["details"], playing["details"]);
        assert_eq!(resumed["assets"], playing["assets"]);
        assert_eq!(resumed["state"], "Megadeth • Rust In Peace");
        assert_eq!(resumed["timestamps"]["start"], 1076);
        assert_eq!(resumed["timestamps"]["end"], 1395);
    }

    #[test]
    fn timestamps_are_stable_and_correct_after_pause_resume_and_seek() {
        let mut timeline = Timeline::default();
        let mut track = Track {
            status: Playback::Playing,
            position: 30.0,
            duration: 200.0,
            ..Track::default()
        };
        assert!(timeline.update(&track, 1000.0, true));
        track.position = 31.2;
        assert!(!timeline.update(&track, 1001.0, false));
        assert_eq!(timeline.start, Some(970.0));
        track.status = Playback::Paused;
        timeline.update(&track, 1010.0, false);
        assert!(
            activity(&Config::default(), &track, None, &timeline)
                .get("timestamps")
                .is_none()
        );
        track.status = Playback::Playing;
        assert!(timeline.update(&track, 1020.0, false));
        assert_eq!(timeline.start, Some(988.8));
        track.position = 120.0;
        assert!(timeline.update(&track, 1021.0, false));
        assert_eq!(timeline.start, Some(901.0));
    }

    #[test]
    fn fallback_activity_omits_unverified_cover_and_track_url() {
        let track = Track {
            status: Playback::Playing,
            title: "Different song".into(),
            ..Track::default()
        };
        let value = activity(&Config::default(), &track, None, &Timeline::default());
        assert!(value.get("assets").is_none());
        assert!(value.get("buttons").is_none());
    }

    #[test]
    fn short_zero_glitch_is_filtered_but_seek_to_start_is_honoured() {
        let mut timeline = Timeline::default();
        let mut track = Track {
            status: Playback::Playing,
            position: 20.0,
            ..Track::default()
        };
        timeline.update(&track, 100.0, true);
        track.position = 0.0;
        assert!(!timeline.update(&track, 101.0, false));
        assert!(timeline.update(&track, 104.0, false));
        assert_eq!(timeline.start, Some(104.0));
    }
}
