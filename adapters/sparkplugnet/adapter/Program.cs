// SPDX-License-Identifier: MIT
// sparkplugb-interop adapter for SparkplugNet (docs/CONTAINER_CONTRACT.md).
//
// The edge role is SparkplugNet's SparkplugNode, the host role its
// SparkplugApplication (Sparkplug B, specification 3.0). The adapter wraps the
// MQTT client the library creates to observe what goes over the wire (the seq
// and bdSeq of each message, which the library does not expose), the same way
// the pysparkplug adapter wraps its client's publish. Everything Sparkplug is
// the library's: births, deaths, wills, STATE, encoding and decoding.

using System.Buffers;
using System.Net.Sockets;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.RegularExpressions;
using MQTTnet;
using MQTTnet.Diagnostics.PacketInspection;
using SparkplugNet.Core;
using SparkplugNet.Core.Application;
using SparkplugNet.Core.Enumerations;
using SparkplugNet.Core.Node;
using SparkplugNet.VersionB;
using SparkplugNet.VersionB.Data;

namespace SparkplugbInterop;

internal sealed class Usage(string message) : Exception(message);

internal sealed class FixtureError(string message) : Exception(message);

internal sealed class CallError(string code, string message) : Exception(message)
{
    public string Code { get; } = code;
}

internal static class Out
{
    private static readonly object Lock = new();
    private static readonly Stream Stdout = Console.OpenStandardOutput();

    public static readonly JsonSerializerOptions Json = new() { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };

    public static string Now() => DateTime.UtcNow.ToString("yyyy-MM-dd'T'HH:mm:ss.fff'Z'");

    public static void Line(JsonNode doc)
    {
        var bytes = Encoding.UTF8.GetBytes(doc.ToJsonString(Json) + "\n");
        lock (Lock)
        {
            Stdout.Write(bytes);
            Stdout.Flush();
        }
    }

    public static void Emit(string ev, params (string Key, JsonNode? Value)[] fields)
    {
        var doc = new JsonObject { ["event"] = ev, ["time"] = Now() };
        foreach (var (k, v) in fields)
        {
            doc[k] = v;
        }

        Line(doc);
    }

    public static void Diag(string msg) => Console.Error.WriteLine($"[{Program.Adapter}] {msg}");
}

// ---- Sparkplug datatypes, values and fixtures ----

internal static class Values
{
    public static readonly string[] DataTypes =
        ["Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float", "Double", "Boolean", "String", "DateTime", "Text"];

    private const long MaxSafe = (1L << 53) - 1;

    private static readonly Dictionary<string, (long Lo, long Hi)> Ranges = new()
    {
        ["Int8"] = (sbyte.MinValue, sbyte.MaxValue), ["Int16"] = (short.MinValue, short.MaxValue),
        ["Int32"] = (int.MinValue, int.MaxValue), ["Int64"] = (-MaxSafe, MaxSafe),
        ["UInt8"] = (0, byte.MaxValue), ["UInt16"] = (0, ushort.MaxValue), ["UInt32"] = (0, uint.MaxValue),
        ["UInt64"] = (0, MaxSafe), ["DateTime"] = (0, MaxSafe),
    };

    public static DataType ToSpb(string dt) => Enum.Parse<DataType>(dt);

    public static string FromSpb(DataType dt) => dt.ToString();

    public static void Check(string where, string dt, JsonElement v)
    {
        if (Ranges.TryGetValue(dt, out var range))
        {
            if (v.ValueKind != JsonValueKind.Number || !v.TryGetInt64(out var i) || i < range.Lo || i > range.Hi)
            {
                throw new FixtureError($"{where}: {v.GetRawText()} is not a {dt}");
            }
        }
        else if (dt is "Float" or "Double")
        {
            if (v.ValueKind != JsonValueKind.Number)
            {
                throw new FixtureError($"{where}: {v.GetRawText()} is not a number");
            }

            var d = v.GetDouble();
            if (dt == "Float" && (double)(float)d != d)
            {
                throw new FixtureError($"{where}: {v.GetRawText()} is not exactly a Float");
            }
        }
        else if (dt == "Boolean")
        {
            if (v.ValueKind is not (JsonValueKind.True or JsonValueKind.False))
            {
                throw new FixtureError($"{where}: {v.GetRawText()} is not a boolean");
            }
        }
        else if (dt is "String" or "Text")
        {
            if (v.ValueKind != JsonValueKind.String)
            {
                throw new FixtureError($"{where}: {v.GetRawText()} is not a string");
            }
        }
        else
        {
            throw new FixtureError($"{where}: unknown datatype {dt}");
        }
    }

    // ToClr converts a checked JSON value to the CLR type SparkplugNet expects for dt.
    public static object? ToClr(string dt, JsonElement v) => v.ValueKind == JsonValueKind.Null ? null : dt switch
    {
        "Int8" => (sbyte)v.GetInt64(),
        "Int16" => (short)v.GetInt64(),
        "Int32" => (int)v.GetInt64(),
        "Int64" => v.GetInt64(),
        "UInt8" => (byte)v.GetInt64(),
        "UInt16" => (ushort)v.GetInt64(),
        "UInt32" => (uint)v.GetInt64(),
        "UInt64" => (ulong)v.GetInt64(),
        "DateTime" => (ulong)v.GetInt64(),
        "Float" => (float)v.GetDouble(),
        "Double" => v.GetDouble(),
        "Boolean" => v.GetBoolean(),
        _ => v.GetString(),
    };

    public static JsonNode? ToJson(Metric m)
    {
        if (m.IsNull || m.Value is null)
        {
            return null;
        }

        var v = m.Value;
        return m.DataType switch
        {
            DataType.Int8 or DataType.Int16 or DataType.Int32 or DataType.Int64
                or DataType.UInt8 or DataType.UInt16 or DataType.UInt32 => JsonValue.Create(Convert.ToInt64(v)),
            DataType.UInt64 => JsonValue.Create(Convert.ToUInt64(v)),
            DataType.DateTime => JsonValue.Create(v is DateTimeOffset dto ? dto.ToUnixTimeMilliseconds() : Convert.ToInt64(v)),
            DataType.Float => JsonValue.Create((double)Convert.ToSingle(v)),
            DataType.Double => JsonValue.Create(Convert.ToDouble(v)),
            DataType.Boolean => JsonValue.Create(Convert.ToBoolean(v)),
            DataType.String or DataType.Text => JsonValue.Create(Convert.ToString(v)),
            _ => JsonValue.Create(Convert.ToString(v)),
        };
    }

    public static JsonObject Doc(Metric m) => new()
    {
        ["name"] = m.Name,
        ["datatype"] = FromSpb(m.DataType),
        ["value"] = ToJson(m),
    };

    public static Metric Make(string name, string dt, JsonElement value, ulong? alias = null) =>
        new(name, ToSpb(dt), ToClr(dt, value)) { Alias = alias };
}

