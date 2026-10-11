use serde::{Deserialize, Serialize};
use unicode_normalization::UnicodeNormalization;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Playback {
    Playing,
    Paused,
    #[default]
    Stopped,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Track {
    pub status: Playback,
    pub title: String,
    pub artist: String,
    pub album: String,
    pub album_artist: String,
    pub persistent_id: String,
    pub address: String,
    pub track_number: u32,
    pub disc_number: u32,
    pub duration: f64,
    pub position: f64,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct TrackKey {
    pub persistent_id: String,
    pub title: String,
    pub artist: String,
    pub album: String,
    pub album_artist: String,
    pub address: String,
    pub track_number: u32,
    pub disc_number: u32,
    pub duration_ms: u64,
}

impl Track {
    pub fn key(&self) -> TrackKey {
        TrackKey {
            persistent_id: self.persistent_id.clone(),
            title: self.title.clone(),
            artist: self.artist.clone(),
            album: self.album.clone(),
            album_artist: self.album_artist.clone(),
            address: self.address.clone(),
            track_number: self.track_number,
            disc_number: self.disc_number,
            duration_ms: (self.duration * 1000.0).round() as u64,
        }
    }

    pub fn album_artist(&self) -> &str {
        if self.album_artist.is_empty() {
            &self.artist
        } else {
            &self.album_artist
        }
    }
}

pub fn clean(value: &str) -> String {
    value.split_whitespace().collect::<Vec<_>>().join(" ")
}

// NFKC handles full-width punctuation and composed/decomposed Unicode names.
// Keep edition, live, remix, and featured-artist text: removing it can pick a different recording.
pub fn normalized(value: &str) -> String {
    value
        .nfkc()
        .flat_map(char::to_lowercase)
        .filter(|c| c.is_alphanumeric())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn identity_changes_with_album_edition_or_library_id() {
        let original = Track {
            title: "One".into(),
            artist: "Metallica".into(),
            album: "…And Justice for All".into(),
            ..Track::default()
        };
        let mut other = original.clone();
        other.album = "Greatest Hits".into();
        assert_ne!(original.key(), other.key());
        other = original.clone();
        other.persistent_id = "different".into();
        assert_ne!(original.key(), other.key());
        other = original.clone();
        other.position = 42.0;
        other.status = Playback::Paused;
        assert_eq!(original.key(), other.key());
    }

    #[test]
    fn unicode_normalization_preserves_version_information() {
        assert_eq!(normalized("ＢＯＮ JＯVI"), normalized("Bon Jovi"));
        assert_eq!(normalized("Beyonce\u{301}"), normalized("Beyoncé"));
        assert_ne!(normalized("One"), normalized("One (Live)"));
        assert_ne!(normalized("Album"), normalized("Album (Deluxe)"));
    }
}
