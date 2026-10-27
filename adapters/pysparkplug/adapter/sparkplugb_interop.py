# SPDX-License-Identifier: MIT
"""sparkplugb-interop adapter for pysparkplug (docs/CONTAINER_CONTRACT.md).

The edge role is pysparkplug's EdgeNode and Device. pysparkplug has no host
application, so the host role is this adapter's logic (STATE, node tracking,
seq checks) over pysparkplug's Client, which decodes payloads and resolves
aliases and datatypes from births.
"""

import datetime
import json
import os
import re
import signal
import socket
import socketserver
import struct
import sys
import threading
import time
import traceback
from urllib.parse import urlsplit

import pysparkplug as spb

ADAPTER = "pysparkplug"
ADAPTER_VERSION = os.environ.get("SPARKPLUGB_INTEROP_ADAPTER_VERSION", "dev")
UPSTREAM_VERSION = os.environ.get("SPARKPLUGB_INTEROP_UPSTREAM_VERSION", spb.__version__)
FIXTURES_DIR = os.environ.get("SPARKPLUGB_INTEROP_FIXTURES", "/fixtures")
RUN_DIR = "/run/sparkplugb-interop"
DEFAULT_READY = f"{RUN_DIR}/ready"
DEFAULT_SOCKET = f"{RUN_DIR}/control.sock"
REBIRTH = "Node Control/Rebirth"

DATATYPES = ["Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64",
             "Float", "Double", "Boolean", "String", "DateTime", "Text"]
TO_SPB = {name: getattr(spb.DataType, name.upper()) for name in DATATYPES}
FROM_SPB = {v: k for k, v in TO_SPB.items()}
MAX_SAFE = 2**53 - 1
RANGES = {"Int8": (-(2**7), 2**7 - 1), "Int16": (-(2**15), 2**15 - 1), "Int32": (-(2**31), 2**31 - 1),
          "Int64": (-MAX_SAFE, MAX_SAFE), "UInt8": (0, 255), "UInt16": (0, 65535), "UInt32": (0, 2**32 - 1),
          "UInt64": (0, MAX_SAFE), "DateTime": (0, MAX_SAFE)}
ID = re.compile(r"^[^/+#]+$")

EDGE_CALLS = ["publish-data", "device-death", "device-birth"]
HOST_CALLS = ["send-command", "request-rebirth", "nodes"]
ALL_CALLS = {"publish-data", "rebirth", "device-death", "device-birth", "send-command", "request-rebirth", "nodes"}

EXIT_FAILED, EXIT_USAGE, EXIT_CONNECT, EXIT_FIXTURE, EXIT_UNSUPPORTED = 1, 2, 3, 4, 5


class Usage(Exception):
    pass


class FixtureError(Exception):
    pass


class CallError(Exception):
    def __init__(self, code, detail):
        super().__init__(detail)
        self.code = code


# ---- output ----

_out = threading.Lock()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def emit(event, **fields):
    line = json.dumps({"event": event, "time": now(), **fields}, separators=(",", ":"), ensure_ascii=False)
    with _out:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def diag(msg):
    print(f"[{ADAPTER}] {msg}", file=sys.stderr, flush=True)


def ms():
    return int(time.time() * 1000)


# ---- flags ----

def parse_flags(args, spec, required=()):
    """spec: flag -> kind ('str', 'int', 'url', 'id', 'json'); exit 2 on any misuse."""
    out, i = {}, 0
    while i < len(args):
        a = args[i]
        if not a.startswith("--") or a[2:] not in spec:
            raise Usage(f"unknown argument {a}")
        name = a[2:]
        if name in out:
            raise Usage(f"--{name} given twice")
        if i + 1 >= len(args):
            raise Usage(f"--{name} needs a value")
        v, kind = args[i + 1], spec[name]
        if kind == "int":
            if not re.fullmatch(r"[0-9]+", v) or int(v) <= 0:
                raise Usage(f"--{name}: {v!r} is not a positive integer")
            v = int(v)
        elif kind == "url":
            u = urlsplit(v)
            if u.scheme != "tcp" or not u.hostname:
                raise Usage(f"--{name}: {v!r} is not tcp://host[:port]")
            try:
                port = u.port or 1883
            except ValueError as e:
                raise Usage(f"--{name}: {e}") from e
            v = (u.hostname, port)
        elif kind == "id":
            if not ID.match(v):
                raise Usage(f"--{name}: {v!r} is not a valid Sparkplug ID")
        elif kind == "json":
            try:
                v = json.loads(v)
            except ValueError as e:
                raise Usage(f"--{name}: {e}") from e
            if not isinstance(v, dict):
                raise Usage(f"--{name} must be a JSON object")
        out[name] = v
        i += 2
    for r in required:
        if r not in out:
            raise Usage(f"--{r} is required")
    return out