internal sealed record FixtureMetric(string Name, ulong Alias, string DataType, JsonElement Value, bool Writable);

internal sealed record FixtureDevice(string Id, List<FixtureMetric> Metrics);

internal sealed record Change(string Name, JsonElement Value);

internal sealed record Update(List<Change> Node, Dictionary<string, List<Change>> Devices);

internal sealed class Fixture
{
    public static readonly Regex Id = new("^[^/+#]+$");

    public required string Name { get; init; }
    public required string Group { get; init; }
    public required string EdgeNode { get; init; }
    public required List<FixtureMetric> Node { get; init; }
    public required List<FixtureDevice> Devices { get; init; }
    public required List<Update> Updates { get; init; }

    public static string PathOf(string nameOrPath) =>
        nameOrPath.Contains('/') || nameOrPath.EndsWith(".json", StringComparison.Ordinal)
            ? nameOrPath
            : Path.Combine(Program.FixturesDir, nameOrPath, "fixture.json");

    private static JsonElement Get(JsonElement o, string key, JsonValueKind kind)
    {
        if (o.ValueKind != JsonValueKind.Object || !o.TryGetProperty(key, out var v))
        {
            throw new FixtureError($"malformed fixture: missing {key}");
        }

        if (kind != JsonValueKind.Undefined && v.ValueKind != kind)
        {
            throw new FixtureError($"malformed fixture: {key} is not {kind}");
        }

        return v;
    }

    public static Fixture Load(string nameOrPath)
    {
        var path = PathOf(nameOrPath);
        JsonElement root;
        try
        {
            using var doc = JsonDocument.Parse(File.ReadAllBytes(path));
            root = doc.RootElement.Clone();
        }
        catch (Exception e) when (e is IOException or JsonException or UnauthorizedAccessException)
        {
            throw new FixtureError($"{path}: {e.Message}");
        }

        if (root.ValueKind != JsonValueKind.Object || !root.TryGetProperty("schemaVersion", out var sv)
            || sv.ValueKind != JsonValueKind.String || sv.GetString() != "1.0")
        {
            throw new FixtureError("schemaVersion is not 1.0");
        }

        string IdOf(string key)
        {
            var v = Get(root, key, JsonValueKind.String).GetString()!;
            return Id.IsMatch(v) ? v : throw new FixtureError($"{key} is not a valid Sparkplug ID");
        }

        var group = IdOf("group");
        var edgeNode = IdOf("edgeNode");
        var aliases = new HashSet<ulong>();
        var types = new Dictionary<(string Scope, string Name), string>();

        List<FixtureMetric> Metrics(string scope, JsonElement arr)
        {
            if (arr.ValueKind != JsonValueKind.Array || arr.GetArrayLength() == 0)
            {
                throw new FixtureError($"{scope} has no metrics");
            }

            var names = new HashSet<string>();
            var list = new List<FixtureMetric>();
            foreach (var m in arr.EnumerateArray())
            {
                var name = Get(m, "name", JsonValueKind.String).GetString()!;
                var where = $"{scope}, metric '{name}'";
                if (!names.Add(name) || name.StartsWith("Node Control/", StringComparison.Ordinal) || name == "bdSeq")
                {
                    throw new FixtureError($"{where}: duplicate or reserved name");
                }

                var a = Get(m, "alias", JsonValueKind.Number);
                if (!a.TryGetInt64(out var alias) || alias < 1 || !aliases.Add((ulong)alias))
                {
                    throw new FixtureError($"{where}: invalid or duplicate alias");
                }

                var dt = Get(m, "datatype", JsonValueKind.String).GetString()!;
                var value = Get(m, "value", JsonValueKind.Undefined);
                Values.Check(where, dt, value);
                var writable = m.TryGetProperty("writable", out var w) && w.ValueKind == JsonValueKind.True;
                types[(scope, name)] = dt;
                list.Add(new FixtureMetric(name, (ulong)alias, dt, value, writable));
            }

            return list;
        }

        var node = Metrics("node", Get(Get(root, "node", JsonValueKind.Object), "metrics", JsonValueKind.Array));
        var devices = new List<FixtureDevice>();
        foreach (var d in Get(root, "devices", JsonValueKind.Array).EnumerateArray())
        {
            var id = Get(d, "id", JsonValueKind.String).GetString()!;
            if (devices.Any(x => x.Id == id))
            {
                throw new FixtureError("a device appears twice");
            }

            devices.Add(new FixtureDevice(id, Metrics($"device {id}", Get(d, "metrics", JsonValueKind.Array))));
        }

        var updates = new List<Update>();
        var i = 0;
        foreach (var u in Get(root, "updates", JsonValueKind.Array).EnumerateArray())
        {
            List<Change> Changes(string scope, JsonElement arr)
            {
                if (arr.ValueKind != JsonValueKind.Array)
                {
                    throw new FixtureError($"updates[{i}]: malformed changes");
                }

                var list = new List<Change>();
                foreach (var c in arr.EnumerateArray())
                {
                    var name = Get(c, "name", JsonValueKind.String).GetString()!;
                    if (!types.TryGetValue((scope, name), out var dt))
                    {
                        throw new FixtureError($"updates[{i}]: {scope} has no metric '{name}'");
                    }

                    var value = Get(c, "value", JsonValueKind.Undefined);
                    Values.Check($"updates[{i}], {scope}, metric '{name}'", dt, value);
                    list.Add(new Change(name, value));
                }

                return list;
            }

            var nodeChanges = Changes("node", Get(u, "node", JsonValueKind.Array));
            var devChanges = new Dictionary<string, List<Change>>();
            foreach (var p in Get(u, "devices", JsonValueKind.Object).EnumerateObject())
            {
                if (devices.All(d => d.Id != p.Name))
                {
                    throw new FixtureError($"updates[{i}]: unknown device '{p.Name}'");
                }

                devChanges[p.Name] = Changes($"device {p.Name}", p.Value);
            }

            if (nodeChanges.Count == 0 && devChanges.Values.All(c => c.Count == 0))
            {
                throw new FixtureError($"updates[{i}] changes nothing");
            }

            updates.Add(new Update(nodeChanges, devChanges));
            i++;
        }

        return new Fixture
        {
            Name = Get(root, "name", JsonValueKind.String).GetString()!,
            Group = group,
            EdgeNode = edgeNode,
            Node = node,
            Devices = devices,
            Updates = updates,
        };
    }
}

// ---- flags ----

