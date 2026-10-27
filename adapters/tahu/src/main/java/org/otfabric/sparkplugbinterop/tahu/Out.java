// SPDX-License-Identifier: MIT
package org.otfabric.sparkplugbinterop.tahu;

import java.io.FileDescriptor;
import java.io.FileOutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;

/**
 * stdout carries JSON only (docs/CONTAINER_CONTRACT.md, output rules). The real stdout is kept here and System.out
 * is pointed at stderr, so nothing an upstream prints can reach it.
 */
final class Out {

	static final ObjectMapper JSON = new ObjectMapper();

	private static final PrintStream STDOUT =
			new PrintStream(new FileOutputStream(FileDescriptor.out), false, StandardCharsets.UTF_8);
	private static final DateTimeFormatter TIME =
			DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'").withZone(ZoneOffset.UTC);

	private Out() {
	}

	static void init() {
		System.setOut(System.err);
	}

	static String now() {
		return TIME.format(Instant.now());
	}

	static ObjectNode object() {
		return JSON.createObjectNode();
	}

	/** Writes one event line: event and time first, then the fields. */
	static void emit(String event, ObjectNode fields) {
		ObjectNode line = object();
		line.put("event", event);
		line.put("time", now());
		if (fields != null) {
			line.setAll(fields);
		}
		line(line.toString());
	}

	static void emit(String event) {
		emit(event, null);
	}

	static synchronized void line(String s) {
		STDOUT.print(s);
		STDOUT.print('\n');
		STDOUT.flush();
	}

	static synchronized void raw(byte[] bytes) {
		STDOUT.write(bytes, 0, bytes.length);
		STDOUT.flush();
	}

	static void diag(String msg) {
		System.err.println("[tahu] " + msg);
		System.err.flush();
	}

	/** Ends the process at once with the code, without shutdown hooks. */
	static void halt(int code) {
		STDOUT.flush();
		System.err.flush();
		Runtime.getRuntime().halt(code);
	}
}
