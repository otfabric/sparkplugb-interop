// SPDX-License-Identifier: MIT
//
// Copies the licence files of every installed npm package to <dir>/<name>/.
//
// Usage: node collect-licenses.js <node_modules> <dir> [<package>=<package with the same licence> ...]
//
// Run at image build time. Also writes <dir>/BUNDLED-MODULES.txt (name,
// version and declared licence of each package). Fails when a package has no
// licence file, unless an argument names a package of the same upstream
// project whose licence file covers it (sparkplug-payload, published by
// Eclipse Tahu without one, is covered by sparkplug-client's).

"use strict";

const fs = require("fs");
const path = require("path");

const LICENSE = /^(licen[cs]e|copying|notice|authors)/i;

function packages(dir) {
    const out = [];
    for (const entry of fs.readdirSync(dir)) {
        if (entry.startsWith(".")) continue;
        const p = path.join(dir, entry);
        if (entry.startsWith("@")) {
            out.push(...packages(p));
        } else if (fs.existsSync(path.join(p, "package.json"))) {
            out.push(p);
            if (fs.existsSync(path.join(p, "node_modules"))) out.push(...packages(path.join(p, "node_modules")));
        }
    }
    return out;
}

function main([modules, out, ...same]) {
    const covered = new Map(same.map((s) => s.split("=")));
    const found = new Map();
    const bundled = [];
    const missing = [];
    for (const dir of packages(modules)) {
        const pkg = JSON.parse(fs.readFileSync(path.join(dir, "package.json"), "utf8"));
        const id = `${pkg.name}@${pkg.version}`;
        if (found.has(id)) continue;
        const files = fs.readdirSync(dir).filter((f) => LICENSE.test(f) && fs.statSync(path.join(dir, f)).isFile());
        found.set(id, { dir, pkg, files });
    }
    for (const [id, { dir, pkg, files }] of [...found.entries()].sort()) {
        bundled.push(`${id} ${typeof pkg.license === "string" ? pkg.license : JSON.stringify(pkg.license || null)}`);
        const target = path.join(out, `${pkg.name}@${pkg.version}`);
        fs.mkdirSync(target, { recursive: true });
        if (files.length > 0) {
            for (const f of files) fs.copyFileSync(path.join(dir, f), path.join(target, f));
            continue;
        }
        const other = covered.get(pkg.name);
        const source = other && [...found.values()].find((v) => v.pkg.name === other && v.files.length > 0);
        if (!source) {
            missing.push(id);
            continue;
        }
        for (const f of source.files) fs.copyFileSync(path.join(source.dir, f), path.join(target, f));
        fs.writeFileSync(path.join(target, "SOURCE.txt"),
            `${id} ships no licence file. It is published by the same project as ${source.pkg.name}, ` +
            `under the same licence (${pkg.license}); this is ${source.pkg.name}'s licence file.\n`);
    }
    fs.writeFileSync(path.join(out, "BUNDLED-MODULES.txt"), bundled.join("\n") + "\n");
    if (missing.length > 0) {
        process.stderr.write(`no licence file in: ${missing.join(", ")}\n`);
        return 1;
    }
    return 0;
}

process.exit(main(process.argv.slice(2)));