internal static class Flags
{
    // Parse: spec maps a flag to its kind ("str", "int", "url", "id", "json"); any misuse is a Usage error.
    public static Dictionary<string, object> Parse(string[] args, Dictionary<string, string> spec, params string[] required)
    {
        var flags = new Dictionary<string, object>();
        for (var i = 0; i < args.Length; i += 2)
        {
            var a = args[i];
            if (!a.StartsWith("--", StringComparison.Ordinal) || !spec.TryGetValue(a[2..], out var kind))
            {
                throw new Usage($"unknown argument {a}");
            }

            var name = a[2..];
            if (flags.ContainsKey(name))
            {
                throw new Usage($"--{name} given twice");
            }

            if (i + 1 >= args.Length)
            {
                throw new Usage($"--{name} needs a value");
            }

            var v = args[i + 1];
            flags[name] = kind switch
            {
                "int" => Regex.IsMatch(v, "^[0-9]{1,9}$") && int.Parse(v) > 0 ? int.Parse(v) : throw new Usage($"--{name}: '{v}' is not a positive integer"),
                "url" => ParseUrl(name, v),
                "id" => Fixture.Id.IsMatch(v) ? v : throw new Usage($"--{name}: '{v}' is not a valid Sparkplug ID"),
                "json" => ParseJson(name, v),
                _ => v,
            };
        }

        foreach (var r in required)
        {
            if (!flags.ContainsKey(r))
            {
                throw new Usage($"--{r} is required");
            }
        }

        return flags;
    }

    private static (string Host, int Port) ParseUrl(string name, string v)
    {
        var m = Regex.Match(v, @"^tcp://([^:/?#@\[\]]+)(?::([0-9]{1,5}))?/?$");
        if (!m.Success)
        {
            throw new Usage($"--{name}: '{v}' is not tcp://host[:port]");
        }

        var port = m.Groups[2].Success ? int.Parse(m.Groups[2].Value) : 1883;
        if (port is < 1 or > 65535)
        {
            throw new Usage($"--{name}: port {port} is out of range");
        }

        return (m.Groups[1].Value, port);
    }

    private static JsonElement ParseJson(string name, string v)
    {
        try
        {
            using var doc = JsonDocument.Parse(v);
            return doc.RootElement.ValueKind == JsonValueKind.Object
                ? doc.RootElement.Clone()
                : throw new Usage($"--{name} must be a JSON object");
        }
        catch (JsonException e)
        {
            throw new Usage($"--{name}: {e.Message}");
        }
    }
}

// ---- what goes over the wire ----

// Wire reads the few Sparkplug B payload fields the contract reports and the
// library does not expose: the payload seq and the bdSeq metric.
internal static class Wire
{
    public sealed record Message(string Type, string Group, string EdgeNode, string? Device, ulong? Seq, long? BdSeq);

    public static Message? Parse(string topic, ReadOnlySpan<byte> payload)
    {
        var parts = topic.Split('/');
        if (parts.Length is < 4 or > 5 || parts[0] != "spBv1.0" || parts[2] is "STATE")
        {
            return null;
        }

        ulong? seq = null;
        long? bdSeq = null;
        try
        {
            var r = new Reader(payload);
            while (!r.Done)
            {
                var (field, wire) = r.Tag();
                if (field == 3 && wire == 0)
                {
                    seq = r.Varint();
                }
                else if (field == 2 && wire == 2)
                {
                    var m = new Reader(r.Bytes());
                    string? name = null;
                    ulong? longValue = null;
                    while (!m.Done)
                    {
                        var (mf, mw) = m.Tag();
                        if (mf == 1 && mw == 2)
                        {
                            name = Encoding.UTF8.GetString(m.Bytes());
                        }
                        else if (mf == 11 && mw == 0)
                        {
                            longValue = m.Varint();
                        }
                        else
                        {
                            m.Skip(mw);
                        }
                    }

                    if (name == "bdSeq" && longValue is not null)
                    {
                        bdSeq = (long)longValue.Value;
                    }
                }
                else
                {
                    r.Skip(wire);
                }
            }
        }
        catch (Exception e) when (e is IndexOutOfRangeException or ArgumentOutOfRangeException or InvalidDataException)
        {
            Out.Diag($"cannot read the payload on {topic}: {e.Message}");
        }

        return new Message(parts[2], parts[1], parts[3], parts.Length == 5 ? parts[4] : null, seq, bdSeq);
    }

    private ref struct Reader(ReadOnlySpan<byte> data)
    {
        private readonly ReadOnlySpan<byte> data = data;
        private int pos;

        public readonly bool Done => this.pos >= this.data.Length;

        public ulong Varint()
        {
            ulong v = 0;
            for (var shift = 0; shift < 64; shift += 7)
            {
                var b = this.data[this.pos++];
                v |= (ulong)(b & 0x7f) << shift;
                if ((b & 0x80) == 0)
                {
                    return v;
                }
            }

            throw new InvalidDataException("varint too long");
        }

        public (int Field, int Wire) Tag()
        {
            var t = this.Varint();
            return ((int)(t >> 3), (int)(t & 7));
        }

        public ReadOnlySpan<byte> Bytes()
        {
            var n = (int)this.Varint();
            var s = this.data.Slice(this.pos, n);
            this.pos += n;
            return s;
        }

        public void Skip(int wire)
        {
            switch (wire)
            {
                case 0: this.Varint(); break;
                case 1: this.pos += 8; break;
                case 2: this.Bytes(); break;
                case 5: this.pos += 4; break;
                default: throw new InvalidDataException($"wire type {wire}");
            }
        }
    }
}

// ObservedClient is the MQTT client of the library, with a look at every
// message it publishes (after the broker took it) and receives (before the
// library handles it).
internal sealed class ObservedClient(IMqttClient inner) : IMqttClient
{
    private readonly List<Func<MqttApplicationMessageReceivedEventArgs, Task>> handlers = [];
    private bool hooked;

    public Action<MqttApplicationMessage>? Published { get; set; }
    public Func<MqttApplicationMessageReceivedEventArgs, Func<Task>, Task>? Receiving { get; set; }

    public bool IsConnected => inner.IsConnected;
    public MqttClientOptions Options => inner.Options;

    public event Func<MqttApplicationMessageReceivedEventArgs, Task> ApplicationMessageReceivedAsync
    {
        add
        {
            lock (this.handlers)
            {
                this.handlers.Add(value);
                if (!this.hooked)
                {
                    inner.ApplicationMessageReceivedAsync += this.Dispatch;
                    this.hooked = true;
                }
            }
        }

        remove
        {
            lock (this.handlers)
            {
                this.handlers.Remove(value);
            }
        }
    }

    public event Func<MqttClientConnectedEventArgs, Task> ConnectedAsync { add => inner.ConnectedAsync += value; remove => inner.ConnectedAsync -= value; }
    public event Func<MqttClientConnectingEventArgs, Task> ConnectingAsync { add => inner.ConnectingAsync += value; remove => inner.ConnectingAsync -= value; }
    public event Func<MqttClientDisconnectedEventArgs, Task> DisconnectedAsync { add => inner.DisconnectedAsync += value; remove => inner.DisconnectedAsync -= value; }
    public event Func<InspectMqttPacketEventArgs, Task> InspectPacketAsync { add => inner.InspectPacketAsync += value; remove => inner.InspectPacketAsync -= value; }

