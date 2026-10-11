use crate::config::support_dir;
use anyhow::{Context, Result};
use std::{
    fs::{self, File, OpenOptions},
    os::{fd::AsRawFd, unix::fs::OpenOptionsExt},
};

pub struct Lock {
    _file: File,
}

impl Lock {
    pub fn acquire() -> Result<Option<Self>> {
        let directory = support_dir()?;
        fs::create_dir_all(&directory)?;
        // Share the original Go app's lock, preventing overlap during migrations.
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .open(directory.join("amrpc.applemusic.lock"))?;
        // SAFETY: The file descriptor stays alive in Lock for the entire app lifetime.
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
            let error = std::io::Error::last_os_error();
            if error.raw_os_error() == Some(libc::EWOULDBLOCK) {
                return Ok(None);
            }
            return Err(error).context("acquiring single-instance lock");
        }
        Ok(Some(Self { _file: file }))
    }
}
