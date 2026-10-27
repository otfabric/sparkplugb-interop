// SPDX-License-Identifier: MIT
//
// sparkplugb-interop adapter for the Eclipse Tahu JavaScript sparkplug-client
// (docs/CONTAINER_CONTRACT.md).
//
// sparkplug-client is an Edge Node client: it owns the MQTT connection, the
// will, bdSeq and seq, publishes NDEATH on stop and decodes NCMD and DCMD. The
// application supplies every birth and data payload and decides what a
// command does, as Tahu's own example does, so births, aliases, writes and
// the rebirth on NCMD are this adapter's logic over the client. It has no
// host application and no STATE awareness: the host role and --primary-host
// are unsupported.

"use strict";

const fs = require("fs");
const net = require("net");
const path = require("path");
const sparkplug = require("sparkplug-client");

const ADAPTER = "tahu-js";
const UPSTREAM_PACKAGE = require("sparkplug-client/package.json");
const ADAPTER_VERSION = process.env.SPARKPLUGB_INTEROP_ADAPTER_VERSION || "dev";
const UPSTREAM_VERSION = process.env.SPARKPLUGB_INTEROP_UPSTREAM_VERSION || UPSTREAM_PACKAGE.version;
const FIXTURES_DIR = process.env.SPARKPLUGB_INTEROP_FIXTURES || "/fixtures";
const RUN_DIR = "/run/sparkplugb-interop";
const DEFAULT_READY = `${RUN_DIR}/ready`;
const DEFAULT_SOCKET = `${RUN_DIR}/control.sock`;
const REBIRTH = "Node Control/Rebirth";

const DATATYPES = ["Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64",
    "Float", "Double", "Boolean", "String", "DateTime", "Text"];