    public IMqttClient Inner => inner;

    private async Task Dispatch(MqttApplicationMessageReceivedEventArgs args)
    {
        Func<MqttApplicationMessageReceivedEventArgs, Task>[] hs;
        lock (this.handlers)
        {
            hs = [.. this.handlers];
        }

        async Task Library()
        {
            foreach (var h in hs)
            {
                await h(args);
            }
        }

        try
        {
            if (this.Receiving is { } receiving)
            {
                await receiving(args, Library);
            }
            else
            {
                await Library();
            }
        }
        catch (Exception e)
        {
            Out.Diag($"handling a message on {args.ApplicationMessage.Topic}: {e}");
        }
    }

    public Task<MqttClientConnectResult> ConnectAsync(MqttClientOptions options, CancellationToken cancellationToken = default) => inner.ConnectAsync(options, cancellationToken);

    public Task DisconnectAsync(MqttClientDisconnectOptions options, CancellationToken cancellationToken = default) => inner.DisconnectAsync(options, cancellationToken);

    public Task PingAsync(CancellationToken cancellationToken = default) => inner.PingAsync(cancellationToken);

    public async Task<MqttClientPublishResult> PublishAsync(MqttApplicationMessage applicationMessage, CancellationToken cancellationToken = default)
    {
        var result = await inner.PublishAsync(applicationMessage, cancellationToken);
        this.Published?.Invoke(applicationMessage);
        return result;
    }

    public Task SendEnhancedAuthenticationExchangeDataAsync(MqttEnhancedAuthenticationExchangeData data, CancellationToken cancellationToken = default) => inner.SendEnhancedAuthenticationExchangeDataAsync(data, cancellationToken);

    public Task<MqttClientSubscribeResult> SubscribeAsync(MqttClientSubscribeOptions options, CancellationToken cancellationToken = default) => inner.SubscribeAsync(options, cancellationToken);

    public Task<MqttClientUnsubscribeResult> UnsubscribeAsync(MqttClientUnsubscribeOptions options, CancellationToken cancellationToken = default) => inner.UnsubscribeAsync(options, cancellationToken);

    public void Dispose() => inner.Dispose();

    // Observe replaces the client the library created in its constructor.
    public static ObservedClient Observe(SparkplugBase<Metric> sparkplug)
    {
        var field = typeof(SparkplugBase<Metric>).GetField("client", BindingFlags.Instance | BindingFlags.NonPublic)
            ?? throw new InvalidOperationException("SparkplugNet has no MQTT client field");
        var observed = new ObservedClient((IMqttClient)field.GetValue(sparkplug)!);
        field.SetValue(sparkplug, observed);
        return observed;
    }
}

// PassThrough keeps every metric. SparkplugNet filters the metrics it receives
// and sends against a list known in advance; a host learns them from births.
internal sealed class PassThrough() : SparkplugBase<Metric>.KnownMetricStorage([])
{
    public override IEnumerable<Metric> FilterMetrics(IEnumerable<Metric> metrics, SparkplugMessageType sparkplugMessageType) => metrics;
}

// ---- control socket ----

internal sealed class Control
{
    private readonly Dictionary<string, Func<JsonElement, Task<JsonNode?>>> handlers;
    private readonly SemaphoreSlim gate;

    public Control(string path, SemaphoreSlim gate, Dictionary<string, Func<JsonElement, Task<JsonNode?>>> handlers)
    {
        this.handlers = handlers;
        this.gate = gate;
        File.Delete(path);
        var listener = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
        listener.Bind(new UnixDomainSocketEndPoint(path));
        listener.Listen(16);
        _ = Task.Run(async () =>
        {
            while (true)
            {
                var conn = await listener.AcceptAsync();
                _ = Task.Run(() => this.Serve(conn));
            }
        });
    }

    private async Task Serve(Socket conn)
    {
        using (conn)
        {
            await using var stream = new NetworkStream(conn);
            using var reader = new StreamReader(stream, Encoding.UTF8);
            var line = await reader.ReadLineAsync() ?? string.Empty;
            var doc = await this.Handle(line);
            await stream.WriteAsync(Encoding.UTF8.GetBytes(doc.ToJsonString(Out.Json) + "\n"));
        }
    }

    private async Task<JsonObject> Handle(string line)
    {
        var start = Environment.TickCount64;
        string? op = null;
        try
        {
            using var req = JsonDocument.Parse(line);
            op = req.RootElement.TryGetProperty("operation", out var o) && o.ValueKind == JsonValueKind.String ? o.GetString() : null;
            var payload = req.RootElement.TryGetProperty("payload", out var p) ? p.Clone() : JsonDocument.Parse("{}").RootElement;
            if (op is null || !this.handlers.TryGetValue(op, out var handler))
            {
                throw new CallError("unsupported", $"{op} is not offered by this role");
            }

            if (payload.ValueKind != JsonValueKind.Object)
            {
                throw new CallError("invalid-request", "payload is not an object");
            }

            await this.gate.WaitAsync();
            try
            {
                return Program.Result(op, true, null, await handler(payload), start);
            }
            finally
            {
                this.gate.Release();
            }
        }
        catch (CallError e)
        {
            return Program.Result(op, false, new JsonObject { ["code"] = e.Code, ["detail"] = e.Message }, null, start);
        }
        catch (Exception e)
        {
            Out.Diag(e.ToString());
            return Program.Result(op, false, new JsonObject { ["code"] = "invalid-request", ["detail"] = e.Message }, null, start);
        }
    }
}

// ---- shared role plumbing ----

internal abstract class Role
{
    protected readonly string ReadyFile;
    protected readonly string SocketPath;
    protected readonly int TimeoutMs;
    protected readonly SemaphoreSlim Gate = new(1, 1);
    protected volatile bool Stopping;

    protected Role(Dictionary<string, object> flags)
    {
        this.ReadyFile = flags.TryGetValue("ready-file", out var r) ? (string)r : Program.DefaultReady;
        this.SocketPath = flags.TryGetValue("control-socket", out var s) ? (string)s : Program.DefaultSocket;
        this.TimeoutMs = flags.TryGetValue("connect-timeout-ms", out var t) ? (int)t : 10000;
    }

    protected void WatchConnection(ObservedClient client)
    {
        client.Inner.DisconnectedAsync += args =>
        {
            if (args.ClientWasConnected && !this.Stopping)
            {
                Out.Emit("connection-lost", ("detail", $"MQTT disconnect: {args.Reason} {args.Exception?.Message}".Trim()));
                Environment.Exit(Program.ExitFailed);
            }

            return Task.CompletedTask;
        };
    }

