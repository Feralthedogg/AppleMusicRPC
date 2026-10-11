use crate::{
    artwork::{Cancel, Fingerprint},
    config::{self, atomic_json},
};
use anyhow::Result;
use reqwest::Url;
use rusqlite::{Connection, OpenFlags};
use serde::{Deserialize, Serialize};
use std::{
    collections::HashMap,
    fs,
    hash::{DefaultHasher, Hash, Hasher},
    path::{Path, PathBuf},
    time::{Duration, UNIX_EPOCH},
};

#[derive(Serialize, Deserialize)]
struct IndexedImage {
    signature: String,
    fingerprint: Fingerprint,
}

#[derive(Default, Serialize, Deserialize)]
struct Index {
    version: u32,
    images: HashMap<String, IndexedImage>,
}

pub struct MusicCache {
    index: Index,
    path: PathBuf,
}

fn public_artwork_url(value: &str) -> Option<String> {
    let mut url = Url::parse(value).ok()?;
    let host = url.host_str()?;
    if url.scheme() != "https"
        || !host.ends_with(".mzstatic.com")
        || !url.path().starts_with("/image/thumb/")
    {
        return None;
    }
    // The image itself is public; cache headers and URL query credentials are never forwarded.
    url.set_query(None);
    url.set_fragment(None);
    Some(url.to_string())
}

pub fn jpeg_url(value: &str) -> Option<String> {
    let mut url = Url::parse(&public_artwork_url(value)?).ok()?;
    let path = url.path().to_string();
    let (base, size) = path.rsplit_once('/')?;
    // Preserve the original image path, requesting a Discord-compatible rendering.
    if size.contains('x')
        && (size.ends_with(".heic") || size.ends_with(".jpg") || size.ends_with(".png"))
    {
        url.set_path(&format!("{base}/512x512bb.jpg"));
    }
    Some(url.to_string())
}

fn file_key(bytes: &[u8]) -> Option<&str> {
    let key = std::str::from_utf8(bytes).ok()?;
    (key.len() == 36 && key.bytes().all(|c| c.is_ascii_hexdigit() || c == b'-')).then_some(key)
}

impl MusicCache {
    pub fn new() -> Result<Self> {
        let path =
            config::home()?.join("Library/Caches/Feralthedogg/AppleMusicRPC/music-photos-v1.json");
        let index = fs::read(&path)
            .ok()
            .and_then(|bytes| serde_json::from_slice::<Index>(&bytes).ok())
            .filter(|index| index.version == 1)
            .unwrap_or(Index {
                version: 1,
                images: HashMap::new(),
            });
        Ok(Self { index, path })
    }

    fn save(&self) {
        if let Err(error) = atomic_json(&self.path, &self.index) {
            eprintln!("Music photo index: {error}");
        }
    }

