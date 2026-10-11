use crate::model::{Playback, Track, clean};
use anyhow::{Context, Result, bail};
use objc2::{AnyThread, MainThreadMarker, rc::Retained, runtime::AnyObject};
use objc2_foundation::{NSAppleEventDescriptor, NSAppleScript, NSDictionary, NSLocale, NSString};

const POLL: &str = r#"
if application "Music" is not running then return {"stopped"}
with timeout of 3 seconds
    tell application "Music"
        set playback to player state as text
        if playback is "stopped" then return {"stopped"}
        set t to current track
        set trackTitle to name of t as text
        set trackArtist to artist of t as text
        set trackAlbum to album of t as text
        set albumArtist to ""
        set persistentId to ""
        set trackAddress to ""
        set trackNumber to 0
        set discNumber to 0
        set trackDuration to 0
        set trackPosition to 0
        try
            set albumArtist to album artist of t as text
        end try
        try
            set persistentId to persistent ID of t as text
        end try
        try
            set trackAddress to address of t as text
        end try
        try
            set trackNumber to track number of t
        end try
        try
            set discNumber to disc number of t
        end try
        try
            set trackDuration to duration of t
        end try
        try
            set trackPosition to player position
        end try
        if persistentId is not "" then
            if persistent ID of current track is not persistentId then return {"transition"}
        else
            if name of current track is not trackTitle then return {"transition"}
        end if
        return {playback, trackTitle, trackArtist, trackAlbum, albumArtist, persistentId, trackAddress, trackNumber, discNumber, trackDuration, trackPosition}
    end tell
end timeout
"#;

const ARTWORK: &str = r#"
if application "Music" is not running then return {}
with timeout of 3 seconds
    tell application "Music"
        set t to current track
        set persistentId to ""
        try
            set persistentId to persistent ID of t as text
        end try
        set trackTitle to name of t as text
        set trackArtist to artist of t as text
        set trackAlbum to album of t as text
        if (count artworks of t) is 0 then return {}
        set coverData to raw data of artwork 1 of t
        if persistentId is not "" then
            if persistent ID of current track is not persistentId then return {}
        end if
        return {persistentId, trackTitle, trackArtist, trackAlbum, coverData}
    end tell
end timeout
"#;

fn compile(source: &str) -> Result<Retained<NSAppleScript>> {
    let script = NSAppleScript::initWithSource(NSAppleScript::alloc(), &NSString::from_str(source))
        .context("creating AppleScript")?;
    let mut error: Option<Retained<NSDictionary<NSString, AnyObject>>> = None;
    // The Foundation API uses an NSDictionary<NSString, id> error out-parameter.
    if !unsafe { script.compileAndReturnError(Some(&mut error)) } {
        bail!(
            "AppleScript compilation failed: {}",
            error
                .map(|e| e.description().to_string())
                .unwrap_or_default()
        );
    }
    Ok(script)
}

fn execute(script: &NSAppleScript) -> Result<Retained<NSAppleEventDescriptor>> {
    let mut error: Option<Retained<NSDictionary<NSString, AnyObject>>> = None;
    let result = unsafe { script.executeAndReturnError(Some(&mut error)) };
    if let Some(error) = error {
        bail!("Music automation failed: {}", error.description());
    }
    Ok(result)
}

fn text(result: &NSAppleEventDescriptor, index: isize) -> String {
    result
        .descriptorAtIndex(index)
        .and_then(|d| d.stringValue())
        .map(|s| clean(&s.to_string()))
        .filter(|s| s != "missing value")
        .unwrap_or_default()
}

fn number(result: &NSAppleEventDescriptor, index: isize) -> f64 {
    let value = result
        .descriptorAtIndex(index)
        .map(|d| d.doubleValue())
        .unwrap_or(0.0);
    if value.is_finite() && value > 0.0 {
        value
    } else {
        0.0
    }
}

#[allow(deprecated)] // countryCode also works on macOS 12/13; regionCode requires macOS 14.
pub fn locale_country(_mtm: MainThreadMarker) -> String {
    NSLocale::currentLocale()
        .countryCode()
        .map(|s| s.to_string())
        .unwrap_or_else(|| "KR".into())
}

// NSAppleScript is main-thread-only. Retaining the marker makes the provider !Send/!Sync.
pub struct Provider {
    _mtm: MainThreadMarker,
    poll: Retained<NSAppleScript>,
    artwork: Retained<NSAppleScript>,
}

impl Provider {
    pub fn new(mtm: MainThreadMarker) -> Result<Self> {
        Ok(Self {
            _mtm: mtm,
            poll: compile(POLL)?,
            artwork: compile(ARTWORK)?,
        })
    }

    pub fn poll(&self) -> Result<Option<Track>> {
        let result = execute(&self.poll)?;
        let status = match text(&result, 1).as_str() {
            "playing" | "fast forwarding" | "rewinding" => Playback::Playing,
            "paused" => Playback::Paused,
            "stopped" => return Ok(Some(Track::default())),
            _ => return Ok(None),
        };
        if result.numberOfItems() != 11 {
            return Ok(None);
        }
        let mut track = Track {
            status,
            title: text(&result, 2),
            artist: text(&result, 3),
            album: text(&result, 4),
            album_artist: text(&result, 5),
            persistent_id: text(&result, 6),
            address: text(&result, 7),
            track_number: number(&result, 8) as u32,
            disc_number: number(&result, 9) as u32,
            duration: number(&result, 10),
            position: number(&result, 11),
        };
        if track.title.is_empty() {
            return Ok(None);
        }
        if track.duration > 0.0 {
            track.position = track.position.min(track.duration);
        }
        Ok(Some(track))
    }

    pub fn artwork(&self, track: &Track) -> Result<Option<Vec<u8>>> {
        let result = execute(&self.artwork)?;
        if result.numberOfItems() != 5
            || text(&result, 1) != track.persistent_id
            || text(&result, 2) != track.title
            || text(&result, 3) != track.artist
            || text(&result, 4) != track.album
        {
            return Ok(None);
        }
        let data = result
            .descriptorAtIndex(5)
            .context("artwork descriptor is missing")?
            .data();
        if data.length() > 20 * 1024 * 1024 {
            bail!("Music artwork exceeds 20 MB");
        }
        Ok(Some(data.to_vec()))
    }
}