    // Connect runs the library's Start within the connect timeout; exit 3 when the broker cannot be reached.
    protected async Task Connect(Func<Task> start)
    {
        var task = start();
        if (await Task.WhenAny(task, Task.Delay(this.TimeoutMs)) != task)
        {
            Out.Diag("no connection within the connect timeout");
            Environment.Exit(Program.ExitConnect);
        }

        try
        {
            await task;
        }
        catch (Exception e)
        {
            Out.Diag($"connect: {e.Message}");
            Environment.Exit(Program.ExitConnect);
        }
    }

    protected void MarkReady()
    {
        var dir = Path.GetDirectoryName(this.ReadyFile);
        if (!string.IsNullOrEmpty(dir))
        {
            Directory.CreateDirectory(dir);
        }

        File.WriteAllText(this.ReadyFile, "ready\n");
    }

    protected async Task RunUntilTerm(Func<Task> stop)
    {
        var done = new TaskCompletionSource();
        using var term = PosixSignalRegistration.Create(PosixSignal.SIGTERM, ctx => { ctx.Cancel = true; done.TrySetResult(); });
        using var intr = PosixSignalRegistration.Create(PosixSignal.SIGINT, ctx => { ctx.Cancel = true; done.TrySetResult(); });
        await done.Task;
        this.Stopping = true;
        try
        {
            var task = stop();
            await Task.WhenAny(task, Task.Delay(8000));
        }
        catch (Exception e)
        {
            Out.Diag($"stop: {e.Message}");
        }
        finally
        {
            Out.Emit("stopped");
            Environment.Exit(0);
        }
    }

    public abstract Task Run();
}

// ---- edge ----

internal sealed class Edge : Role
{
    private const string Rebirth = "Node Control/Rebirth";

    private readonly Fixture fx;
    private readonly string group;
    private readonly string nodeId;
    private readonly (string Host, int Port) broker;
    private readonly string clientId;
    private readonly Dictionary<string, (FixtureMetric Meta, Metric Known)> nodeMetrics = [];
    private readonly Dictionary<string, Dictionary<string, (FixtureMetric Meta, Metric Known)>> deviceMetrics = [];
    private readonly List<string> online = [];
    private SparkplugNode node = null!;
    private long? bdSeq;

    public Edge(Dictionary<string, object> flags) : base(flags)
    {
        this.fx = Fixture.Load(flags.TryGetValue("fixture", out var f) ? (string)f : "baseline");
        this.group = flags.TryGetValue("group", out var g) ? (string)g : this.fx.Group;
        this.nodeId = flags.TryGetValue("edge-node", out var n) ? (string)n : this.fx.EdgeNode;
        this.broker = ((string, int))flags["broker"];
        this.clientId = flags.TryGetValue("client-id", out var c) ? (string)c : $"sparkplugb-interop-{Program.Adapter}-edge-{this.group}-{this.nodeId}";
        foreach (var m in this.fx.Node)
        {
            this.nodeMetrics[m.Name] = (m, Values.Make(m.Name, m.DataType, m.Value, m.Alias));
        }

        foreach (var d in this.fx.Devices)
        {
            this.deviceMetrics[d.Id] = d.Metrics.ToDictionary(m => m.Name, m => (m, Values.Make(m.Name, m.DataType, m.Value, m.Alias)));
            this.online.Add(d.Id);
        }
    }

    private void OnPublished(MqttApplicationMessage message)
    {
        var w = Wire.Parse(message.Topic, message.Payload.ToArray());
        if (w is null)
        {
            return;
        }

        var birthOrDeath = w.Type is "NBIRTH" or "NDEATH";
        if (w.Type == "NBIRTH")
        {
            this.bdSeq = w.BdSeq;
        }

        Out.Emit("published", ("type", w.Type), ("device", w.Device), ("seq", w.Type == "NDEATH" || w.Seq is null ? null : JsonValue.Create(w.Seq.Value)),
            ("bdSeq", birthOrDeath && w.BdSeq is not null ? JsonValue.Create(w.BdSeq.Value) : null));
    }

    private async Task OnCommand(string? device, IEnumerable<Metric> metrics)
    {
        var list = metrics.ToList();
        var applied = false;
        await this.Gate.WaitAsync();
        try
        {
            var scope = device is null ? this.nodeMetrics : this.deviceMetrics.GetValueOrDefault(device);
            var changes = new List<(FixtureMetric Meta, Metric Known, Metric Value)>();
            foreach (var m in list)
            {
                if (scope is not null && scope.TryGetValue(m.Name, out var e) && e.Meta.Writable && Values.FromSpb(m.DataType) == e.Meta.DataType)
                {
                    changes.Add((e.Meta, e.Known, m));
                }
            }

            if (changes.Count > 0)
            {
                var data = new List<Metric>();
                foreach (var (meta, known, value) in changes)
                {
                    known.SetValue(known.DataType, value.Value);
                    data.Add(new Metric(meta.Name, known.DataType, value.Value, DateTimeOffset.UtcNow));
                }

                if (device is null)
                {
                    await this.node.PublishMetrics(data);
                }
                else
                {
                    await this.node.PublishDeviceData(data, device);
                }

                applied = true;
            }
        }
        catch (Exception e)
        {
            Out.Diag($"command: {e}");
        }
        finally
        {
            this.Gate.Release();
            Out.Emit("command", ("device", device), ("metrics", new JsonArray([.. list.Select(m => (JsonNode)Values.Doc(m))])), ("applied", applied));
        }
    }

    public override async Task Run()
    {
        var known = new List<Metric> { new(Rebirth, DataType.Boolean, false) };
        known.AddRange(this.nodeMetrics.Values.Select(e => e.Known));
        this.node = new SparkplugNode(known, SparkplugSpecificationVersion.Version30);
        var client = ObservedClient.Observe(this.node);
        client.Published = this.OnPublished;
        this.WatchConnection(client);
        this.node.NodeCommandReceived += args => this.OnCommand(null, args.Metrics);
        this.node.DeviceCommandReceived += args => this.OnCommand(args.DeviceIdentifier, args.Metrics);
        var options = new SparkplugNodeOptions(
            brokerAddress: this.broker.Host, port: this.broker.Port, clientId: this.clientId,
            groupIdentifier: this.group, edgeNodeIdentifier: this.nodeId)
        {
            PublishKnownDeviceMetricsOnReconnect = true,
            // SparkplugNet defaults to "" for both and MQTTnet then sends a
            // password without a user name, which MQTT 3.1.1 forbids
            // (MQTT-3.1.2-22) and NanoMQ refuses: send no credentials.
            UserName = null!,
            Password = null!,
        };

        await this.Gate.WaitAsync();
        await this.Connect(async () =>
        {
            await this.node.Start(options);
            foreach (var d in this.fx.Devices)
            {
                await this.node.PublishDeviceBirthMessage([.. this.deviceMetrics[d.Id].Values.Select(e => e.Known)], d.Id);
            }
        });
        _ = new Control(this.SocketPath, this.Gate, new()
        {
            ["publish-data"] = this.PublishData,
            ["device-death"] = this.DeviceDeath,
            ["device-birth"] = this.DeviceBirth,
        });
        this.MarkReady();
        Out.Emit("ready", ("adapter", Program.Adapter), ("fixture", this.fx.Name), ("group", this.group), ("edgeNode", this.nodeId),
            ("bdSeq", this.bdSeq is null ? null : JsonValue.Create(this.bdSeq.Value)));
        this.Gate.Release();
        await this.RunUntilTerm(async () =>
        {
            await this.Gate.WaitAsync();
            await this.node.Stop();
        });
    }

