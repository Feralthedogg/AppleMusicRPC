use anyhow::{Context, Result, bail};
use serde::Deserialize;
use std::{
    env, fs,
    path::{Path, PathBuf},
};

#[derive(Clone, Debug, Deserialize)]
#[serde(default)]
pub struct Config {
    pub discord_app_id: String,
    pub large_image: String,
    pub large_text: String,
    pub poll_ms: u64,
    pub use_album_art: bool,
    pub show_paused: bool,
    pub force_refresh_ms: u64,
    pub country: String,
    pub fallback_countries: Vec<String>,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            discord_app_id: String::new(),
            large_image: String::new(),
            large_text: "Apple Music".into(),
            poll_ms: 1200,
            use_album_art: true,
            show_paused: true,
            force_refresh_ms: 0,
            country: String::new(),
            fallback_countries: vec!["KR".into(), "US".into(), "JP".into(), "GB".into()],
        }
    }
}

pub fn home() -> Result<PathBuf> {
    env::var_os("HOME")
        .map(PathBuf::from)
        .context("HOME is unavailable")
}

pub fn support_dir() -> Result<PathBuf> {
    Ok(home()?.join("Library/Application Support/Feralthedogg/AppleMusicRPC"))
}

pub fn cache_path() -> Result<PathBuf> {
    Ok(home()?.join("Library/Caches/Feralthedogg/AppleMusicRPC/artwork-v2.json"))
}

pub fn status_path() -> Result<PathBuf> {
    Ok(support_dir()?.join("status.json"))
}

fn country(value: &str) -> Option<String> {
    let value = value.trim().to_ascii_uppercase();
    (value.len() == 2 && value.bytes().all(|c| c.is_ascii_uppercase())).then_some(value)
}

impl Config {
    pub fn load(explicit: Option<&Path>, locale_country: &str) -> Result<(Self, PathBuf)> {
        let mut paths = Vec::new();
        if let Some(path) = explicit {
            paths.push(path.to_path_buf());
        } else if let Some(path) = env::var_os("AMRPC_CONFIG") {
            paths.push(PathBuf::from(path));
        } else {
            paths.push(support_dir()?.join("config.json"));
            paths.push(home()?.join("Library/Application Support/AppleMusicRPC/config.json"));
            paths.push(PathBuf::from(
                "/Library/Application Support/Feralthedogg/AppleMusicRPC/config.json",
            ));
            if let Some(dir) = env::current_exe()?.parent() {
                paths.push(dir.join("config.json"));
                paths.push(dir.join("../Resources/config.json"));
            }
            paths.push(env::current_dir()?.join("config.json"));
            paths.push(PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("config.json"));
        }
        for path in &paths {
            let data = match fs::read(path) {
                Ok(data) => data,
                Err(err) if err.kind() == std::io::ErrorKind::NotFound => continue,
                Err(err) => return Err(err).with_context(|| format!("reading {}", path.display())),
            };
            let mut config: Self = serde_json::from_slice(&data)
                .with_context(|| format!("invalid config: {}", path.display()))?;
            if config.discord_app_id.is_empty()
                || !config.discord_app_id.bytes().all(|c| c.is_ascii_digit())
            {
                bail!("discord_app_id must be a numeric Discord application ID");
            }
            config.poll_ms = if config.poll_ms == 0 {
                1200
            } else {
                config.poll_ms.clamp(250, 60_000)
            };
            if config.large_text.trim().is_empty() {
                config.large_text = "Apple Music".into();
            }
            if !config.country.trim().is_empty() && country(&config.country).is_none() {
                bail!("country must be a two-letter country code");
            }
            config.country = country(&config.country)
                .or_else(|| country(locale_country))
                .unwrap_or_else(|| "KR".into());
            config.fallback_countries = config
                .fallback_countries
                .iter()
                .filter_map(|cc| country(cc))
                .collect();
            return Ok((config, path.clone()));
        }
        bail!(
            "config.json was not found; searched {}",
            paths
                .iter()
                .map(|p| p.display().to_string())
                .collect::<Vec<_>>()
                .join(", ")
        )
    }

    pub fn countries(&self) -> Vec<String> {
        let mut countries = vec![self.country.clone()];
        for cc in &self.fallback_countries {
            if !countries.contains(cc) {
                countries.push(cc.clone());
            }
        }
        countries
    }
}

pub fn atomic_json(path: &Path, value: &impl serde::Serialize) -> Result<()> {
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
    }
    let temporary = path.with_extension(format!("{}.tmp", std::process::id()));
    fs::write(&temporary, serde_json::to_vec_pretty(value)?)?;
    fs::rename(temporary, path)?;
    Ok(())
}
