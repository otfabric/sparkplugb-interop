// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.StandardProtocolFamily;
import java.net.UnixDomainSocketAddress;
import java.nio.channels.Channels;
import java.nio.channels.ServerSocketChannel;
import java.nio.channels.SocketChannel;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

/** Serves "call" on a Unix socket: one JSON line in, one result document out. */
final class Control {

	static final Set<String> ALL_CALLS = Set.of("publish-data", "rebirth", "device-death", "device-birth",
			"send-command", "request-rebirth", "nodes");

	@FunctionalInterface
	interface Handler {
		JsonNode handle(JsonNode payload) throws Exception;
	}

	static final class CallError extends Exception {
		private static final long serialVersionUID = 1L;
		final String code;

		CallError(String code, String detail) {
			super(detail);
			this.code = code;
		}
	}

	private final Map<String, Handler> handlers;

	private Control(Map<String, Handler> handlers) {
		this.handlers = handlers;
	}

	static void serve(String path, Map<String, Handler> handlers) throws Exception {
		Control control = new Control(handlers);
		Path p = Path.of(path);
		Files.deleteIfExists(p);
		if (p.getParent() != null) {
			Files.createDirectories(p.getParent());
		}
		ServerSocketChannel server = ServerSocketChannel.open(StandardProtocolFamily.UNIX);
		server.bind(UnixDomainSocketAddress.of(p));
		Thread accept = new Thread(() -> {
			while (true) {
				try {
					SocketChannel conn = server.accept();
					Thread t = new Thread(() -> control.answer(conn), "control-conn");
					t.setDaemon(true);
					t.start();
				} catch (Exception e) {
					Out.diag("control socket: " + e);
					return;
				}
			}
		}, "control-accept");
		accept.setDaemon(true);
		accept.start();
	}

	private void answer(SocketChannel conn) {
		try (conn) {
			BufferedReader in =
					new BufferedReader(new InputStreamReader(Channels.newInputStream(conn), StandardCharsets.UTF_8));
			String line = in.readLine();
			ObjectNode doc = serve(line);
			OutputStream out = Channels.newOutputStream(conn);
			out.write((doc.toString() + "\n").getBytes(StandardCharsets.UTF_8));
			out.flush();
		} catch (Exception e) {
			Out.diag("control connection: " + e);
		}
	}

	private ObjectNode serve(String line) {
		long start = System.nanoTime();
		String op = null;
		try {
			JsonNode req = Out.JSON.readTree(line == null ? "" : line);
			if (req == null || !req.isObject()) {
				throw new CallError("invalid-request", "the request is not a JSON object");
			}
			op = req.path("operation").isTextual() ? req.get("operation").asText() : null;
			JsonNode payload = req.get("payload");
			if (payload == null || payload.isNull()) {
				payload = Out.object();
			}
			Handler h = op == null ? null : handlers.get(op);
			if (h == null) {
				throw new CallError("unsupported", op + " is not offered by this role");
			}
			if (!payload.isObject()) {
				throw new CallError("invalid-request", "payload is not an object");
			}
			return result(op, true, null, h.handle(payload), start);
		} catch (CallError e) {
			return result(op, false, error(e.code, e.getMessage()), null, start);
		} catch (Exception e) {
			e.printStackTrace(System.err);
			return result(op, false, error("invalid-request", String.valueOf(e)), null, start);
		}
	}

	static ObjectNode error(String code, String detail) {
		ObjectNode e = Out.object();
		e.put("code", code);
		e.put("detail", detail);
		return e;
	}

	static ObjectNode result(String op, boolean ok, ObjectNode error, JsonNode result, long startNanos) {
		ObjectNode d = Out.object();
		d.put("schemaVersion", "1.0");
		d.put("adapter", Main.ADAPTER);
		d.put("operation", op);
		d.put("ok", ok);
		d.set("error", error);
		d.set("result", result);
		d.put("elapsedMs", TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - startNanos));
		return d;
	}

	/** The "call" command: sends one operation to the running role and prints its result document. */
	static int call(String[] args) throws Flags.Usage {
		if (args.length < 2) {
			throw new Flags.Usage("call needs an operation");
		}
		String op = args[1];
		if (!ALL_CALLS.contains(op)) {
			throw new Flags.Usage("unknown operation " + op);
		}
		Flags f = Flags.parse(args, 2,
				Map.of("payload", Flags.Kind.JSON, "timeout-ms", Flags.Kind.INT, "control-socket", Flags.Kind.STR));
		String path = f.str("control-socket", Main.DEFAULT_SOCKET);
		int timeout = f.integer("timeout-ms", 10000);
		JsonNode payload = f.has("payload") ? f.json("payload") : Out.object();
		long start = System.nanoTime();
		ObjectNode request = Out.object();
		request.put("operation", op);
		request.set("payload", payload);

		ExecutorService ex = Executors.newSingleThreadExecutor(r -> {
			Thread t = new Thread(r, "call");
			t.setDaemon(true);
			return t;
		});
		ObjectNode doc;
		Future<String> answer = ex.submit(() -> {
			try (SocketChannel ch = SocketChannel.open(StandardProtocolFamily.UNIX)) {
				ch.connect(UnixDomainSocketAddress.of(path));
				OutputStream out = Channels.newOutputStream(ch);
				out.write((request.toString() + "\n").getBytes(StandardCharsets.UTF_8));
				out.flush();
				return new BufferedReader(new InputStreamReader(Channels.newInputStream(ch), StandardCharsets.UTF_8))
						.readLine();
			}
		});
		try {
			String line = answer.get(timeout, TimeUnit.MILLISECONDS);
			JsonNode got = line == null ? null : Out.JSON.readTree(line);
			if (got == null || !got.isObject()) {
				throw new IllegalStateException("no answer on " + path);
			}
			doc = (ObjectNode) got;
		} catch (TimeoutException e) {
			doc = result(op, false, error("timeout", "no answer in time"), null, start);
		} catch (Exception e) {
			Throwable c = e.getCause() != null ? e.getCause() : e;
			doc = result(op, false, error("not-connected", String.valueOf(c.getMessage())), null, start);
		}
		Out.line(doc.toString());
		if (doc.path("ok").asBoolean(false)) {
			return 0;
		}
		return "unsupported".equals(doc.path("error").path("code").asText()) ? Main.EXIT_UNSUPPORTED : Main.EXIT_FAILED;
	}
}