    private static int Index(JsonElement p, string key, int count)
    {
        if (!p.TryGetProperty(key, out var v) || v.ValueKind != JsonValueKind.Number || !v.TryGetInt32(out var i) || i < 0 || i >= count)
        {
            throw new CallError("invalid-request", $"no update {(p.TryGetProperty(key, out var x) ? x.GetRawText() : "given")}");
        }

        return i;
    }

    private static string? Str(JsonElement p, string key) =>
        p.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;

    private async Task<JsonNode?> PublishData(JsonElement p)
    {
        var u = this.fx.Updates[Index(p, "update", this.fx.Updates.Count)];
        var published = new JsonArray();
        foreach (var d in this.fx.Devices)
        {
            if (u.Devices.TryGetValue(d.Id, out var c) && c.Count > 0 && !this.online.Contains(d.Id))
            {
                throw new CallError("invalid-request", $"device {d.Id} is offline");
            }
        }

        if (u.Node.Count > 0)
        {
            await this.node.PublishMetrics(Apply(this.nodeMetrics, u.Node));
            published.Add("NDATA");
        }

        foreach (var d in this.fx.Devices)
        {
            if (u.Devices.TryGetValue(d.Id, out var changes) && changes.Count > 0)
            {
                await this.node.PublishDeviceData(Apply(this.deviceMetrics[d.Id], changes), d.Id);
                published.Add($"DDATA/{d.Id}");
            }
        }

        return new JsonObject { ["published"] = published };
    }

    // Apply sets the current values and returns the data metrics, by name: SparkplugNet
    // drops a data metric that carries both a name and an alias.
    private static List<Metric> Apply(Dictionary<string, (FixtureMetric Meta, Metric Known)> scope, List<Change> changes)
    {
        var data = new List<Metric>();
        foreach (var c in changes)
        {
            var (meta, known) = scope[c.Name];
            known.SetValue(known.DataType, Values.ToClr(meta.DataType, c.Value));
            data.Add(new Metric(meta.Name, known.DataType, Values.ToClr(meta.DataType, c.Value), DateTimeOffset.UtcNow));
        }

        return data;
    }

    private async Task<JsonNode?> DeviceDeath(JsonElement p)
    {
        var dev = Str(p, "device");
        if (dev is null || !this.online.Contains(dev))
        {
            throw new CallError("invalid-request", $"no online device {dev}");
        }

        await this.node.PublishDeviceDeathMessage(dev);
        this.online.Remove(dev);
        return new JsonObject { ["published"] = new JsonArray($"DDEATH/{dev}") };
    }

    private async Task<JsonNode?> DeviceBirth(JsonElement p)
    {
        var dev = Str(p, "device");
        if (dev is null || !this.deviceMetrics.ContainsKey(dev) || this.online.Contains(dev))
        {
            throw new CallError("invalid-request", $"no offline device {dev}");
        }

        await this.node.PublishDeviceBirthMessage([.. this.deviceMetrics[dev].Values.Select(e => e.Known)], dev);
        this.online.Add(dev);
        return new JsonObject { ["published"] = new JsonArray($"DBIRTH/{dev}") };
    }
}

// ---- host ----

internal sealed class Host : Role
{
    private static readonly AsyncLocal<Wire.Message?> Current = new();

    private readonly string hostId;
    private readonly (string Host, int Port) broker;
    private readonly string clientId;
    private readonly object track = new();
    private readonly Dictionary<string, string> groups = [];
    private readonly Dictionary<string, long?> bdSeqs = [];
    private SparkplugApplication app = null!;

    public Host(Dictionary<string, object> flags) : base(flags)
    {
        this.hostId = (string)flags["host-id"];
        this.broker = ((string, int))flags["broker"];
        this.clientId = flags.TryGetValue("client-id", out var c) ? (string)c : $"sparkplugb-interop-{Program.Adapter}-host-{this.hostId}";
    }

    // Receiving reads seq and bdSeq off the wire, then lets the library handle the message.
    private async Task Receiving(MqttApplicationMessageReceivedEventArgs args, Func<Task> library)
    {
        var w = Wire.Parse(args.ApplicationMessage.Topic, args.ApplicationMessage.Payload.ToArray());
        if (w is null || w.Type is "NCMD" or "DCMD")
        {
            await library();
            return;
        }

        Current.Value = w;
        var before = this.handled;
        await library();
        if (this.handled == before)
        {
            Out.Diag($"SparkplugNet did not deliver the {w.Type} on {args.ApplicationMessage.Topic}");
        }
    }

    private long handled;

    private Task Message(string type, string group, string edgeNode, string? device, IEnumerable<Metric> metrics)
    {
        Interlocked.Increment(ref this.handled);
        var w = Current.Value;
        var list = metrics.Where(m => m.Name != "bdSeq").ToList();
        var birthOrDeath = type is "NBIRTH" or "NDEATH";
        lock (this.track)
        {
            this.groups[edgeNode] = group;
            if (birthOrDeath)
            {
                this.bdSeqs[$"{group}/{edgeNode}"] = w?.BdSeq;
            }
        }

        Out.Emit("message", ("type", type), ("group", group), ("edgeNode", edgeNode), ("device", device),
            ("seq", type == "NDEATH" || w?.Seq is null ? null : JsonValue.Create(w.Seq.Value)),
            ("bdSeq", birthOrDeath && w?.BdSeq is not null ? JsonValue.Create(w.BdSeq.Value) : null),
            ("metrics", new JsonArray([.. list.Select(m => (JsonNode)Values.Doc(m))])));
        return Task.CompletedTask;
    }