# ---- fixtures ----

def fixture_path(name_or_path):
    if "/" in name_or_path or name_or_path.endswith(".json"):
        return name_or_path
    return os.path.join(FIXTURES_DIR, name_or_path, "fixture.json")


def check_value(where, dt, v):
    if dt in RANGES:
        lo, hi = RANGES[dt]
        if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
            raise FixtureError(f"{where}: {v!r} is not a {dt}")
    elif dt in ("Float", "Double"):
        if isinstance(v, bool) or not isinstance(v, (int, float)):
            raise FixtureError(f"{where}: {v!r} is not a number")
        if dt == "Float" and struct.unpack("f", struct.pack("f", v))[0] != v:
            raise FixtureError(f"{where}: {v!r} is not exactly a Float")
    elif dt == "Boolean":
        if not isinstance(v, bool):
            raise FixtureError(f"{where}: {v!r} is not a boolean")
    elif dt in ("String", "Text"):
        if not isinstance(v, str):
            raise FixtureError(f"{where}: {v!r} is not a string")
    else:
        raise FixtureError(f"{where}: unknown datatype {dt!r}")


def load_fixture(name_or_path):
    path = fixture_path(name_or_path)
    try:
        with open(path, encoding="utf-8") as f:
            fx = json.load(f)
    except (OSError, ValueError) as e:
        raise FixtureError(f"{path}: {e}") from e
    try:
        if fx.get("schemaVersion") != "1.0":
            raise FixtureError("schemaVersion is not 1.0")
        for k in ("group", "edgeNode"):
            if not isinstance(fx.get(k), str) or not ID.match(fx[k]):
                raise FixtureError(f"{k} is not a valid Sparkplug ID")
        scopes = [("node", fx["node"]["metrics"])] + [(f"device {d['id']}", d["metrics"]) for d in fx["devices"]]
        if len({d["id"] for d in fx["devices"]}) != len(fx["devices"]):
            raise FixtureError("a device appears twice")
        aliases, types = set(), {}
        for scope, metrics in scopes:
            if not metrics:
                raise FixtureError(f"{scope} has no metrics")
            names = set()
            for m in metrics:
                where = f"{scope}, metric {m['name']!r}"
                if m["name"] in names or m["name"].startswith("Node Control/") or m["name"] == "bdSeq":
                    raise FixtureError(f"{where}: duplicate or reserved name")
                names.add(m["name"])
                if not isinstance(m["alias"], int) or isinstance(m["alias"], bool) or m["alias"] < 1 or m["alias"] in aliases:
                    raise FixtureError(f"{where}: invalid or duplicate alias")
                aliases.add(m["alias"])
                check_value(where, m["datatype"], m["value"])
                types[(scope, m["name"])] = m["datatype"]
        devices = {d["id"] for d in fx["devices"]}
        for i, u in enumerate(fx["updates"]):
            changes = [("node", c) for c in u["node"]]
            for dev, cs in u["devices"].items():
                if dev not in devices:
                    raise FixtureError(f"updates[{i}]: unknown device {dev!r}")
                changes += [(f"device {dev}", c) for c in cs]
            if not changes:
                raise FixtureError(f"updates[{i}] changes nothing")
            for scope, c in changes:
                dt = types.get((scope, c["name"]))
                if dt is None:
                    raise FixtureError(f"updates[{i}]: {scope} has no metric {c['name']!r}")
                check_value(f"updates[{i}], {scope}, metric {c['name']!r}", dt, c["value"])
    except (KeyError, TypeError, AttributeError) as e:
        raise FixtureError(f"{path}: malformed fixture ({e!r})") from e
    return fx


# ---- values ----

def to_spb_value(dt, v):
    if v is None:
        return None
    if dt == "DateTime":
        return datetime.datetime.fromtimestamp(v / 1000, tz=datetime.timezone.utc)
    if dt in ("Float", "Double"):
        return float(v)
    return v


