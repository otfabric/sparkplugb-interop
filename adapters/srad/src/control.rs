// SPDX-License-Identifier: MIT
//! The control socket of `call`: one JSON line in, one result document out.

use std::future::Future;
use std::io::{BufRead, BufReader, ErrorKind, Write};
use std::pin::Pin;
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde_json::{json, Map, Value};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader as AsyncBufReader};
use tokio::net::UnixListener;
use tokio::sync::Mutex;

use crate::out::{diag, print_line, ADAPTER};

pub const ALL_CALLS: [&str; 7] = [
    "publish-data",
    "rebirth",
    "device-death",
    "device-birth",
    "send-command",
    "request-rebirth",
    "nodes",
];

/// A failed call: an error code of the contract and a detail.
pub struct CallError(pub &'static str, pub String);

impl CallError {
    pub fn invalid(detail: impl Into<String>) -> Self {
        CallError("invalid-request", detail.into())
    }
}

pub type CallResult = Result<Value, CallError>;
pub type Handler =
    Arc<dyn Fn(String, Map<String, Value>) -> Pin<Box<dyn Future<Output = CallResult> + Send>> + Send + Sync>;

pub fn result_doc(op: Option<&str>, res: CallResult, start: Instant) -> Value {
    let (ok, error, result) = match res {
        Ok(r) => (true, Value::Null, r),
        Err(CallError(code, detail)) => (false, json!({"code": code, "detail": detail}), Value::Null),
    };
    json!({"schemaVersion": "1.0", "adapter": ADAPTER, "operation": op, "ok": ok, "error": error,
           "result": result, "elapsedMs": start.elapsed().as_millis() as u64})
}

/// Serves the socket at path. `ops` are the operations of the running role;
/// calls run one at a time.
pub fn serve(path: &str, ops: &'static [&'static str], handler: Handler) -> std::io::Result<()> {
    let _ = std::fs::remove_file(path);
    let listener = UnixListener::bind(path)?;
    let lock = Arc::new(Mutex::new(()));
    tokio::spawn(async move {
        loop {
            let Ok((stream, _)) = listener.accept().await else {
                continue;
            };
            let (handler, lock) = (handler.clone(), lock.clone());
            tokio::spawn(async move {
                let (r, mut w) = stream.into_split();
                let mut line = String::new();
                if AsyncBufReader::new(r).read_line(&mut line).await.is_err() {
                    return;
                }
                let start = Instant::now();
                let req: Value = serde_json::from_str(&line).unwrap_or(Value::Null);
                let op = req.get("operation").and_then(Value::as_str).map(str::to_string);
                let res = match (&op, req.get("payload")) {
                    (Some(o), _) if !ops.contains(&o.as_str()) => {
                        Err(CallError("unsupported", format!("{o} is not offered by this role")))
                    }
                    (None, _) => Err(CallError::invalid("no operation")),
                    (Some(o), p) => match p {
                        None | Some(Value::Null) => {
                            let _g = lock.lock().await;
                            handler(o.clone(), Map::new()).await
                        }
                        Some(Value::Object(m)) => {
                            let _g = lock.lock().await;
                            handler(o.clone(), m.clone()).await
                        }
                        Some(_) => Err(CallError::invalid("payload is not an object")),
                    },
                };
                let doc = result_doc(op.as_deref(), res, start);
                let _ = w.write_all(format!("{doc}\n").as_bytes()).await;
            });
        }
    });
    Ok(())
}

/// `call`: sends one operation to the running role and prints its result.
pub fn call(op: &str, payload: Value, timeout_ms: u64, path: &str) -> i32 {
    let start = Instant::now();
    let doc = match exchange(op, &payload, Duration::from_millis(timeout_ms), path) {
        Ok(doc) => doc,
        Err(e) if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut) => {
            result_doc(Some(op), Err(CallError("timeout", "no answer in time".into())), start)
        }
        Err(e) => {
            diag(format!("call {op}: {e}"));
            result_doc(Some(op), Err(CallError("not-connected", e.to_string())), start)
        }
    };
    print_line(&doc);
    if doc["ok"] == json!(true) {
        0
    } else if doc["error"]["code"] == json!("unsupported") {
        5
    } else {
        1
    }
}

fn exchange(op: &str, payload: &Value, timeout: Duration, path: &str) -> std::io::Result<Value> {
    let mut s = std::os::unix::net::UnixStream::connect(path)?;
    s.set_read_timeout(Some(timeout))?;
    s.set_write_timeout(Some(timeout))?;
    s.write_all(format!("{}\n", json!({"operation": op, "payload": payload})).as_bytes())?;
    let mut line = String::new();
    BufReader::new(s).read_line(&mut line)?;
    serde_json::from_str(&line).map_err(|e| std::io::Error::new(ErrorKind::InvalidData, e))
}