const MAX_SAFE = Number.MAX_SAFE_INTEGER;
const RANGES = {
    Int8: [-(2 ** 7), 2 ** 7 - 1], Int16: [-(2 ** 15), 2 ** 15 - 1], Int32: [-(2 ** 31), 2 ** 31 - 1],
    Int64: [-MAX_SAFE, MAX_SAFE], UInt8: [0, 255], UInt16: [0, 65535], UInt32: [0, 2 ** 32 - 1],
    UInt64: [0, MAX_SAFE], DateTime: [0, MAX_SAFE],
};
const ID = /^[^/+#]+$/;

const EDGE_CALLS = ["publish-data", "rebirth", "device-death", "device-birth"];
const ALL_CALLS = new Set(["publish-data", "rebirth", "device-death", "device-birth", "send-command", "request-rebirth", "nodes"]);

const EXIT_FAILED = 1, EXIT_USAGE = 2, EXIT_CONNECT = 3, EXIT_FIXTURE = 4, EXIT_UNSUPPORTED = 5;

class Usage extends Error {}
class FixtureError extends Error {}
class CallError extends Error {
    constructor(code, detail) {
        super(detail);
        this.code = code;
    }
}

// ---- output ----

function emit(event, fields = {}) {
    fs.writeSync(1, JSON.stringify({ event, time: new Date().toISOString(), ...fields }) + "\n");
}

function diag(msg) {
    fs.writeSync(2, `[${ADAPTER}] ${msg}\n`);
}

// ---- flags ----

function parseFlags(args, spec, required = []) {
    const out = {};
    for (let i = 0; i < args.length; i += 2) {
        const a = args[i];
        const name = a.startsWith("--") ? a.slice(2) : null;
        if (name === null || !Object.prototype.hasOwnProperty.call(spec, name)) throw new Usage(`unknown argument ${a}`);
        if (Object.prototype.hasOwnProperty.call(out, name)) throw new Usage(`--${name} given twice`);
        if (i + 1 >= args.length) throw new Usage(`--${name} needs a value`);
        let v = args[i + 1];
        switch (spec[name]) {
            case "int":
                if (!/^[0-9]+$/.test(v) || Number(v) <= 0) throw new Usage(`--${name}: ${JSON.stringify(v)} is not a positive integer`);
                v = Number(v);
                break;
            case "url": {
                let u;
                try {
                    u = new URL(v);
                } catch (e) {
                    throw new Usage(`--${name}: ${JSON.stringify(v)} is not tcp://host[:port]`);
                }
                if (u.protocol !== "tcp:" || !u.hostname) throw new Usage(`--${name}: ${JSON.stringify(v)} is not tcp://host[:port]`);
                v = { host: u.hostname.replace(/^\[|\]$/g, ""), port: u.port ? Number(u.port) : 1883 };
                break;
            }
            case "id":
                if (!ID.test(v)) throw new Usage(`--${name}: ${JSON.stringify(v)} is not a valid Sparkplug ID`);
                break;
            case "json":
                try {
                    v = JSON.parse(v);
                } catch (e) {
                    throw new Usage(`--${name}: ${e.message}`);
                }
                if (v === null || typeof v !== "object" || Array.isArray(v)) throw new Usage(`--${name} must be a JSON object`);
                break;
            default:
                break;
        }
        out[name] = v;
    }
    for (const r of required) {
        if (!Object.prototype.hasOwnProperty.call(out, r)) throw new Usage(`--${r} is required`);
    }
    return out;
}

// ---- fixtures ----

function fixturePath(nameOrPath) {
    if (nameOrPath.includes("/") || nameOrPath.endsWith(".json")) return nameOrPath;
    return path.join(FIXTURES_DIR, nameOrPath, "fixture.json");
}

function checkValue(where, dt, v) {
    if (Object.prototype.hasOwnProperty.call(RANGES, dt)) {
        const [lo, hi] = RANGES[dt];
        if (!Number.isInteger(v) || v < lo || v > hi) throw new FixtureError(`${where}: ${JSON.stringify(v)} is not a ${dt}`);
    } else if (dt === "Float" || dt === "Double") {
        if (typeof v !== "number" || !Number.isFinite(v)) throw new FixtureError(`${where}: ${JSON.stringify(v)} is not a number`);
        if (dt === "Float" && Math.fround(v) !== v) throw new FixtureError(`${where}: ${v} is not exactly a Float`);
    } else if (dt === "Boolean") {
        if (typeof v !== "boolean") throw new FixtureError(`${where}: ${JSON.stringify(v)} is not a boolean`);
    } else if (dt === "String" || dt === "Text") {
        if (typeof v !== "string") throw new FixtureError(`${where}: ${JSON.stringify(v)} is not a string`);
    } else {
        throw new FixtureError(`${where}: unknown datatype ${JSON.stringify(dt)}`);
    }
}

function loadFixture(nameOrPath) {
    const file = fixturePath(nameOrPath);
    let fx;
    try {
        fx = JSON.parse(fs.readFileSync(file, "utf8"));
    } catch (e) {
        throw new FixtureError(`${file}: ${e.message}`);
    }
    try {
        if (fx.schemaVersion !== "1.0") throw new FixtureError("schemaVersion is not 1.0");
        for (const k of ["group", "edgeNode"]) {
            if (typeof fx[k] !== "string" || !ID.test(fx[k])) throw new FixtureError(`${k} is not a valid Sparkplug ID`);
        }
        const scopes = [["node", fx.node.metrics], ...fx.devices.map((d) => [`device ${d.id}`, d.metrics])];
        if (new Set(fx.devices.map((d) => d.id)).size !== fx.devices.length) throw new FixtureError("a device appears twice");
        const aliases = new Set();
        const types = new Map();
        for (const [scope, metrics] of scopes) {
            if (!Array.isArray(metrics) || metrics.length === 0) throw new FixtureError(`${scope} has no metrics`);
            const names = new Set();
            for (const m of metrics) {
                const where = `${scope}, metric ${JSON.stringify(m.name)}`;
                if (typeof m.name !== "string" || names.has(m.name) || m.name.startsWith("Node Control/") || m.name === "bdSeq") {
                    throw new FixtureError(`${where}: duplicate or reserved name`);
                }
                names.add(m.name);
                if (!Number.isInteger(m.alias) || m.alias < 1 || aliases.has(m.alias)) throw new FixtureError(`${where}: invalid or duplicate alias`);
                aliases.add(m.alias);
                checkValue(where, m.datatype, m.value);
                types.set(`${scope}\u0000${m.name}`, m.datatype);
            }
        }
        const devices = new Set(fx.devices.map((d) => d.id));
        fx.updates.forEach((u, i) => {
            const changes = u.node.map((c) => ["node", c]);
            for (const [dev, cs] of Object.entries(u.devices)) {
                if (!devices.has(dev)) throw new FixtureError(`updates[${i}]: unknown device ${JSON.stringify(dev)}`);
                changes.push(...cs.map((c) => [`device ${dev}`, c]));
            }
            if (changes.length === 0) throw new FixtureError(`updates[${i}] changes nothing`);
            for (const [scope, c] of changes) {
                const dt = types.get(`${scope}\u0000${c.name}`);
                if (dt === undefined) throw new FixtureError(`updates[${i}]: ${scope} has no metric ${JSON.stringify(c.name)}`);
                checkValue(`updates[${i}], ${scope}, metric ${JSON.stringify(c.name)}`, dt, c.value);
            }
        });
    } catch (e) {
        if (e instanceof FixtureError) throw e;
        throw new FixtureError(`${file}: malformed fixture (${e.message})`);
    }
    return fx;
}

// ---- values ----

// sparkplug-payload decodes 64-bit values as Long objects (and UInt32 as a
// signed Long); documents carry JSON numbers.
function fromSpbValue(dt, v) {
    if (v === null || v === undefined) return null;
    if (typeof v === "object" && typeof v.toNumber === "function") {
        if (dt === "UInt32" || dt === "UInt64" || dt === "DateTime") return v.toUnsigned().toNumber();
        return v.toSigned().toNumber();
    }
    if (dt === "UInt32" && typeof v === "number" && v < 0) return v >>> 0;
    if (dt === "Int8") return (v << 24) >> 24;
    if (dt === "Int16") return (v << 16) >> 16;
    return v;
}

function ms() {
    return Date.now();
}

// ---- control socket ----

function resultDoc(op, ok, error, result, start) {
    return { schemaVersion: "1.0", adapter: ADAPTER, operation: op, ok, error, result, elapsedMs: Date.now() - start };
}

class Control {
    constructor(socketPath, handlers) {
        this.handlers = handlers;
        try {
            fs.unlinkSync(socketPath);
        } catch (e) {
            if (e.code !== "ENOENT") throw e;
        }
        this.server = net.createServer((conn) => {
            let buf = "";
            conn.setEncoding("utf8");
            conn.on("data", (chunk) => {
                buf += chunk;
                const nl = buf.indexOf("\n");
                if (nl < 0) return;
                const doc = this.serve(buf.slice(0, nl));
                conn.end(JSON.stringify(doc) + "\n");
            });
            conn.on("error", () => {});
        });
        this.server.listen(socketPath);
    }

    serve(line) {
        const start = Date.now();
        let op = null;
        try {
            let req;
            try {
                req = JSON.parse(line);
            } catch (e) {
                throw new CallError("invalid-request", `not JSON: ${e.message}`);
            }
            op = req.operation === undefined ? null : req.operation;
            const payload = req.payload === undefined || req.payload === null ? {} : req.payload;
            if (!Object.prototype.hasOwnProperty.call(this.handlers, op)) throw new CallError("unsupported", `${op} is not offered by this role`);
            if (typeof payload !== "object" || Array.isArray(payload)) throw new CallError("invalid-request", "payload is not an object");
            return resultDoc(op, true, null, this.handlers[op](payload), start);
        } catch (e) {
            if (e instanceof CallError) return resultDoc(op, false, { code: e.code, detail: e.message }, null, start);
            diag(e.stack || String(e));
            return resultDoc(op, false, { code: "invalid-request", detail: String(e) }, null, start);
        }
    }
}

function call(args) {
    if (args.length === 0) throw new Usage("call needs an operation");
    const op = args[0];
    if (!ALL_CALLS.has(op)) throw new Usage(`unknown operation ${op}`);
    const flags = parseFlags(args.slice(1), { payload: "json", "timeout-ms": "int", "control-socket": "str" });
    const socketPath = flags["control-socket"] || DEFAULT_SOCKET;
    const timeout = flags["timeout-ms"] || 10000;
    const start = Date.now();
    return new Promise((resolve) => {
        let buf = "";
        let done = false;
        const finish = (doc) => {
            if (done) return;
            done = true;
            clearTimeout(timer);
            sock.destroy();
            fs.writeSync(1, JSON.stringify(doc) + "\n");
            if (doc.ok) resolve(0);
            else resolve(doc.error && doc.error.code === "unsupported" ? EXIT_UNSUPPORTED : EXIT_FAILED);
        };
        const timer = setTimeout(() => finish(resultDoc(op, false, { code: "timeout", detail: "no answer in time" }, null, start)), timeout);
        const sock = net.connect(socketPath, () => {
            sock.write(JSON.stringify({ operation: op, payload: flags.payload || {} }) + "\n");
        });
        sock.setEncoding("utf8");
        sock.on("data", (chunk) => {
            buf += chunk;
        });
        sock.on("end", () => {
            try {
                finish(JSON.parse(buf));
            } catch (e) {
                finish(resultDoc(op, false, { code: "not-connected", detail: `no result: ${e.message}` }, null, start));
            }
        });
        sock.on("error", (e) => finish(resultDoc(op, false, { code: "not-connected", detail: e.message }, null, start)));
    });
}

// ---- edge ----

class Edge {
    constructor(flags) {
        this.fx = loadFixture(flags.fixture || "baseline");
        this.fixtureName = this.fx.name;
        this.group = flags.group || this.fx.group;
        this.nodeId = flags["edge-node"] || this.fx.edgeNode;
        this.broker = flags.broker;
        this.clientId = flags["client-id"] || `sparkplugb-interop-${ADAPTER}-edge-${this.group}-${this.nodeId}`;
        this.timeout = flags["connect-timeout-ms"] || 10000;
        this.readyFile = flags["ready-file"] || DEFAULT_READY;
        this.socketPath = flags["control-socket"] || DEFAULT_SOCKET;
        this.nodeMeta = new Map(this.fx.node.metrics.map((m) => [m.name, m]));
        this.devMeta = new Map(this.fx.devices.map((d) => [d.id, new Map(d.metrics.map((m) => [m.name, m]))]));
        this.values = new Map();
        for (const m of this.fx.node.metrics) this.values.set(this.key(null, m.name), m.value);
        for (const d of this.fx.devices) for (const m of d.metrics) this.values.set(this.key(d.id, m.name), m.value);
        this.onlineDevices = this.fx.devices.map((d) => d.id);
        this.stopping = false;
        this.connected = false;
    }

    key(dev, name) {
        return `${dev || ""}\u0000${name}`;
    }

    // birthMetric: name, alias, datatype and value; dataMetric: the alias only.
    birthMetric(dev, meta) {
        return { name: meta.name, alias: meta.alias, type: meta.datatype, value: this.values.get(this.key(dev, meta.name)), timestamp: ms() };
    }

    dataMetric(dev, meta) {
        return { alias: meta.alias, type: meta.datatype, value: this.values.get(this.key(dev, meta.name)), timestamp: ms() };
    }

    published(type, device, payload) {
        const bd = (payload.metrics || []).find((m) => m.name === "bdSeq");
        const isBd = type === "NBIRTH" || type === "NDEATH";
        emit("published", {
            type, device,
            seq: type === "NDEATH" ? null : payload.seq,
            bdSeq: isBd && bd ? bd.value : null,
        });
    }

    publishNodeBirth() {
        const payload = {
            timestamp: ms(),
            metrics: [{ name: REBIRTH, type: "Boolean", value: false, timestamp: ms() },
                ...[...this.nodeMeta.values()].map((m) => this.birthMetric(null, m))],
        };
        this.client.publishNodeBirth(payload);
        this.published("NBIRTH", null, payload);
    }

    publishDeviceBirth(dev) {
        const payload = { timestamp: ms(), metrics: [...this.devMeta.get(dev).values()].map((m) => this.birthMetric(dev, m)) };
        this.client.publishDeviceBirth(dev, payload);
        this.published("DBIRTH", dev, payload);
    }

    births() {
        this.publishNodeBirth();
        for (const dev of this.onlineDevices) this.publishDeviceBirth(dev);
    }

    publishNodeData(metas) {
        const payload = { timestamp: ms(), metrics: metas.map((m) => this.dataMetric(null, m)) };
        this.client.publishNodeData(payload);
        this.published("NDATA", null, payload);
    }

    publishDeviceData(dev, metas) {
        const payload = { timestamp: ms(), metrics: metas.map((m) => this.dataMetric(dev, m)) };
        this.client.publishDeviceData(dev, payload);
        this.published("DDATA", dev, payload);
    }

    requireConnected() {
        if (!this.connected) throw new CallError("not-connected", "not connected to the broker");
    }

    onCommand(dev, payload) {
        const metas = dev === null ? this.nodeMeta : this.devMeta.get(dev);
        const byAlias = new Map(metas ? [...metas.values()].map((m) => [m.alias, m]) : []);
        const docs = [];
        const changes = [];
        let rebirth = false;
        for (const m of payload.metrics || []) {
            let name = m.name;
            if (name === undefined && m.alias !== undefined) {
                const meta = byAlias.get(fromSpbValue("UInt64", m.alias));
                if (meta) name = meta.name;
            }
            const value = fromSpbValue(m.type, m.value);
            docs.push({ name: name === undefined ? null : name, datatype: m.type, value });
            if (dev === null && name === REBIRTH && m.type === "Boolean" && value === true) {
                rebirth = true;
                continue;
            }
            const meta = metas && name !== undefined ? metas.get(name) : undefined;
            if (meta && meta.writable && m.type === meta.datatype) {
                try {
                    checkValue(name, meta.datatype, value);
                } catch (e) {
                    continue;
                }
                changes.push([meta, value]);
            }
        }
        if (changes.length > 0) {
            for (const [meta, value] of changes) this.values.set(this.key(dev, meta.name), value);
            const metasChanged = changes.map(([meta]) => meta);
            if (dev === null) this.publishNodeData(metasChanged);
            else this.publishDeviceData(dev, metasChanged);
        }
        if (rebirth) this.births();
        emit("command", { device: dev, metrics: docs, applied: changes.length > 0 || rebirth });
    }

    run() {
        return new Promise((resolve) => {
            let readyDone = false;
            const connectTimer = setTimeout(() => {
                diag("no CONNACK within the connect timeout");
                process.exit(EXIT_CONNECT);
            }, this.timeout);
            this.client = sparkplug.newClient({
                serverUrl: `tcp://${this.broker.host.includes(":") ? `[${this.broker.host}]` : this.broker.host}:${this.broker.port}`,
                username: null,
                password: null,
                clientId: this.clientId,
                groupId: this.group,
                edgeNode: this.nodeId,
                publishDeath: true,
                mqttOptions: { reconnectPeriod: 0 },
            });
            this.client.on("error", (e) => {
                if (!this.connected) {
                    diag(`connect: ${e.message}`);
                    process.exit(EXIT_CONNECT);
                }
                diag(`MQTT error: ${e.message}`);
            });
            const lost = (why) => {
                if (this.stopping || !readyDone) return;
                emit("connection-lost", { detail: why });
                process.exit(EXIT_FAILED);
            };
            this.client.on("close", () => lost("MQTT connection closed"));
            this.client.on("offline", () => lost("MQTT client offline"));
            this.client.client.on("close", () => {
                if (!this.connected && !readyDone && !this.stopping) {
                    diag("connection closed before CONNACK");
                }
            });
            this.client.on("birth", () => {
                if (readyDone) {
                    // sparkplug-client asks for births on every connect; with
                    // reconnects disabled this only happens once.
                    return;
                }
                clearTimeout(connectTimer);
                this.connected = true;
                this.births();
                this.control = new Control(this.socketPath, {
                    "publish-data": (p) => this.publishData(p),
                    rebirth: () => this.rebirth(),
                    "device-death": (p) => this.deviceDeath(p),
                    "device-birth": (p) => this.deviceBirth(p),
                });
                fs.mkdirSync(path.dirname(this.readyFile), { recursive: true });
                fs.writeFileSync(this.readyFile, "ready\n");
                readyDone = true;
                emit("ready", { adapter: ADAPTER, fixture: this.fixtureName, group: this.group, edgeNode: this.nodeId, bdSeq: this.client.bdSeq });
            });
            this.client.on("ncmd", (payload) => this.safely(() => this.onCommand(null, payload)));
            this.client.on("dcmd", (dev, payload) => this.safely(() => {
                if (!this.devMeta.has(dev)) {
                    emit("command", { device: dev, metrics: [], applied: false });
                    return;
                }
                this.onCommand(dev, payload);
            }));
            const onTerm = () => {
                if (this.stopping) return;
                this.stopping = true;
                const exit = () => {
                    emit("stopped");
                    process.exit(0);
                };
                const failsafe = setTimeout(exit, 8000);
                failsafe.unref();
                if (!this.connected) {
                    exit();
                    return;
                }
                const death = this.client.getDeathPayload();
                // stop() publishes NDEATH with the client's bdSeq, then ends the
                // MQTT client, which flushes it before it disconnects.
                this.client.client.once("end", () => {
                    this.published("NDEATH", null, death);
                    exit();
                });
                this.client.stop();
            };
            process.on("SIGTERM", onTerm);
            process.on("SIGINT", onTerm);
            void resolve;
        });
    }

    safely(fn) {
        try {
            fn();
        } catch (e) {
            diag(e.stack || String(e));
        }
    }

    publishData(p) {
        this.requireConnected();
        const i = p.update;
        if (!Number.isInteger(i) || i < 0 || i >= this.fx.updates.length) throw new CallError("invalid-request", `no update ${JSON.stringify(i)}`);
        const u = this.fx.updates[i];
        for (const d of this.fx.devices) {
            if (u.devices[d.id] && u.devices[d.id].length > 0 && !this.onlineDevices.includes(d.id)) {
                throw new CallError("invalid-request", `device ${d.id} is offline`);
            }
        }
        const published = [];
        if (u.node.length > 0) {
            for (const c of u.node) this.values.set(this.key(null, c.name), c.value);
            this.publishNodeData(u.node.map((c) => this.nodeMeta.get(c.name)));
            published.push("NDATA");
        }
        for (const d of this.fx.devices) {
            const changes = u.devices[d.id];
            if (!changes || changes.length === 0) continue;
            for (const c of changes) this.values.set(this.key(d.id, c.name), c.value);
            const meta = this.devMeta.get(d.id);
            this.publishDeviceData(d.id, changes.map((c) => meta.get(c.name)));
            published.push(`DDATA/${d.id}`);
        }
        return { published };
    }

    rebirth() {
        this.requireConnected();
        this.births();
        return { published: ["NBIRTH", ...this.onlineDevices.map((d) => `DBIRTH/${d}`)] };
    }

    deviceDeath(p) {
        this.requireConnected();
        const dev = p.device;
        if (!this.onlineDevices.includes(dev)) throw new CallError("invalid-request", `no online device ${JSON.stringify(dev)}`);
        const payload = { timestamp: ms() };
        this.client.publishDeviceDeath(dev, payload);
        this.published("DDEATH", dev, payload);
        this.onlineDevices = this.onlineDevices.filter((d) => d !== dev);
        return { published: [`DDEATH/${dev}`] };
    }

    deviceBirth(p) {
        this.requireConnected();
        const dev = p.device;
        if (typeof dev !== "string" || !this.devMeta.has(dev) || this.onlineDevices.includes(dev)) {
            throw new CallError("invalid-request", `no offline device ${JSON.stringify(dev)}`);
        }
        this.onlineDevices.push(dev);
        // Keep fixture order for later rebirths.
        const order = this.fx.devices.map((d) => d.id);
        this.onlineDevices.sort((a, b) => order.indexOf(a) - order.indexOf(b));
        this.publishDeviceBirth(dev);
        return { published: [`DBIRTH/${dev}`] };
    }
}

// ---- commands ----

function capabilities() {
    return {
        schemaVersion: "1.0", adapter: ADAPTER, adapterVersion: ADAPTER_VERSION, protocol: "sparkplug-b-3.0.0",
        upstream: { name: "sparkplug-client", version: UPSTREAM_VERSION, license: "EPL-2.0", language: "JavaScript", independentEngine: true },
        roles: {
            edge: {
                supported: true, fixtures: ["baseline", "scalars"], calls: EDGE_CALLS,
                features: { aliases: true, rebirthCommand: true, writes: true, primaryHost: false, gracefulDeath: true },
            },
            host: { supported: false },
        },
    };
}

async function main(argv) {
    if (argv.length === 0) argv = ["edge"];
    const [cmd, ...args] = argv;
    try {
        switch (cmd) {
            case "print-capabilities":
                parseFlags(args, {});
                fs.writeSync(1, JSON.stringify(capabilities()) + "\n");
                return 0;
            case "print-fixture": {
                if (args.length > 1) throw new Usage("print-fixture takes at most one name");
                const name = args.length ? args[0] : "baseline";
                const file = path.join(FIXTURES_DIR, name, "fixture.json");
                if (!ID.test(name) || !fs.existsSync(file) || !fs.statSync(file).isFile()) {
                    diag(`no baked fixture ${JSON.stringify(name)}`);
                    return EXIT_FAILED;
                }
                fs.writeSync(1, fs.readFileSync(file));
                return 0;
            }
            case "version":
                parseFlags(args, {});
                fs.writeSync(1, `sparkplugb-interop-${ADAPTER} ${ADAPTER_VERSION} (sparkplug-client ${UPSTREAM_VERSION})\n`);
                return 0;
            case "call":
                return await call(args);
            case "edge": {
                const flags = parseFlags(args, {
                    broker: "url", "client-id": "str", "connect-timeout-ms": "int", "ready-file": "str", "control-socket": "str",
                    fixture: "str", group: "id", "edge-node": "id", "primary-host": "id",
                }, ["broker"]);
                if (Object.prototype.hasOwnProperty.call(flags, "primary-host")) {
                    diag("--primary-host is not supported: sparkplug-client has no STATE awareness");
                    return EXIT_UNSUPPORTED;
                }
                await new Edge(flags).run();
                return 0;
            }
            case "host":
                diag("the host role is not supported: sparkplug-client is an Edge Node client only");
                return EXIT_UNSUPPORTED;
            default:
                throw new Usage(`unknown command ${cmd}`);
        }
    } catch (e) {
        if (e instanceof Usage) {
            diag(`usage: ${e.message}`);
            return EXIT_USAGE;
        }
        if (e instanceof FixtureError) {
            diag(`fixture: ${e.message}`);
            return EXIT_FIXTURE;
        }
        throw e;
    }
}

main(process.argv.slice(2)).then((code) => process.exit(code), (e) => {
    diag(e.stack || String(e));
    process.exit(EXIT_FAILED);
});