def from_spb_value(dt, v):
    if v is None:
        return None
    if isinstance(v, datetime.datetime):
        return int(round(v.timestamp() * 1000))
    if dt in ("Float", "Double") and float(v).is_integer() and abs(v) <= MAX_SAFE:
        return int(v)
    return v


def metric_doc(m):
    dt = FROM_SPB.get(m.datatype, m.datatype.name)
    doc = {"name": m.name, "datatype": dt, "value": None if m.is_null else from_spb_value(dt, m.value)}
    if m.name is None:  # an alias the host has no birth for
        doc["alias"] = m.alias
    return doc


def payload_metrics(payload):
    if hasattr(payload, "bd_seq_metric"):
        return [payload.bd_seq_metric]
    return list(getattr(payload, "metrics", ()) or ())


def spb_metric(name, dt, value, alias=None):
    return spb.Metric(timestamp=ms(), name=name, datatype=TO_SPB[dt], value=to_spb_value(dt, value), alias=alias)


def check_payload_metric(m):
    if not isinstance(m, dict) or not isinstance(m.get("name"), str) or m.get("datatype") not in TO_SPB or "value" not in m:
        raise CallError("invalid-request", f"invalid metric {m!r}")
    try:
        check_value(m["name"], m["datatype"], m["value"])
    except FixtureError as e:
        raise CallError("invalid-request", str(e)) from e


# ---- control socket ----

