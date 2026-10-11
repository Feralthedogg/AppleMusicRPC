use anyhow::{Context, Result, bail};
use serde_json::{Value, json};
use std::{
    env,
    io::{Read, Write},
    os::fd::AsRawFd,
    os::unix::net::UnixStream,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
        mpsc,
    },
    thread,
    time::{Duration, Instant},
};

const HANDSHAKE: u32 = 0;
const FRAME: u32 = 1;
const CLOSE: u32 = 2;
const PING: u32 = 3;
const PONG: u32 = 4;
const MAX_FRAME: usize = 1024 * 1024;

fn write_frame(stream: &mut UnixStream, op: u32, body: &[u8]) -> Result<()> {
    stream.write_all(&op.to_le_bytes())?;
    stream.write_all(&(body.len() as u32).to_le_bytes())?;
    stream.write_all(body)?;
    Ok(())
}

fn read_exact_until(stream: &mut UnixStream, buffer: &mut [u8], deadline: Instant) -> Result<()> {
    let mut offset = 0;
    while offset < buffer.len() {
        let remaining = deadline
            .checked_duration_since(Instant::now())
            .context("Discord IPC response timed out")?;
        let mut descriptor = libc::pollfd {
            fd: stream.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        // SAFETY: descriptor is initialized and valid for one pollfd. Only this
        // thread reads the socket; each read after poll consumes available bytes.
        let ready = unsafe {
            libc::poll(
                &mut descriptor,
                1,
                remaining.as_millis().max(1).min(i32::MAX as u128) as i32,
            )
        };
        if ready < 0 {
            let error = std::io::Error::last_os_error();
            if error.kind() == std::io::ErrorKind::Interrupted {
                continue;
            }
            return Err(error).context("polling Discord IPC");
        }
        if ready == 0 {
            bail!("Discord IPC response timed out");
        }
        match stream.read(&mut buffer[offset..]) {
            Ok(0) => bail!("Discord closed IPC before completing its frame"),
            Ok(length) => offset += length,
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(error).context("reading Discord IPC"),
        }
    }
    Ok(())
}

fn read_frame(stream: &mut UnixStream, deadline: Instant) -> Result<(u32, Vec<u8>)> {
    let mut header = [0; 8];
    read_exact_until(stream, &mut header, deadline)?;
    let op = u32::from_le_bytes(header[..4].try_into()?);
    let length = u32::from_le_bytes(header[4..].try_into()?) as usize;
    if length > MAX_FRAME {
        bail!("Discord IPC frame is too large: {length}");
    }
    let mut body = vec![0; length];
    read_exact_until(stream, &mut body, deadline)?;
    Ok((op, body))
}

fn runtime_dirs() -> Vec<PathBuf> {
    let mut dirs = Vec::new();
    for key in ["XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"] {
        if let Some(value) = env::var_os(key).filter(|v| !v.is_empty()) {
            dirs.push(PathBuf::from(value));
        }
    }
    // LaunchAgents need not inherit TMPDIR, but Discord uses Darwin's per-user temp directory.
    let mut buffer = vec![0u8; 1024];
    // SAFETY: confstr writes at most buffer.len() bytes and the constant names a system path.
    let length = unsafe {
        libc::confstr(
            libc::_CS_DARWIN_USER_TEMP_DIR,
            buffer.as_mut_ptr().cast(),
            buffer.len(),
        )
    };
    if length > 1 && length <= buffer.len() {
        dirs.push(PathBuf::from(
            String::from_utf8_lossy(&buffer[..length - 1]).as_ref(),
        ));
    }
    dirs.extend(["/tmp", "/var/tmp", "/usr/tmp"].map(PathBuf::from));
    let mut unique = Vec::new();
    for dir in dirs {
        if !unique.contains(&dir) {
            unique.push(dir);
        }
    }
    unique
}

pub struct Client {
    app_id: String,
    stream: Option<UnixStream>,
    nonce: u64,
}

impl Client {
    pub fn new(app_id: String) -> Self {
        Self {
            app_id,
            stream: None,
            nonce: 0,
        }
    }

    fn receive(&mut self, expected_nonce: Option<&str>) -> Result<Value> {
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            let stream = self
                .stream
                .as_mut()
                .context("Discord IPC is disconnected")?;
            // poll preserves buffered replies even if the peer closes immediately.
            // Darwin can reject SO_RCVTIMEO changes on an already-closed Unix peer.
            let (op, body) = read_frame(stream, deadline)?;
            match op {
                PING => {
                    write_frame(stream, PONG, &body)?;
                    continue;
                }
                PONG => continue,
                CLOSE => bail!("Discord closed IPC: {}", String::from_utf8_lossy(&body)),
                FRAME => {}
                _ => bail!("unexpected Discord IPC opcode: {op}"),
            }
            let value: Value = serde_json::from_slice(&body)?;
            if let Some(nonce) = expected_nonce {
                if value.get("nonce").and_then(Value::as_str) != Some(nonce) {
                    continue;
                }
            } else if value.get("evt").and_then(Value::as_str) != Some("READY") {
                bail!(
                    "Discord handshake was rejected: {}",
                    value.get("data").unwrap_or(&value)
                );
            }
            if value.get("evt").and_then(Value::as_str) == Some("ERROR") {
                bail!(
                    "Discord rejected SET_ACTIVITY: {}",
                    value.get("data").unwrap_or(&value)
                );
            }
            return Ok(value);
        }
    }

    pub fn connect(&mut self) -> Result<()> {
        if self.stream.is_some() {
            return Ok(());
        }
        let mut last_error = String::from("socket not found");
        for directory in runtime_dirs() {
            for index in 0..10 {
                let path = directory.join(format!("discord-ipc-{index}"));
                let mut stream = match UnixStream::connect(&path) {
                    Ok(stream) => stream,
                    Err(_) => continue,
                };
                stream.set_write_timeout(Some(Duration::from_secs(2)))?;
                write_frame(
                    &mut stream,
                    HANDSHAKE,
                    &serde_json::to_vec(&json!({"v": 1, "client_id": self.app_id}))?,
                )?;
                self.stream = Some(stream);
                match self.receive(None) {
                    Ok(_) => return Ok(()),
                    Err(error) => {
                        last_error = error.to_string();
                        self.stream = None;
                    }
                }
            }
        }
        bail!("Discord IPC connection failed: {last_error}")
    }

    pub fn set_activity(&mut self, activity: Option<&Value>) -> Result<Value> {
        self.connect()?;
        self.nonce += 1;
        let nonce = format!("{}-{}", std::process::id(), self.nonce);
        let payload = json!({"cmd": "SET_ACTIVITY", "nonce": nonce, "args": {"pid": std::process::id(), "activity": activity}});
        let result = (|| {
            write_frame(
                self.stream
                    .as_mut()
                    .context("Discord IPC is disconnected")?,
                FRAME,
                &serde_json::to_vec(&payload)?,
            )?;
            let reply = self.receive(Some(&nonce))?;
            Ok(reply.get("data").cloned().unwrap_or(Value::Null))
        })();
        if result.is_err() {
            self.stream = None;
        }
        result
    }
}