    public override async Task Run()
    {
        this.app = new SparkplugApplication(new PassThrough(), SparkplugSpecificationVersion.Version30);
        var client = ObservedClient.Observe(this.app);
        client.Receiving = this.Receiving;
        this.WatchConnection(client);
        this.app.NodeBirthReceived += a => this.Message("NBIRTH", a.GroupIdentifier, a.EdgeNodeIdentifier, null, a.Metrics);
        this.app.NodeDataReceived += a => this.Message("NDATA", a.GroupIdentifier, a.EdgeNodeIdentifier, null, a.Metrics);
        this.app.NodeDeathReceived += a => this.Message("NDEATH", a.GroupIdentifier, a.EdgeNodeIdentifier, null, []);
        this.app.DeviceBirthReceived += a => this.Message("DBIRTH", a.GroupIdentifier, a.EdgeNodeIdentifier, a.DeviceIdentifier, a.Metrics);
        this.app.DeviceDataReceived += a => this.Message("DDATA", a.GroupIdentifier, a.EdgeNodeIdentifier, a.DeviceIdentifier, a.Metrics);
        this.app.DeviceDeathReceived += a => this.Message("DDEATH", a.GroupIdentifier, a.EdgeNodeIdentifier, a.DeviceIdentifier, []);
        var options = new SparkplugApplicationOptions(
            brokerAddress: this.broker.Host, port: this.broker.Port, clientId: this.clientId,
            scadaHostIdentifier: this.hostId, isPrimaryApplication: true)
        {
            // As for the edge node: no credentials rather than empty ones.
            UserName = null!,
            Password = null!,
        };
        await this.Connect(() => this.app.Start(options));
        _ = new Control(this.SocketPath, this.Gate, new()
        {
            ["send-command"] = this.SendCommand,
            ["request-rebirth"] = this.RequestRebirth,
            ["nodes"] = this.Nodes,
        });
        this.MarkReady();
        Out.Emit("ready", ("adapter", Program.Adapter), ("hostId", this.hostId));
        await this.RunUntilTerm(() => this.app.Stop());
    }

    private static (string Group, string Node, string? Device) Target(JsonElement p, bool withDevice)
    {
        string Id(string key) =>
            p.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String && Fixture.Id.IsMatch(v.GetString()!)
                ? v.GetString()!
                : throw new CallError("invalid-request", $"{key} is missing or invalid");

        string? dev = null;
        if (withDevice && p.TryGetProperty("device", out var d) && d.ValueKind != JsonValueKind.Null)
        {
            dev = d.ValueKind == JsonValueKind.String && Fixture.Id.IsMatch(d.GetString()!) ? d.GetString() : throw new CallError("invalid-request", "device is invalid");
        }

        return (Id("group"), Id("edgeNode"), dev);
    }

    private async Task<JsonNode?> SendCommand(JsonElement p)
    {
        var (group, nodeId, dev) = Target(p, true);
        if (!p.TryGetProperty("metrics", out var ms) || ms.ValueKind != JsonValueKind.Array || ms.GetArrayLength() == 0)
        {
            throw new CallError("invalid-request", "metrics is missing or empty");
        }

        var metrics = new List<Metric>();
        foreach (var m in ms.EnumerateArray())
        {
            if (m.ValueKind != JsonValueKind.Object || !m.TryGetProperty("name", out var n) || n.ValueKind != JsonValueKind.String
                || !m.TryGetProperty("datatype", out var dt) || dt.ValueKind != JsonValueKind.String || !Values.DataTypes.Contains(dt.GetString())
                || !m.TryGetProperty("value", out var v))
            {
                throw new CallError("invalid-request", $"invalid metric {m.GetRawText()}");
            }

            try
            {
                Values.Check(n.GetString()!, dt.GetString()!, v);
            }
            catch (FixtureError e)
            {
                throw new CallError("invalid-request", e.Message);
            }

            metrics.Add(new Metric(n.GetString()!, Values.ToSpb(dt.GetString()!), Values.ToClr(dt.GetString()!, v), DateTimeOffset.UtcNow));
        }

        if (dev is null)
        {
            await this.app.PublishNodeCommand(metrics, group, nodeId);
            return new JsonObject { ["published"] = new JsonArray("NCMD") };
        }

        await this.app.PublishDeviceCommand(metrics, group, nodeId, dev);
        return new JsonObject { ["published"] = new JsonArray($"DCMD/{dev}") };
    }

    private async Task<JsonNode?> RequestRebirth(JsonElement p)
    {
        var (group, nodeId, _) = Target(p, false);
        await this.app.PublishNodeCommand([new Metric("Node Control/Rebirth", DataType.Boolean, true, DateTimeOffset.UtcNow)], group, nodeId);
        return new JsonObject { ["published"] = new JsonArray("NCMD") };
    }

    // Nodes reports what SparkplugNet tracks (NodeStates and DeviceStates), with
    // the group and bdSeq the adapter saw, which the library does not keep.
    private Task<JsonNode?> Nodes(JsonElement _)
    {
        var nodes = new List<JsonObject>();
        lock (this.track)
        {
            foreach (var (edgeNode, state) in this.app.NodeStates)
            {
                var group = this.groups.GetValueOrDefault(edgeNode, string.Empty);
                var devices = this.app.DeviceStates
                    .Where(kv => kv.Key.StartsWith(edgeNode + "/", StringComparison.Ordinal))
                    .OrderBy(kv => kv.Key, StringComparer.Ordinal)
                    .Select(kv => (JsonNode)new JsonObject
                    {
                        ["device"] = kv.Key[(edgeNode.Length + 1)..],
                        ["online"] = kv.Value.MetricStatus == SparkplugMetricStatus.Online,
                    });
                var bd = this.bdSeqs.GetValueOrDefault($"{group}/{edgeNode}");
                nodes.Add(new JsonObject
                {
                    ["group"] = group,
                    ["edgeNode"] = edgeNode,
                    ["online"] = state.MetricStatus == SparkplugMetricStatus.Online,
                    ["bdSeq"] = bd is null ? null : JsonValue.Create(bd.Value),
                    ["devices"] = new JsonArray([.. devices]),
                });
            }
        }

        var sorted = nodes.OrderBy(n => (string)n["group"]!, StringComparer.Ordinal).ThenBy(n => (string)n["edgeNode"]!, StringComparer.Ordinal);
        return Task.FromResult<JsonNode?>(new JsonObject { ["nodes"] = new JsonArray([.. sorted]) });
    }
}

// ---- commands ----

internal static class Program
{
    public const string Adapter = "sparkplugnet";
    public const string DefaultReady = "/run/sparkplugb-interop/ready";
    public const string DefaultSocket = "/run/sparkplugb-interop/control.sock";
    public const int ExitFailed = 1, ExitUsage = 2, ExitConnect = 3, ExitFixture = 4, ExitUnsupported = 5;

    public static readonly string AdapterVersion = Environment.GetEnvironmentVariable("SPARKPLUGB_INTEROP_ADAPTER_VERSION") ?? "dev";
    public static readonly string UpstreamVersion = Environment.GetEnvironmentVariable("SPARKPLUGB_INTEROP_UPSTREAM_VERSION")
        ?? typeof(SparkplugNode).Assembly.GetName().Version?.ToString(3) ?? "unknown";
    public static readonly string FixturesDir = Environment.GetEnvironmentVariable("SPARKPLUGB_INTEROP_FIXTURES") ?? "/fixtures";

