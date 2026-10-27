// SPDX-License-Identifier: MIT
//! stdout carries JSON Lines only; diagnostics and srad's own logs go to stderr.

use std::io::Write;
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Map, Value};

pub const ADAPTER: &str = "srad";

static OUT: Mutex<()> = Mutex::new(());

/// Milliseconds since the epoch.
pub fn ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

/// RFC 3339 in UTC with milliseconds, e.g. 2026-10-27T10:00:00.123Z.
pub fn now() -> String {
    let t = ms();
    let (secs, millis) = (t / 1000, t % 1000);
    let (days, rem) = ((secs / 86400) as i64, secs % 86400);
    // Civil date from days since 1970-01-01 (Howard Hinnant's algorithm).
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = yoe + era * 400 + i64::from(m <= 2);
    format!(
        "{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}.{millis:03}Z",
        rem / 3600,
        rem / 60 % 60,
        rem % 60
    )
}

/// Writes one event line: {"event": ..., "time": ..., fields...}.
pub fn emit(event: &str, fields: Value) {
    let mut doc = Map::new();
    doc.insert("event".into(), json!(event));
    doc.insert("time".into(), json!(now()));
    if let Value::Object(f) = fields {
        doc.extend(f);
    }
    print_line(&Value::Object(doc));
}

/// Writes one JSON document as a line on stdout.
pub fn print_line(doc: &Value) {
    let _guard = OUT.lock().unwrap_or_else(|e| e.into_inner());
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{doc}");
    let _ = out.flush();
}

pub fn diag(msg: impl AsRef<str>) {
    eprintln!("[{ADAPTER}] {}", msg.as_ref());
}

/// Sends srad's (and rumqttc's) log records to stderr. The level is warn,
/// or SPARKPLUGB_INTEROP_LOG (error, warn, info, debug, trace).
struct StderrLog;

impl log::Log for StderrLog {
    fn enabled(&self, m: &log::Metadata) -> bool {
        m.level() <= log::max_level()
    }
    fn log(&self, r: &log::Record) {
        if self.enabled(r.metadata()) {
            eprintln!("[{ADAPTER}] {} {}: {}", r.level(), r.target(), r.args());
        }
    }
    fn flush(&self) {}
}

pub fn init_log() {
    let level = match std::env::var("SPARKPLUGB_INTEROP_LOG").as_deref() {
        Ok("error") => log::LevelFilter::Error,
        Ok("info") => log::LevelFilter::Info,
        Ok("debug") => log::LevelFilter::Debug,
        Ok("trace") => log::LevelFilter::Trace,
        _ => log::LevelFilter::Warn,
    };
    if log::set_logger(&StderrLog).is_ok() {
        log::set_max_level(level);
    }
}
