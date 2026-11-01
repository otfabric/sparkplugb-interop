// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;

import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

import sun.misc.Signal;

/** sparkplugb-interop adapter for Eclipse Tahu (docs/CONTAINER_CONTRACT.md). */
public final class Main {

	static final String ADAPTER = "tahu";
	static final String ADAPTER_VERSION = env("SPARKPLUGB_INTEROP_ADAPTER_VERSION", "dev");
	static final String UPSTREAM_VERSION = env("SPARKPLUGB_INTEROP_UPSTREAM_VERSION", "1.0.21");
	static final String RUN_DIR = "/run/sparkplugb-interop";
	static final String DEFAULT_READY = RUN_DIR + "/ready";
	static final String DEFAULT_SOCKET = RUN_DIR + "/control.sock";

	static final int EXIT_FAILED = 1, EXIT_USAGE = 2, EXIT_CONNECT = 3, EXIT_FIXTURE = 4, EXIT_UNSUPPORTED = 5;

	static final List<String> EDGE_CALLS = List.of("publish-data", "rebirth", "device-death", "device-birth");
	static final List<String> HOST_CALLS = List.of("send-command", "request-rebirth", "nodes");

	private Main() {
	}

	private static String env(String name, String dflt) {
		String v = System.getenv(name);
		return v == null || v.isEmpty() ? dflt : v;
	}

	public static void main(String[] argv) {
		Out.init();
		int code;
		try {
			code = run(argv.length == 0 ? new String[] {"edge"} : argv);
		} catch (Flags.Usage e) {
			Out.diag("usage: " + e.getMessage());
			code = EXIT_USAGE;
		} catch (Fixture.Invalid e) {
			Out.diag("fixture: " + e.getMessage());
			code = EXIT_FIXTURE;
		} catch (Throwable t) {
			t.printStackTrace(System.err);
			code = EXIT_FAILED;
		}
		Out.halt(code);
	}

	private static int run(String[] args) throws Exception {
		Map<String, Flags.Kind> common = new HashMap<>(Map.of("broker", Flags.Kind.URL, "client-id", Flags.Kind.STR,
				"connect-timeout-ms", Flags.Kind.INT, "ready-file", Flags.Kind.STR, "control-socket", Flags.Kind.STR));
		switch (args[0]) {
			case "print-capabilities":
				Flags.parse(args, 1, Map.of());
				Out.line(capabilities().toString());
				return 0;
			case "print-fixture": {
				if (args.length > 2) {
					throw new Flags.Usage("print-fixture takes at most one name");
				}
				String name = args.length > 1 ? args[1] : "baseline";
				Path path = Path.of(Fixture.DIR, name, "fixture.json");
				if (!Flags.ID.matcher(name).matches() || !Files.isRegularFile(path)) {
					Out.diag("no baked fixture '" + name + "'");
					return EXIT_FAILED;
				}
				Out.raw(Files.readAllBytes(path));
				return 0;
			}
			case "version":
				Flags.parse(args, 1, Map.of());
				Out.line("sparkplugb-interop-" + ADAPTER + " " + ADAPTER_VERSION + " (Eclipse Tahu " + UPSTREAM_VERSION
						+ ")");
				return 0;
			case "call":
				return Control.call(args);
			case "edge": {
				Map<String, Flags.Kind> spec = new HashMap<>(common);
				spec.putAll(Map.of("fixture", Flags.Kind.STR, "group", Flags.Kind.ID, "edge-node", Flags.Kind.ID,
						"primary-host", Flags.Kind.ID));
				new Edge(Flags.parse(args, 1, spec, "broker")).run();
				return 0;
			}
			case "host": {
				Map<String, Flags.Kind> spec = new HashMap<>(common);
				spec.put("host-id", Flags.Kind.ID);
				new Host(Flags.parse(args, 1, spec, "broker", "host-id")).run();
				return 0;
			}
			default:
				throw new Flags.Usage("unknown command " + args[0]);
		}
	}

	static ObjectNode capabilities() {
		ObjectNode c = Out.object();
		c.put("schemaVersion", "1.0");
		c.put("adapter", ADAPTER);
		c.put("adapterVersion", ADAPTER_VERSION);
		c.put("protocol", "sparkplug-b-3.0.0");
		ObjectNode up = c.putObject("upstream");
		up.put("name", "Eclipse Tahu");
		up.put("version", UPSTREAM_VERSION);
		up.put("license", "EPL-2.0");
		up.put("language", "Java");
		up.put("independentEngine", true);
		ObjectNode roles = c.putObject("roles");
		ObjectNode edge = roles.putObject("edge");
		edge.put("supported", true);
		edge.putArray("fixtures").add("baseline").add("scalars").add("composite");
		ArrayNode ec = edge.putArray("calls");
		EDGE_CALLS.forEach(ec::add);
		ObjectNode ef = edge.putObject("features");
		ef.put("aliases", true);
		ef.put("rebirthCommand", true);
		ef.put("writes", true);
		ef.put("primaryHost", true);
		ef.put("gracefulDeath", true);
		ObjectNode host = roles.putObject("host");
		host.put("supported", true);
		ArrayNode dt = host.putArray("datatypes");
		Values.DATATYPES.forEach(dt::add);
		ArrayNode hc = host.putArray("calls");
		HOST_CALLS.forEach(hc::add);
		ObjectNode hf = host.putObject("features");
		hf.put("aliasResolution", true);
		hf.put("seqCheck", true);
		hf.put("gracefulOffline", true);
		return c;
	}

	static void markReady(String file) throws Exception {
		Path p = Path.of(file);
		if (p.getParent() != null) {
			Files.createDirectories(p.getParent());
		}
		Files.writeString(p, "ready\n");
	}

	/** Runs stop on SIGTERM or SIGINT; stop ends the process. */
	static void onTerm(Runnable stop) {
		Signal.handle(new Signal("TERM"), s -> stop.run());
		Signal.handle(new Signal("INT"), s -> stop.run());
	}

	static void park() throws InterruptedException {
		new CountDownLatch(1).await();
	}
}