class Control:
    """Serves "call" on a Unix socket: one JSON line in, one result document out."""

    def __init__(self, path, handlers):
        self.handlers = handlers
        self.lock = threading.Lock()
        try:
            os.unlink(path)
        except FileNotFoundError:
            pass
        control = self

        class Handler(socketserver.StreamRequestHandler):
            def handle(self):
                line = self.rfile.readline()
                self.wfile.write((json.dumps(control.serve(line), separators=(",", ":")) + "\n").encode())

        self.server = socketserver.ThreadingUnixStreamServer(path, Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def serve(self, line):
        start = time.monotonic()
        op = None
        try:
            req = json.loads(line)
            op = req.get("operation")
            payload = req.get("payload") or {}
            if op not in self.handlers:
                raise CallError("unsupported", f"{op} is not offered by this role")
            if not isinstance(payload, dict):
                raise CallError("invalid-request", "payload is not an object")
            with self.lock:
                result = self.handlers[op](payload)
            return result_doc(op, True, None, result, start)
        except CallError as e:
            return result_doc(op, False, {"code": e.code, "detail": str(e)}, None, start)
        except Exception as e:  # noqa: BLE001 - every failure is a result document
            diag(traceback.format_exc())
            return result_doc(op, False, {"code": "invalid-request", "detail": repr(e)}, None, start)


def result_doc(op, ok, error, result, start):
    return {"schemaVersion": "1.0", "adapter": ADAPTER, "operation": op, "ok": ok, "error": error,
            "result": result, "elapsedMs": int((time.monotonic() - start) * 1000)}


def call(args):
    if not args:
        raise Usage("call needs an operation")
    op, rest = args[0], args[1:]
    if op not in ALL_CALLS:
        raise Usage(f"unknown operation {op}")
    flags = parse_flags(rest, {"payload": "json", "timeout-ms": "int", "control-socket": "str"})
    path = flags.get("control-socket", DEFAULT_SOCKET)
    timeout = flags.get("timeout-ms", 10000) / 1000
    start = time.monotonic()
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
            s.settimeout(timeout)
            s.connect(path)
            s.sendall((json.dumps({"operation": op, "payload": flags.get("payload", {})}) + "\n").encode())
            buf = b""
            while not buf.endswith(b"\n"):
                chunk = s.recv(65536)
                if not chunk:
                    break
                buf += chunk
        doc = json.loads(buf)
    except socket.timeout:
        doc = result_doc(op, False, {"code": "timeout", "detail": "no answer in time"}, None, start)
    except (OSError, ValueError) as e:
        doc = result_doc(op, False, {"code": "not-connected", "detail": str(e)}, None, start)
    print(json.dumps(doc, separators=(",", ":")), flush=True)
    if doc["ok"]:
        return 0
    return EXIT_UNSUPPORTED if doc["error"]["code"] == "unsupported" else EXIT_FAILED


# ---- shared role plumbing ----

class Role:
    def __init__(self, flags):
        self.ready_file = flags.get("ready-file", DEFAULT_READY)
        self.socket_path = flags.get("control-socket", DEFAULT_SOCKET)
        self.timeout = flags.get("connect-timeout-ms", 10000) / 1000
        self.stopping = threading.Event()
        self.connected = threading.Event()

    def watch_connection(self, paho):
        def on_disconnect(_client, _userdata, rc):
            if not self.stopping.is_set():
                emit("connection-lost", detail=f"MQTT disconnect, rc={rc}")
                os._exit(EXIT_FAILED)
        paho.on_disconnect = on_disconnect

    def wait_connected(self):
        if not self.connected.wait(self.timeout):
            diag("no CONNACK within the connect timeout")
            sys.exit(EXIT_CONNECT)

    def mark_ready(self):
        os.makedirs(os.path.dirname(self.ready_file) or ".", exist_ok=True)
        with open(self.ready_file, "w", encoding="utf-8") as f:
            f.write("ready\n")

    def run_until_term(self, stop):
        done = threading.Event()

        def on_term(_sig, _frame):
            self.stopping.set()
            done.set()
        signal.signal(signal.SIGTERM, on_term)
        signal.signal(signal.SIGINT, on_term)
        done.wait()
        try:
            stop()
        finally:
            emit("stopped")
            os._exit(0)


def connect_paho(client, host, port, timeout):
    try:
        socket.getaddrinfo(host, port)
    except OSError as e:
        diag(f"cannot resolve {host}: {e}")
        sys.exit(EXIT_CONNECT)
    return client


# ---- edge ----

class Edge(Role):
    def __init__(self, flags):
        super().__init__(flags)
        self.fx = load_fixture(flags.get("fixture", "baseline"))
        self.fixture_name = self.fx["name"]
        self.group = flags.get("group", self.fx["group"])
        self.node_id = flags.get("edge-node", self.fx["edgeNode"])
        self.host, self.port = flags["broker"]
        self.client_id = flags.get("client-id", f"sparkplugb-interop-{ADAPTER}-edge-{self.group}-{self.node_id}")
        self.node_meta = {m["name"]: m for m in self.fx["node"]["metrics"]}
        self.dev_meta = {d["id"]: {m["name"]: m for m in d["metrics"]} for d in self.fx["devices"]}
        self.values = {("", m["name"]): m["value"] for m in self.fx["node"]["metrics"]}
        for d in self.fx["devices"]:
            self.values.update({(d["id"], m["name"]): m["value"] for m in d["metrics"]})
        self.online_devices = [d["id"] for d in self.fx["devices"]]
        self.bd_seq = None
        self.births = 0

    def node_metric(self, name, value=None):
        m = self.node_meta[name]
        return spb_metric(name, m["datatype"], self.values[("", name)] if value is None else value, m["alias"])

    def device(self, dev):
        metrics = [spb_metric(n, m["datatype"], self.values[(dev, n)], m["alias"]) for n, m in self.dev_meta[dev].items()]
        return spb.Device(dev, metrics, cmd_callback=lambda _e, msg: self.on_command(dev, msg))

    def instrument(self, client):
        publish = client.publish

        def wrapper(message, **kw):
            publish(message, **kw)
            t = message.topic
            payload = message.payload
            mt = t.message_type
            bd = next((m.value for m in payload_metrics(payload) if m.name == "bdSeq"), None)
            is_bd = mt in (spb.MessageType.NBIRTH, spb.MessageType.NDEATH)
            if mt == spb.MessageType.NBIRTH:
                self.bd_seq, self.births = bd, 0
            emit("published", type=mt.value, device=t.device_id,
                 seq=None if mt == spb.MessageType.NDEATH else getattr(payload, "seq", None), bdSeq=bd if is_bd else None)
            if mt == spb.MessageType.DBIRTH:
                self.births += 1
            if mt in (spb.MessageType.NBIRTH, spb.MessageType.DBIRTH) and self.births == len(self.online_devices):
                self.connected.set()
        client.publish = wrapper

    def on_command(self, dev, message):
        metrics = [metric_doc(m) for m in message.payload.metrics]
        applied = False
        try:
            changes = []
            for m in metrics:
                meta = (self.node_meta if dev is None else self.dev_meta[dev]).get(m["name"])
                if meta and meta.get("writable") and m["datatype"] == meta["datatype"]:
                    changes.append((m["name"], meta, m["value"]))
            if changes:
                for name, meta, value in changes:
                    self.values[(dev or "", name)] = value
                upd = [spb_metric(n, meta["datatype"], v, meta["alias"]) for n, meta, v in changes]
                with self.control.lock:
                    if dev is None:
                        self.node.update(upd)
                    else:
                        self.node.update_device(dev, upd)
                applied = True
        finally:
            emit("command", device=dev, metrics=metrics, applied=applied)

    def run(self):
        client = connect_paho(spb.Client(client_id=self.client_id), self.host, self.port, self.timeout)
        self.instrument(client)
        node_metrics = [spb_metric(REBIRTH, "Boolean", False)] + [self.node_metric(n) for n in self.node_meta]
        self.node = spb.EdgeNode(self.group, self.node_id, node_metrics, client=client,
                                 cmd_callback=lambda _e, msg: self.on_command(None, msg))
        for dev in self.online_devices:
            self.node.register(self.device(dev))
        self.control = Control(self.socket_path, {
            "publish-data": self.publish_data, "device-death": self.device_death, "device-birth": self.device_birth})
        self.watch_connection(client._client)  # pysparkplug exposes no connection hook of its own
        try:
            self.node.connect(self.host, port=self.port, blocking=False)
        except OSError as e:
            diag(f"connect: {e}")
            sys.exit(EXIT_CONNECT)
        self.wait_connected()
        self.mark_ready()
        emit("ready", adapter=ADAPTER, fixture=self.fixture_name, group=self.group, edgeNode=self.node_id, bdSeq=self.bd_seq)
        self.run_until_term(self.node.disconnect)

    def publish_data(self, p):
        i = p.get("update")
        if isinstance(i, bool) or not isinstance(i, int) or not 0 <= i < len(self.fx["updates"]):
            raise CallError("invalid-request", f"no update {i!r}")
        u = self.fx["updates"][i]
        published = []
        if u["node"]:
            for c in u["node"]:
                self.values[("", c["name"])] = c["value"]
            self.node.update([self.node_metric(c["name"]) for c in u["node"]])
            published.append("NDATA")
        for d in self.fx["devices"]:
            changes = u["devices"].get(d["id"])
            if not changes:
                continue
            if d["id"] not in self.online_devices:
                raise CallError("invalid-request", f"device {d['id']} is offline")
            for c in changes:
                self.values[(d["id"], c["name"])] = c["value"]
            meta = self.dev_meta[d["id"]]
            self.node.update_device(d["id"], [spb_metric(c["name"], meta[c["name"]]["datatype"], c["value"],
                                                         meta[c["name"]]["alias"]) for c in changes])
            published.append(f"DDATA/{d['id']}")
        return {"published": published}

    def device_death(self, p):
        dev = p.get("device")
        if dev not in self.online_devices:
            raise CallError("invalid-request", f"no online device {dev!r}")
        self.node.deregister(dev)
        self.online_devices.remove(dev)
        return {"published": [f"DDEATH/{dev}"]}

    def device_birth(self, p):
        dev = p.get("device")
        if dev not in self.dev_meta or dev in self.online_devices:
            raise CallError("invalid-request", f"no offline device {dev!r}")
        self.node.register(self.device(dev))
        self.online_devices.append(dev)
        return {"published": [f"DBIRTH/{dev}"]}


# ---- host ----

class HostClient(spb.Client):
    """A Client whose decoding errors do not stop paho's network loop."""

    def _handle_message(self, mqtt_message):
        try:
            return super()._handle_message(mqtt_message)
        except Exception as e:  # noqa: BLE001
            return e


class Host(Role):
    def __init__(self, flags):
        super().__init__(flags)
        self.host_id = flags["host-id"]
        self.host, self.port = flags["broker"]
        self.client_id = flags.get("client-id", f"sparkplugb-interop-{ADAPTER}-host-{self.host_id}")
        self.nodes = {}
        self.track = threading.Lock()
        self.ts = ms()

    def state_message(self, online):
        return spb.Message(topic=spb.Topic(message_type=spb.MessageType.STATE, sparkplug_host_id=self.host_id),
                           payload=spb.State(timestamp=self.ts, online=online), qos=spb.QoS.AT_LEAST_ONCE, retain=True)

    def run(self):
        client = connect_paho(HostClient(client_id=self.client_id), self.host, self.port, self.timeout)
        self.client = client
        client.set_will(self.state_message(False))
        paho = client._client
        self.watch_connection(paho)
        subscribed = threading.Event()

        def on_connect(_client):
            for mt in (spb.MessageType.NBIRTH, spb.MessageType.NDATA, spb.MessageType.NDEATH):
                client.subscribe(spb.Topic(group_id="+", message_type=mt, edge_node_id="+"), spb.QoS.AT_LEAST_ONCE, self.on_message)
            for mt in (spb.MessageType.DBIRTH, spb.MessageType.DDATA, spb.MessageType.DDEATH):
                client.subscribe(spb.Topic(group_id="+", message_type=mt, edge_node_id="+", device_id="+"),
                                 spb.QoS.AT_LEAST_ONCE, self.on_message)
            client.subscribe(spb.Topic(message_type=spb.MessageType.STATE, sparkplug_host_id=self.host_id),
                             spb.QoS.AT_LEAST_ONCE, lambda _c, _m: None)
            paho.publish(str(spb.Topic(message_type=spb.MessageType.STATE, sparkplug_host_id=self.host_id)),
                         self.state_message(True).payload.encode(), qos=1, retain=True).wait_for_publish(5)
            self.connected.set()
            subscribed.set()
        try:
            client.connect(self.host, port=self.port, blocking=False, callback=lambda c: threading.Thread(target=on_connect, args=(c,), daemon=True).start())
        except OSError as e:
            diag(f"connect: {e}")
            sys.exit(EXIT_CONNECT)
        self.wait_connected()
        subscribed.wait()
        self.control = Control(self.socket_path, {
            "send-command": self.send_command, "request-rebirth": self.request_rebirth, "nodes": self.list_nodes})
        self.mark_ready()
        emit("ready", adapter=ADAPTER, hostId=self.host_id)

        def stop():
            paho.publish(str(spb.Topic(message_type=spb.MessageType.STATE, sparkplug_host_id=self.host_id)),
                         self.state_message(False).payload.encode(), qos=1, retain=True).wait_for_publish(5)
            client.disconnect()
        self.run_until_term(stop)

    def on_message(self, _client, message):
        try:
            self.handle(message)
        except Exception:  # noqa: BLE001
            diag(traceback.format_exc())

    def handle(self, message):
        if isinstance(message, Exception):
            # A data message whose aliases or datatypes no birth defines.
            diag(f"cannot decode a message: {message!r}")
            return
        t = message.topic
        mt = t.message_type
        key = (t.group_id, t.edge_node_id)
        payload = message.payload
        metrics = payload_metrics(payload)
        bd = next((m.value for m in metrics if m.name == "bdSeq"), None)
        seq = getattr(payload, "seq", None)
        if mt == spb.MessageType.NDEATH:
            seq = None
        emit("message", type=mt.value, group=t.group_id, edgeNode=t.edge_node_id, device=t.device_id, seq=seq,
             bdSeq=bd if mt in (spb.MessageType.NBIRTH, spb.MessageType.NDEATH) else None,
             metrics=[metric_doc(m) for m in metrics if m.name != "bdSeq"])
        with self.track:
            node = self.nodes.get(key)
            if mt == spb.MessageType.NBIRTH:
                self.nodes[key] = {"online": True, "bdSeq": bd, "seq": seq, "devices": {}}
                return
            if mt == spb.MessageType.NDEATH:
                if node and node["bdSeq"] == bd:
                    node["online"] = False
                    for d in node["devices"]:
                        node["devices"][d] = False
                return
            if node is None or not node["online"]:
                reason = "unknown-node"
            else:
                expected = (node["seq"] + 1) % 256
                node["seq"] = seq
                reason = None if seq == expected else "seq-gap"
                if mt == spb.MessageType.DBIRTH:
                    node["devices"][t.device_id] = True
                elif mt == spb.MessageType.DDEATH:
                    node["devices"][t.device_id] = False
        if reason:
            emit("rebirth-requested", group=t.group_id, edgeNode=t.edge_node_id, reason=reason)
            self.publish_cmd(t.group_id, t.edge_node_id, None, [spb_metric(REBIRTH, "Boolean", True)])

    def publish_cmd(self, group, node, device, metrics):
        mt = spb.MessageType.DCMD if device else spb.MessageType.NCMD
        payload = (spb.DCmd if device else spb.NCmd)(timestamp=ms(), metrics=tuple(metrics))
        self.client.publish(spb.Message(topic=spb.Topic(group_id=group, message_type=mt, edge_node_id=node, device_id=device),
                                        payload=payload, qos=spb.QoS.AT_MOST_ONCE, retain=False), include_dtypes=True)

    @staticmethod
    def target(p):
        for k in ("group", "edgeNode"):
            if not isinstance(p.get(k), str) or not ID.match(p[k]):
                raise CallError("invalid-request", f"{k} is missing or invalid")
        dev = p.get("device")
        if dev is not None and (not isinstance(dev, str) or not ID.match(dev)):
            raise CallError("invalid-request", "device is invalid")
        return p["group"], p["edgeNode"], dev

    def send_command(self, p):
        group, node, dev = self.target(p)
        metrics = p.get("metrics")
        if not isinstance(metrics, list) or not metrics:
            raise CallError("invalid-request", "metrics is missing or empty")
        for m in metrics:
            check_payload_metric(m)
        self.publish_cmd(group, node, dev, [spb_metric(m["name"], m["datatype"], m["value"]) for m in metrics])
        return {"published": [("DCMD/" + dev) if dev else "NCMD"]}

    def request_rebirth(self, p):
        group, node, _ = self.target({**p, "device": None})
        self.publish_cmd(group, node, None, [spb_metric(REBIRTH, "Boolean", True)])
        return {"published": ["NCMD"]}

    def list_nodes(self, _p):
        with self.track:
            nodes = [{"group": g, "edgeNode": n, "online": s["online"], "bdSeq": s["bdSeq"],
                      "devices": [{"device": d, "online": o} for d, o in sorted(s["devices"].items())]}
                     for (g, n), s in sorted(self.nodes.items())]
        return {"nodes": nodes}


# ---- commands ----

def capabilities():
    return {
        "schemaVersion": "1.0", "adapter": ADAPTER, "adapterVersion": ADAPTER_VERSION, "protocol": "sparkplug-b-3.0.0",
        "upstream": {"name": "pysparkplug", "version": UPSTREAM_VERSION, "license": "Apache-2.0", "language": "Python",
                     "independentEngine": True},
        "roles": {
            "edge": {"supported": True, "fixtures": ["baseline", "scalars"], "calls": EDGE_CALLS,
                     "features": {"aliases": False, "rebirthCommand": False, "writes": True, "primaryHost": False,
                                  "gracefulDeath": True}},
            "host": {"supported": True, "datatypes": DATATYPES, "calls": HOST_CALLS,
                     "features": {"aliasResolution": True, "seqCheck": True, "gracefulOffline": True}},
        },
    }


def main(argv):
    if not argv:
        argv = ["edge"]
    cmd, args = argv[0], argv[1:]
    try:
        if cmd == "print-capabilities":
            parse_flags(args, {})
            print(json.dumps(capabilities(), separators=(",", ":")))
            return 0
        if cmd == "print-fixture":
            if len(args) > 1:
                raise Usage("print-fixture takes at most one name")
            name = args[0] if args else "baseline"
            path = os.path.join(FIXTURES_DIR, name, "fixture.json")
            if not ID.match(name) or not os.path.isfile(path):
                diag(f"no baked fixture {name!r}")
                return EXIT_FAILED
            with open(path, "rb") as f:
                sys.stdout.buffer.write(f.read())
            return 0
        if cmd == "version":
            parse_flags(args, {})
            print(f"sparkplugb-interop-{ADAPTER} {ADAPTER_VERSION} (pysparkplug {UPSTREAM_VERSION})")
            return 0
        if cmd == "call":
            return call(args)
        common = {"broker": "url", "client-id": "str", "connect-timeout-ms": "int", "ready-file": "str", "control-socket": "str"}
        if cmd == "edge":
            flags = parse_flags(args, {**common, "fixture": "str", "group": "id", "edge-node": "id", "primary-host": "id"}, ["broker"])
            if "primary-host" in flags:
                diag("--primary-host is not supported: pysparkplug publishes NBIRTH as soon as it connects")
                return EXIT_UNSUPPORTED
            Edge(flags).run()
            return 0
        if cmd == "host":
            flags = parse_flags(args, {**common, "host-id": "id"}, ["broker", "host-id"])
            Host(flags).run()
            return 0
        raise Usage(f"unknown command {cmd}")
    except Usage as e:
        diag(f"usage: {e}")
        return EXIT_USAGE
    except FixtureError as e:
        diag(f"fixture: {e}")
        return EXIT_FIXTURE


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
