use crate::{
    config::{Config, atomic_json},
    media_remote,
    model::{Track, TrackKey, normalized},
    music_artwork_cache::MusicCache,
};
use anyhow::{Context, Result, bail};
use image::{ImageReader, imageops::FilterType};
use objc2::{rc::autoreleasepool, runtime::AnyObject};
use objc2_app_kit::{NSBitmapImageFileType, NSBitmapImageRep, NSBitmapImageRepPropertyKey};
use objc2_foundation::{NSData, NSDictionary};
use reqwest::{Url, blocking::Client};
use serde::{Deserialize, Serialize};
use std::{
    collections::HashMap,
    fs,
    io::Read,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Meta {
    pub cover_url: String,
    pub track_url: String,
    pub album: String,
    pub track_id: u64,
    pub collection_id: u64,
    pub country: String,
    pub verification: String,
    pub image_difference: Option<f64>,
    pub image_source: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct Fingerprint(Vec<u8>);

impl Fingerprint {
    pub fn from_bytes(data: &[u8]) -> Result<Self> {
        let mut reader = ImageReader::new(std::io::Cursor::new(data)).with_guessed_format()?;
        let mut limits = image::Limits::default();
        limits.max_image_width = Some(8192);
        limits.max_image_height = Some(8192);
        limits.max_alloc = Some(128 * 1024 * 1024);
        reader.limits(limits);
        let image = match reader.decode() {
            Ok(image) => image,
            Err(error) => autoreleasepool(|_| {
                // Music's NSURLCache commonly stores HEIC. Decode with Apple's
                // image codec, then feed a portable JPEG to the same colour grid.
                let bitmap = NSBitmapImageRep::imageRepWithData(&NSData::with_bytes(data))
                    .with_context(|| format!("decoding album cover: {error}"))?;
                if bitmap.pixelsWide() > 8192 || bitmap.pixelsHigh() > 8192 {
                    bail!("album cover dimensions exceed 8192 px");
                }
                let properties = NSDictionary::<NSBitmapImageRepPropertyKey, AnyObject>::new();
                // SAFETY: the empty dictionary has the exact property-key/object types.
                let jpeg = unsafe {
                    bitmap.representationUsingType_properties(
                        NSBitmapImageFileType::JPEG,
                        &properties,
                    )
                }
                .context("converting native album image")?;
                image::load_from_memory(&jpeg.to_vec()).context("decoding native album image")
            })?,
        };
        // A small colour grid tolerates JPEG compression and thumbnail resizing,
        // while distinguishing covers whose layout is similar but colours differ.
        Ok(Self(
            image
                .resize_exact(16, 16, FilterType::Triangle)
                .to_rgb8()
                .into_raw(),
        ))
    }

    pub fn difference(&self, other: &Self) -> f64 {
        (self
            .0
            .iter()
            .zip(&other.0)
            .map(|(a, b)| (*a as f64 - *b as f64).powi(2))
            .sum::<f64>()
            / self.0.len() as f64)
            .sqrt()
    }

    pub fn exact_photo_match(&self, other: &Self) -> bool {
        self.0.len() == 768 && other.0.len() == 768 && self.difference(other) <= 6.0
    }
}

#[derive(Clone)]
pub struct Cancel {
    pub generation: u64,
    pub current: Arc<AtomicU64>,
    pub stop: Arc<AtomicBool>,
}

impl Cancel {
    pub fn check(&self) -> Result<()> {
        if self.stop.load(Ordering::Relaxed)
            || self.current.load(Ordering::Relaxed) != self.generation
        {
            bail!("artwork lookup superseded");
        }
        Ok(())
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct Candidate {
    kind: String,
    track_name: String,
    artist_name: String,
    collection_name: String,
    artwork_url100: String,
    track_view_url: String,
    track_id: u64,
    collection_id: u64,
    track_time_millis: u64,
    track_number: u32,
    disc_number: u32,
}

#[derive(Deserialize)]
struct Response {
    results: Vec<Candidate>,
}

#[derive(Clone, Serialize, Deserialize)]
struct CacheEntry {
    key: TrackKey,
    reference: Option<Fingerprint>,
    countries: Vec<String>,
    resolved_at: u64,
    meta: Meta,
}

#[derive(Default, Serialize, Deserialize)]
struct Cache {
    version: u32,
    entries: Vec<CacheEntry>,
}

pub struct Resolver {
    client: Client,
    countries: Vec<String>,
    cache_path: PathBuf,
    cache: Cache,
    last_request: Option<Instant>,
    music_cache: MusicCache,
}

fn unix_time() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

fn album_key(value: &str) -> String {
    // Apple's display suffix does not indicate a different edition.
    let value = value.trim();
    let lowered = value.to_lowercase();
    for suffix in [" - single", " - ep"] {
        if lowered.ends_with(suffix) {
            return normalized(&value[..value.len() - suffix.len()]);
        }
    }
    normalized(value)
}

fn title_key(value: &str, have_reference: bool) -> String {
    let value = value.trim();
    if have_reference && value.to_lowercase().ends_with(" (bonus track)") {
        return normalized(&value[..value.len() - " (bonus track)".len()]);
    }
    normalized(value)
}

fn metadata_score(track: &Track, candidate: &Candidate, have_reference: bool) -> Option<f64> {
    if candidate.kind != "song"
        || candidate.track_id == 0
        || candidate.collection_id == 0
        || candidate.artwork_url100.is_empty()
    {
        return None;
    }
    if title_key(&track.title, have_reference) != title_key(&candidate.track_name, have_reference)
        || normalized(&track.artist).is_empty()
        || normalized(&track.artist) != normalized(&candidate.artist_name)
    {
        return None;
    }
    if !track.album.is_empty() && album_key(&track.album) != album_key(&candidate.collection_name) {
        return None;
    }
    if track.album.is_empty() && !have_reference {
        return None;
    }
    let difference = (track.duration - candidate.track_time_millis as f64 / 1000.0).abs();
    if track.duration > 0.0 && (candidate.track_time_millis == 0 || difference > 4.0) {
        return None;
    }
    if track.track_number > 0
        && candidate.track_number > 0
        && track.track_number != candidate.track_number
    {
        return None;
    }
    if track.disc_number > 0
        && candidate.disc_number > 0
        && track.disc_number != candidate.disc_number
    {
        return None;
    }
    Some(difference)
}

fn artwork_url(value: &str) -> Result<String> {
    let value = value.replacen("http://", "https://", 1);
    let url = Url::parse(&value)?;
    let host = url.host_str().unwrap_or_default();
    if url.scheme() != "https" || !(host == "mzstatic.com" || host.ends_with(".mzstatic.com")) {
        bail!("unexpected artwork host");
    }
    Ok(value)
}

fn high_resolution(value: &str) -> String {
    value
        .replace("100x100bb", "512x512bb")
        .replace("100x100-", "512x512-")
}

impl Resolver {
    pub fn new(config: &Config, cache_path: PathBuf) -> Result<Self> {
        let cache = fs::read(&cache_path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Cache>(&b).ok())
            .filter(|c| c.version == 3)
            .unwrap_or(Cache {
                version: 3,
                entries: Vec::new(),
            });
        Ok(Self {
            client: Client::builder()
                .connect_timeout(Duration::from_secs(3))
                .timeout(Duration::from_secs(6))
                .https_only(true)
                .user_agent(concat!("AppleMusicRPC/", env!("CARGO_PKG_VERSION")))
                .build()?,
            countries: config.countries(),
            cache_path,
            cache,
            last_request: None,
            music_cache: MusicCache::new()?,
        })
    }

    fn api(
        &mut self,
        endpoint: &str,
        parameters: &[(&str, &str)],
        cancel: &Cancel,
    ) -> Result<Vec<Candidate>> {
        // Apple's search API budget is roughly 20 requests/minute. Waits are
        // cancellable so rapidly skipping songs does not build up stale searches.
        while self
            .last_request
            .is_some_and(|last| last.elapsed() < Duration::from_millis(3100))
        {
            cancel.check()?;
            thread::sleep(Duration::from_millis(50));
        }
        cancel.check()?;
        self.last_request = Some(Instant::now());
        let response = self
            .client
            .get(format!("https://itunes.apple.com/{endpoint}"))
            .query(parameters)
            .send()?
            .error_for_status()?;
        let mut bytes = Vec::new();
        response.take(2 * 1024 * 1024 + 1).read_to_end(&mut bytes)?;
        if bytes.len() > 2 * 1024 * 1024 {
            bail!("catalog response exceeds 2 MB");
        }
        cancel.check()?;
        Ok(serde_json::from_slice::<Response>(&bytes)?.results)
    }

    fn image(&self, url: &str, cancel: &Cancel) -> Result<Fingerprint> {
        cancel.check()?;
        let mut data = Vec::new();
        self.client
            .get(artwork_url(url)?)
            .send()?
            .error_for_status()?
            .take(5 * 1024 * 1024 + 1)
            .read_to_end(&mut data)?;
        if data.len() > 5 * 1024 * 1024 {
            bail!("catalog artwork exceeds 5 MB");
        }
        cancel.check()?;
        Fingerprint::from_bytes(&data)
    }

    fn choose(
        &self,
        track: &Track,
        candidates: Vec<Candidate>,
        reference: Option<&Fingerprint>,
        country: &str,
        images: &mut HashMap<String, Fingerprint>,
        cancel: &Cancel,
    ) -> Result<Option<Meta>> {
        let mut candidates: Vec<_> = candidates
            .into_iter()
            .filter_map(|c| {
                let score = metadata_score(track, &c, reference.is_some());
                // The actual current photo is the authority. Edited titles and
                // regional album names cannot veto a pixel-verified cover.
                if reference.is_some() && !c.artwork_url100.is_empty() {
                    Some((score.unwrap_or(10_000.0), c))
                } else {
                    score.map(|score| (score, c))
                }
            })
            .collect();
        candidates.sort_by(|a, b| a.0.total_cmp(&b.0));
        for (_, candidate) in candidates.into_iter().take(8) {
            cancel.check()?;
            let cover = match artwork_url(&candidate.artwork_url100) {
                Ok(url) => url,
                Err(_) => continue,
            };
            let difference = if let Some(reference) = reference {
                if !images.contains_key(&cover) {
                    let fingerprint = match self.image(&cover, cancel) {
                        Ok(f) => f,
                        Err(error) => {
                            cancel.check()?;
                            eprintln!("Artwork thumbnail: {error}");
                            continue;
                        }
                    };
                    images.insert(cover.clone(), fingerprint);
                }
                let image = &images[&cover];
                if !reference.exact_photo_match(image) {
                    eprintln!(
                        "Rejected different artwork: {} / {} (difference {:.1})",
                        candidate.artist_name,
                        candidate.collection_name,
                        reference.difference(image)
                    );
                    continue;
                }
                Some(reference.difference(image))
            } else {
                // Verify it is a decodable, reachable image even without native art.
                if self.image(&cover, cancel).is_err() {
                    cancel.check()?;
                    continue;
                }
                None
            };
            // Verify the exact URL sent to Discord too; never cache a made-up size that 404s.
            let upgraded = high_resolution(&cover);
            let cover_url = if upgraded != cover {
                match self.image(&upgraded, cancel) {
                    Ok(image) if reference.is_none_or(|r| r.exact_photo_match(&image)) => upgraded,
                    _ => {
                        cancel.check()?;
                        cover
                    }
                }
            } else {
                cover
            };
            let track_url = if metadata_score(track, &candidate, reference.is_some()).is_some() {
                Url::parse(&candidate.track_view_url)
                    .ok()
                    .filter(|u| u.scheme() == "https" && u.host_str() == Some("music.apple.com"))
                    .map(|u| u.to_string())
                    .unwrap_or_else(|| {
                        format!(
                            "https://music.apple.com/{}/album/{}?i={}",
                            country.to_lowercase(),
                            candidate.collection_id,
                            candidate.track_id
                        )
                    })
            } else {
                String::new()
            };
            return Ok(Some(Meta {
                cover_url,
                track_url,
                album: candidate.collection_name,
                track_id: candidate.track_id,
                collection_id: candidate.collection_id,
                country: country.into(),
                verification: if reference.is_some() {
                    "music-artwork-and-metadata"
                } else {
                    "exact-album-and-metadata"
                }
                .into(),
                image_difference: difference,
                image_source: "music-current-artwork-getter".into(),
            }));
        }
        Ok(None)
    }

    pub fn resolve(
        &mut self,
        track: &Track,
        reference: Option<&Fingerprint>,
        cancel: &Cancel,
        bypass_cache: bool,
    ) -> Result<Option<Meta>> {
        cancel.check()?;
        let callback_photo = match media_remote::read_artwork(track, cancel) {
            Ok(Some(bytes)) => match Fingerprint::from_bytes(&bytes) {
                Ok(photo) => {
                    eprintln!("Native artwork callback: {} bytes", bytes.len());
                    Some(photo)
                }
                Err(error) => {
                    eprintln!("Native artwork image: {error}");
                    None
                }
            },
            Ok(None) => None,
            Err(error) => {
                cancel.check()?;
                eprintln!("Native artwork callback: {error}");
                None
            }
        };
        let image_source = if callback_photo.is_some() {
            "mediaremote-artwork-callback"
        } else {
            "music-current-artwork-getter"
        };
        let reference = callback_photo.as_ref().or(reference);
        let key = track.key();
        if !bypass_cache
            && let Some(entry) = self.cache.entries.iter().find(|entry| {
                entry.key == key
                    && entry.reference.as_ref() == reference
                    && entry.countries == self.countries
                    && unix_time().saturating_sub(entry.resolved_at) < 30 * 86400
            })
        {
            return Ok(Some(entry.meta.clone()));
        }
        let mut images = HashMap::new();
        let mut result = None;
        // Resolve the exact photo's original Music URL without catalog IDs or
        // track-name matching. The remote rendering must match the photo too.
        if let Some(reference) = reference
            && let Some(url) = self.music_cache.find(reference, cancel)?
        {
            match self.image(&url, cancel) {
                Ok(image) if reference.exact_photo_match(&image) => {
                    result = Some(Meta {
                        cover_url: url,
                        track_url: String::new(),
                        album: track.album.clone(),
                        track_id: 0,
                        collection_id: 0,
                        country: self.countries[0].clone(),
                        verification: "native-photo-and-music-cache".into(),
                        image_difference: Some(reference.difference(&image)),
                        image_source: image_source.into(),
                    });
                }
                Ok(_) => {}
                Err(error) => {
                    cancel.check()?;
                    eprintln!("Music photo URL: {error}");
                }
            }
        }
        for country in self.countries.clone() {
            if result.is_some() {
                break;
            }
            cancel.check()?;
            let mut candidates = Vec::new();
            let query = if reference.is_some() {
                format!("{} {}", track.album_artist(), track.album)
            } else {
                format!("{} {} {}", track.artist, track.title, track.album)
            };
            match self.api(
                "search",
                &[
                    ("term", &query),
                    ("country", &country),
                    ("media", "music"),
                    ("entity", "song"),
                    ("limit", "100"),
                ],
                cancel,
            ) {
                Ok(found) => candidates.extend(found),
                Err(error) => {
                    cancel.check()?;
                    eprintln!("Song search ({country}): {error}");
                }
            }
            result = self.choose(track, candidates, reference, &country, &mut images, cancel)?;
            if result.is_some() {
                break;
            }

            // When the image is known, album artwork is sufficient; do not
            // query catalog IDs or infer an alternate recording from its title.
            if reference.is_some() && !track.album.is_empty() {
                let query = format!("{} {}", track.album_artist(), track.album);
                match self.api(
                    "search",
                    &[
                        ("term", &query),
                        ("country", &country),
                        ("media", "music"),
                        ("entity", "album"),
                        ("limit", "100"),
                    ],
                    cancel,
                ) {
                    Ok(albums) => {
                        result =
                            self.choose(track, albums, reference, &country, &mut images, cancel)?;
                        if result.is_some() {
                            break;
                        }
                    }
                    Err(error) => {
                        cancel.check()?;
                        eprintln!("Album photo search ({country}): {error}");
                    }
                }
            }
        }
        if let Some(meta) = &mut result {
            meta.image_source = image_source.into();
            self.cache.entries.retain(|entry| entry.key != key);
            self.cache.entries.push(CacheEntry {
                key,
                reference: reference.cloned(),
                countries: self.countries.clone(),
                resolved_at: unix_time(),
                meta: meta.clone(),
            });
            let excess = self.cache.entries.len().saturating_sub(256);
            self.cache.entries.drain(..excess);
            if let Err(error) = atomic_json(&self.cache_path, &self.cache) {
                eprintln!("Artwork cache: {error}");
            }
        }
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn track() -> Track {
        Track {
            title: "One".into(),
            artist: "Metallica".into(),
            album: "…And Justice for All".into(),
            duration: 446.0,
            track_number: 4,
            disc_number: 1,
            ..Track::default()
        }
    }
    fn candidate() -> Candidate {
        Candidate {
            kind: "song".into(),
            track_id: 1,
            collection_id: 2,
            track_name: "One".into(),
            artist_name: "Metallica".into(),
            collection_name: "...And Justice for All".into(),
            artwork_url100: "https://is1-ssl.mzstatic.com/image/100x100bb.jpg".into(),
            track_time_millis: 446_100,
            track_number: 4,
            disc_number: 1,
            ..Candidate::default()
        }
    }

    #[test]
    fn rejects_wrong_album_even_when_duration_and_name_match() {
        let mut candidate = candidate();
        assert!(metadata_score(&track(), &candidate, false).is_some());
        candidate.collection_name = "Greatest Hits".into();
        assert!(metadata_score(&track(), &candidate, false).is_none());
        assert!(metadata_score(&track(), &candidate, true).is_none());
    }
    #[test]
    fn rejects_live_remix_substring_artist_and_wrong_track_number() {
        for (title, artist, number, duration) in [
            ("One (Live)", "Metallica", 4, 446_100),
            ("One", "Metallica Tribute", 4, 446_100),
            ("One", "Metallica", 8, 446_100),
            ("One", "Metallica", 4, 400_000),
        ] {
            let mut candidate = candidate();
            candidate.track_name = title.into();
            candidate.artist_name = artist.into();
            candidate.track_number = number;
            candidate.track_time_millis = duration;
            assert!(metadata_score(&track(), &candidate, true).is_none());
        }
    }
    #[test]
    fn missing_album_requires_native_image_and_bonus_label_requires_verification() {
        let mut track = track();
        track.album.clear();
        assert!(metadata_score(&track, &candidate(), false).is_none());
        assert!(metadata_score(&track, &candidate(), true).is_some());
        track.title = "One (Bonus Track)".into();
        assert!(metadata_score(&track, &candidate(), true).is_some());
        assert!(metadata_score(&track, &candidate(), false).is_none());
    }
    #[test]
    fn fingerprint_rejects_different_cover_and_accepts_thumbnail() {
        let cover = image::RgbImage::from_fn(256, 256, |x, y| {
            image::Rgb([(x / 2) as u8, (y / 2) as u8, 80])
        });
        let mut bytes = std::io::Cursor::new(Vec::new());
        image::DynamicImage::ImageRgb8(cover.clone())
            .write_to(&mut bytes, image::ImageFormat::Png)
            .unwrap();
        let reference = Fingerprint::from_bytes(bytes.get_ref()).unwrap();
        let thumb = image::imageops::resize(&cover, 100, 100, FilterType::Triangle);
        let mut jpg = std::io::Cursor::new(Vec::new());
        image::DynamicImage::ImageRgb8(thumb)
            .write_to(&mut jpg, image::ImageFormat::Jpeg)
            .unwrap();
        assert!(reference.exact_photo_match(&Fingerprint::from_bytes(jpg.get_ref()).unwrap()));
        assert!(!reference.exact_photo_match(&Fingerprint(vec![220; 16 * 16 * 3])));
    }
}