pub enum Request {
    Activity(Option<Value>),
    Quit,
}

pub struct Event {
    pub connected: bool,
    pub error: Option<String>,
    pub requested: Option<Value>,
    pub acknowledged: Option<Value>,
}

pub struct Service {
    pub sender: mpsc::Sender<Request>,
    pub events: mpsc::Receiver<Event>,
    pub thread: thread::JoinHandle<()>,
}

impl Service {
    pub fn start(app_id: String, stop: Arc<AtomicBool>, refresh_ms: u64) -> Self {
        let (sender, requests) = mpsc::channel();
        let (events_tx, events) = mpsc::channel();
        let handle = thread::spawn(move || {
            let mut client = Client::new(app_id);
            let mut desired = None;
            let mut dirty = false;
            let mut next_send = Instant::now();
            let refresh = Duration::from_millis(if refresh_ms == 0 {
                15_000
            } else {
                refresh_ms.max(2000)
            });
            while !stop.load(Ordering::Relaxed) {
                match requests.recv_timeout(Duration::from_millis(200)) {
                    Ok(Request::Quit) | Err(mpsc::RecvTimeoutError::Disconnected) => break,
                    Ok(Request::Activity(activity)) => {
                        desired = activity;
                        dirty = true;
                        next_send = Instant::now();
                    }
                    Err(mpsc::RecvTimeoutError::Timeout) => {}
                }
                for request in requests.try_iter() {
                    match request {
                        Request::Activity(activity) => {
                            desired = activity;
                            dirty = true;
                            next_send = Instant::now();
                        }
                        Request::Quit => {
                            stop.store(true, Ordering::Relaxed);
                        }
                    }
                }
                if stop.load(Ordering::Relaxed) {
                    break;
                }
                if Instant::now() < next_send || (!dirty && desired.is_none()) {
                    continue;
                }
                // Avoid starting Discord connections solely to clear an empty presence.
                if desired.is_none() && client.stream.is_none() {
                    dirty = false;
                    continue;
                }
                match client.set_activity(desired.as_ref()) {
                    Ok(ack) => {
                        let _ = events_tx.send(Event {
                            connected: true,
                            error: None,
                            requested: desired.clone(),
                            acknowledged: Some(ack),
                        });
                        dirty = false;
                        next_send = Instant::now() + refresh;
                    }
                    Err(error) => {
                        let _ = events_tx.send(Event {
                            connected: false,
                            error: Some(error.to_string()),
                            requested: desired.clone(),
                            acknowledged: None,
                        });
                        dirty = true;
                        next_send = Instant::now() + Duration::from_secs(5);
                    }
                }
            }
            if client.stream.is_some() {
                let _ = client.set_activity(None);
            }
        });
        Self {
            sender,
            events,
            thread: handle,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn mock_client(error: bool) -> (Client, thread::JoinHandle<()>) {
        let (client, mut server) = UnixStream::pair().unwrap();
        let handle = thread::spawn(move || {
            let (op, request) =
                read_frame(&mut server, Instant::now() + Duration::from_secs(2)).unwrap();
            assert_eq!(op, FRAME);
            let request: Value = serde_json::from_slice(&request).unwrap();
            write_frame(&mut server, PING, b"heartbeat").unwrap();
            let (op, body) =
                read_frame(&mut server, Instant::now() + Duration::from_secs(2)).unwrap();
            assert_eq!((op, body), (PONG, b"heartbeat".to_vec()));
            // An unrelated event must not be mistaken for a successful update.
            write_frame(&mut server, FRAME, br#"{"evt":"SOME_EVENT"}"#).unwrap();
            let response = if error {
                json!({"nonce": request["nonce"], "evt": "ERROR", "data": {"code": 4000, "message": "bad activity"}})
            } else {
                json!({"nonce": request["nonce"], "data": {"details": "Correct song"}})
            };
            write_frame(&mut server, FRAME, &serde_json::to_vec(&response).unwrap()).unwrap();
        });
        (
            Client {
                app_id: "123".into(),
                stream: Some(client),
                nonce: 0,
            },
            handle,
        )
    }

    #[test]
    fn waits_for_matching_ack_and_answers_ping() {
        let (mut client, server) = mock_client(false);
        assert_eq!(
            client
                .set_activity(Some(&json!({"details": "Correct song"})))
                .unwrap()["details"],
            "Correct song"
        );
        server.join().unwrap();
    }

    #[test]
    fn surfaces_discord_error_and_reconnects_on_next_send() {
        let (mut client, server) = mock_client(true);
        assert!(
            client
                .set_activity(None)
                .unwrap_err()
                .to_string()
                .contains("bad activity")
        );
        assert!(client.stream.is_none());
        server.join().unwrap();
    }

    #[test]
    fn rejects_oversized_frame_before_allocating() {
        let (mut client, mut server) = UnixStream::pair().unwrap();
        server.write_all(&FRAME.to_le_bytes()).unwrap();
        server
            .write_all(&((MAX_FRAME + 1) as u32).to_le_bytes())
            .unwrap();
        assert!(read_frame(&mut client, Instant::now() + Duration::from_secs(2)).is_err());
    }

    #[test]
    fn partial_header_has_a_bounded_deadline() {
        let (mut client, mut server) = UnixStream::pair().unwrap();
        server.write_all(&[FRAME as u8]).unwrap();
        let start = Instant::now();
        assert!(
            read_frame(&mut client, start + Duration::from_millis(30))
                .unwrap_err()
                .to_string()
                .contains("timed out")
        );
        assert!(start.elapsed() < Duration::from_secs(1));
    }
}