    fn scan(
        &mut self,
        path: &Path,
        reference: &Fingerprint,
        cancel: &Cancel,
    ) -> Result<Option<String>> {
        let db = Connection::open_with_flags(
            path.join("Cache.db"),
            OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
        )?;
        db.busy_timeout(Duration::from_millis(200))?;
        let mut statement = db.prepare("SELECT r.entry_ID, r.request_key, d.isDataOnFS, d.receiver_data FROM cfurl_cache_response r JOIN cfurl_cache_receiver_data d USING(entry_ID) WHERE r.request_key LIKE '%mzstatic.com/image/thumb/Music%' AND length(d.receiver_data) <= 5242880 ORDER BY r.entry_ID DESC")?;
        let mut rows = statement.query([])?;
        let mut dirty = false;
        while let Some(row) = rows.next()? {
            if let Err(error) = cancel.check() {
                if dirty {
                    self.save();
                }
                return Err(error);
            }
            let entry_id: i64 = row.get(0)?;
            let raw_url: String = row.get(1)?;
            let Some(url) = public_artwork_url(&raw_url) else {
                continue;
            };
            let on_disk: bool = row.get(2)?;
            let bytes = row.get_ref(3)?.as_bytes()?;
            let file = if on_disk {
                file_key(bytes).map(|key| path.join("fsCachedData").join(key))
            } else {
                None
            };
            let signature = if on_disk {
                let Some(file) = &file else {
                    continue;
                };
                let Ok(metadata) = file.metadata() else {
                    continue;
                };
                if metadata.len() > 5 * 1024 * 1024 {
                    continue;
                }
                format!(
                    "{entry_id}:{}:{}:{}",
                    file.display(),
                    metadata.len(),
                    metadata
                        .modified()?
                        .duration_since(UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_nanos()
                )
            } else {
                let mut hash = DefaultHasher::new();
                bytes.hash(&mut hash);
                format!("{entry_id}:{:x}", hash.finish())
            };
            if self
                .index
                .images
                .get(&url)
                .is_none_or(|cached| cached.signature != signature)
            {
                let bytes = if let Some(file) = file {
                    match fs::read(file) {
                        Ok(bytes) => bytes,
                        Err(_) => continue,
                    }
                } else {
                    bytes.to_vec()
                };
                let fingerprint = match Fingerprint::from_bytes(&bytes) {
                    Ok(fingerprint) => fingerprint,
                    Err(_) => continue,
                };
                self.index.images.insert(
                    url.clone(),
                    IndexedImage {
                        signature,
                        fingerprint,
                    },
                );
                dirty = true;
            }
            if reference.exact_photo_match(&self.index.images[&url].fingerprint) {
                if dirty {
                    self.save();
                }
                return Ok(jpeg_url(&url));
            }
        }
        if dirty {
            self.save();
        }
        Ok(None)
    }

    pub fn find(&mut self, reference: &Fingerprint, cancel: &Cancel) -> Result<Option<String>> {
        // A warmed index makes repeated albums instant, even if SQLite is busy.
        if let Some(url) = self.index.images.iter().find_map(|(url, image)| {
            reference
                .exact_photo_match(&image.fingerprint)
                .then(|| jpeg_url(url))
                .flatten()
        }) {
            return Ok(Some(url));
        }
        let root = config::home()?.join("Library/Caches/com.apple.Music");
        for path in [root.join("MusicUIArtworkCache"), root] {
            if !path.join("Cache.db").exists() {
                continue;
            }
            match self.scan(&path, reference, cancel) {
                Ok(Some(url)) => return Ok(Some(url)),
                Ok(None) => {}
                Err(error) => {
                    cancel.check()?;
                    eprintln!("Music artwork cache: {error}");
                }
            }
        }
        Ok(None)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn local_cache_cannot_return_credentials_or_non_apple_urls() {
        assert_eq!(jpeg_url("https://is1-ssl.mzstatic.com/image/thumb/Music/a/image.jpg/400x400bb.heic?token=private").as_deref(), Some("https://is1-ssl.mzstatic.com/image/thumb/Music/a/image.jpg/512x512bb.jpg"));
        assert!(jpeg_url("https://example.com/image/thumb/Music/a/400x400bb.jpg").is_none());
        assert!(file_key(b"../../../../private-user-data").is_none());
    }

    #[test]
    fn photo_selects_the_matching_cached_image_without_track_names_or_ids() {
        let directory = std::env::temp_dir().join(format!(
            "amrpc-photo-test-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&directory).unwrap();
        let encode = |colour| {
            let image = image::RgbImage::from_pixel(32, 32, image::Rgb(colour));
            let mut bytes = std::io::Cursor::new(Vec::new());
            image::DynamicImage::ImageRgb8(image)
                .write_to(&mut bytes, image::ImageFormat::Png)
                .unwrap();
            bytes.into_inner()
        };
        let original = encode([30, 90, 170]);
        let other = encode([220, 10, 30]);
        let db = Connection::open(directory.join("Cache.db")).unwrap();
        db.execute_batch("CREATE TABLE cfurl_cache_response(entry_ID INTEGER, request_key TEXT); CREATE TABLE cfurl_cache_receiver_data(entry_ID INTEGER,isDataOnFS INTEGER,receiver_data BLOB);").unwrap();
        for (id, file, bytes) in [(1, "correct", &original), (2, "wrong", &other)] {
            db.execute(
                "INSERT INTO cfurl_cache_response VALUES (?1,?2)",
                rusqlite::params![
                    id,
                    format!("https://is1-ssl.mzstatic.com/image/thumb/Music/{file}/100x100bb.png")
                ],
            )
            .unwrap();
            db.execute(
                "INSERT INTO cfurl_cache_receiver_data VALUES (?1,0,?2)",
                rusqlite::params![id, bytes],
            )
            .unwrap();
        }
        drop(db);
        let cancel = Cancel {
            generation: 1,
            current: std::sync::Arc::new(std::sync::atomic::AtomicU64::new(1)),
            stop: std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false)),
        };
        let mut cache = MusicCache {
            index: Index {
                version: 1,
                images: HashMap::new(),
            },
            path: directory.join("index.json"),
        };
        let url = cache
            .scan(
                &directory,
                &Fingerprint::from_bytes(&original).unwrap(),
                &cancel,
            )
            .unwrap()
            .unwrap();
        assert!(url.contains("/correct/"));
        fs::remove_dir_all(directory).unwrap();
    }
}