    private static readonly string[] EdgeCalls = ["publish-data", "device-death", "device-birth"];
    private static readonly string[] HostCalls = ["send-command", "request-rebirth", "nodes"];
    private static readonly string[] AllCalls = ["publish-data", "rebirth", "device-death", "device-birth", "send-command", "request-rebirth", "nodes"];

    private static readonly Dictionary<string, string> Common = new()
    {
        ["broker"] = "url", ["client-id"] = "str", ["connect-timeout-ms"] = "int", ["ready-file"] = "str", ["control-socket"] = "str",
    };

    public static JsonObject Result(string? op, bool ok, JsonObject? error, JsonNode? result, long start) => new()
    {
        ["schemaVersion"] = "1.0",
        ["adapter"] = Adapter,
        ["operation"] = op,
        ["ok"] = ok,
        ["error"] = error,
        ["result"] = result,
        ["elapsedMs"] = Environment.TickCount64 - start,
    };

    private static JsonObject Capabilities() => new()
    {
        ["schemaVersion"] = "1.0",
        ["adapter"] = Adapter,
        ["adapterVersion"] = AdapterVersion,
        ["protocol"] = "sparkplug-b-3.0.0",
        ["upstream"] = new JsonObject
        {
            ["name"] = "SparkplugNet", ["version"] = UpstreamVersion, ["license"] = "MIT", ["language"] = "C#", ["independentEngine"] = true,
        },
        ["roles"] = new JsonObject
        {
            ["edge"] = new JsonObject
            {
                ["supported"] = true,
                ["fixtures"] = new JsonArray("baseline", "scalars"),
                ["calls"] = new JsonArray([.. EdgeCalls.Select(c => (JsonNode)c)]),
                ["features"] = new JsonObject
                {
                    ["aliases"] = false, ["rebirthCommand"] = false, ["writes"] = true, ["primaryHost"] = false, ["gracefulDeath"] = true,
                },
            },
            ["host"] = new JsonObject
            {
                ["supported"] = true,
                ["datatypes"] = new JsonArray([.. Values.DataTypes.Select(d => (JsonNode)d)]),
                ["calls"] = new JsonArray([.. HostCalls.Select(c => (JsonNode)c)]),
                ["features"] = new JsonObject { ["aliasResolution"] = false, ["seqCheck"] = false, ["gracefulOffline"] = true },
            },
        },
    };

    private static async Task<int> Call(string[] args)
    {
        if (args.Length == 0)
        {
            throw new Usage("call needs an operation");
        }

        var op = args[0];
        if (!AllCalls.Contains(op))
        {
            throw new Usage($"unknown operation {op}");
        }

        var flags = Flags.Parse(args[1..], new() { ["payload"] = "json", ["timeout-ms"] = "int", ["control-socket"] = "str" });
        var path = flags.TryGetValue("control-socket", out var s) ? (string)s : DefaultSocket;
        var timeout = flags.TryGetValue("timeout-ms", out var t) ? (int)t : 10000;
        var payload = flags.TryGetValue("payload", out var p) ? ((JsonElement)p).GetRawText() : "{}";
        var start = Environment.TickCount64;
        JsonNode doc;
        using var cts = new CancellationTokenSource(timeout);
        try
        {
            using var sock = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
            await sock.ConnectAsync(new UnixDomainSocketEndPoint(path), cts.Token);
            await using var stream = new NetworkStream(sock);
            var req = new JsonObject { ["operation"] = op, ["payload"] = JsonNode.Parse(payload) };
            await stream.WriteAsync(Encoding.UTF8.GetBytes(req.ToJsonString() + "\n"), cts.Token);
            using var reader = new StreamReader(stream, Encoding.UTF8);
            var line = await reader.ReadLineAsync(cts.Token);
            doc = JsonNode.Parse(line ?? throw new IOException("no answer"))!;
        }
        catch (OperationCanceledException)
        {
            doc = Result(op, false, new JsonObject { ["code"] = "timeout", ["detail"] = "no answer in time" }, null, start);
        }
        catch (Exception e) when (e is SocketException or IOException or JsonException)
        {
            doc = Result(op, false, new JsonObject { ["code"] = "not-connected", ["detail"] = e.Message }, null, start);
        }

        Out.Line(doc);
        if (doc["ok"]?.GetValue<bool>() == true)
        {
            return 0;
        }

        return (string?)doc["error"]?["code"] == "unsupported" ? ExitUnsupported : ExitFailed;
    }

    public static async Task<int> Main(string[] argv)
    {
        if (argv.Length == 0)
        {
            argv = ["edge"];
        }

        var (cmd, args) = (argv[0], argv[1..]);
        try
        {
            switch (cmd)
            {
                case "print-capabilities":
                    Flags.Parse(args, new());
                    Out.Line(Capabilities());
                    return 0;
                case "print-fixture":
                    {
                        if (args.Length > 1)
                        {
                            throw new Usage("print-fixture takes at most one name");
                        }

                        var name = args.Length == 1 ? args[0] : "baseline";
                        var path = Path.Combine(FixturesDir, name, "fixture.json");
                        if (!Fixture.Id.IsMatch(name) || !File.Exists(path))
                        {
                            Out.Diag($"no baked fixture '{name}'");
                            return ExitFailed;
                        }

                        using var stdout = Console.OpenStandardOutput();
                        stdout.Write(File.ReadAllBytes(path));
                        return 0;
                    }

                case "version":
                    Flags.Parse(args, new());
                    Console.Out.WriteLine($"sparkplugb-interop-{Adapter} {AdapterVersion} (SparkplugNet {UpstreamVersion})");
                    return 0;
                case "call":
                    return await Call(args);
                case "edge":
                    {
                        var flags = Flags.Parse(args, new(Common) { ["fixture"] = "str", ["group"] = "id", ["edge-node"] = "id", ["primary-host"] = "id" }, "broker");
                        if (flags.ContainsKey("primary-host"))
                        {
                            Out.Diag("--primary-host is not supported: SparkplugNode publishes NBIRTH as soon as it connects and watches the Sparkplug 2.2 STATE topic");
                            return ExitUnsupported;
                        }

                        await new Edge(flags).Run();
                        return 0;
                    }

                case "host":
                    {
                        var flags = Flags.Parse(args, new(Common) { ["host-id"] = "id" }, "broker", "host-id");
                        await new Host(flags).Run();
                        return 0;
                    }

                default:
                    throw new Usage($"unknown command {cmd}");
            }
        }
        catch (Usage e)
        {
            Out.Diag($"usage: {e.Message}");
            return ExitUsage;
        }
        catch (FixtureError e)
        {
            Out.Diag($"fixture: {e.Message}");
            return ExitFixture;
        }
    }
}
