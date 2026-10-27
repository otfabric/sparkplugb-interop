#!/usr/bin/env python3
"""Latest upstream release of an adapter's library or of a broker, vs the pin.

Usage: upstream.py <name>      e.g. upstream.py srad, upstream.py nanomq
       upstream.py --names     every adapter and broker of versions.yaml

Prints key=value lines for $GITHUB_OUTPUT: kind (adapter or broker), pin,
latest, update (yes when the pin is not the latest release), image (brokers:
the Docker Hub reference of the latest release), known (the entries of
tests/known-bugs.tsv for the pinned version). Only releases count: no
pre-release, release candidate or development build. Reads versions.yaml,
which scripts/check-versions.sh keeps equal to the Dockerfiles. Never writes.
"""
import json
import re
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# Where each adapter's upstream publishes its releases.
ADAPTERS = {
    "tahu": ("Maven Central", "https://repo1.maven.org/maven2/org/eclipse/tahu/tahu-edge/maven-metadata.xml"),
    "pysparkplug": ("PyPI", "https://pypi.org/pypi/pysparkplug/json"),
    "sparkplugnet": ("NuGet", "https://api.nuget.org/v3-flatcontainer/sparkplugnet/index.json"),
    "srad": ("crates.io", "https://crates.io/api/v1/crates/srad"),
    "tahu-js": ("npm", "https://registry.npmjs.org/sparkplug-client"),
    "sparkplughost": ("Go module", "https://proxy.golang.org/github.com/!evergen!energy/sparkplughost/@v/list"),
}

# Release tags of each broker on Docker Hub.
BROKERS = {
    "mosquitto": ("library/eclipse-mosquitto", r"(\d+)\.(\d+)\.(\d+)-alpine"),
    "hivemq-ce": ("hivemq/hivemq-ce", r"(\d{4})\.(\d+)"),
    "emqx": ("emqx/emqx", r"(\d+)\.(\d+)\.(\d+)"),
    "nanomq": ("emqx/nanomq", r"(\d+)\.(\d+)\.(\d+)"),
}

RELEASE = re.compile(r"^v?(\d+)\.(\d+)\.(\d+)$")


def versions_yaml():
    """{section: {name: {key: value}}} of the two-level sections of versions.yaml."""
    out, section, name = {}, None, None
    for line in (ROOT / "versions.yaml").read_text().splitlines():
        line = re.sub(r"\s+#.*$", "", line)
        if m := re.match(r"^([a-z_]+):\s*$", line):
            section, name = m.group(1), None
            out[section] = {}
        elif section and (m := re.match(r"^  ([a-z0-9-]+):\s*$", line)):
            name = m.group(1)
            out[section][name] = {}
        elif section and name and (m := re.match(r"^    ([a-z_]+):\s*(.*)$", line)):
            out[section][name][m.group(1)] = m.group(2).strip().strip('"')
        elif re.match(r"^[a-z_]+:", line):
            section = None
    return out


def get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "sparkplugb-interop upstream check (github.com/otfabric)"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read().decode()


def newest(versions):
    """The highest release of versions, by number."""
    found = [(tuple(int(x) for x in m.groups()), v) for v in versions if (m := RELEASE.match(v))]
    return max(found)[1] if found else ""


def latest_adapter(name):
    _, url = ADAPTERS[name]
    body = get(url)
    if name == "tahu":
        return newest(re.findall(r"<version>([^<]+)</version>", body))
    if name == "pysparkplug":
        return newest(json.loads(body)["releases"])
    if name == "sparkplugnet":
        return newest(json.loads(body)["versions"])
    if name == "srad":
        return newest(v["num"] for v in json.loads(body)["versions"] if not v["yanked"])
    if name == "tahu-js":
        return newest(json.loads(body)["versions"])
    if name == "sparkplughost":
        return newest(body.split())
    raise SystemExit(f"no registry for {name}")


def latest_broker(name):
    repo, pattern = BROKERS[name]
    tag = re.compile(f"^{pattern}$")
    found, url = [], f"https://hub.docker.com/v2/repositories/{repo}/tags?page_size=100&ordering=last_updated"
    for _ in range(5):                       # the newest 500 tags
        page = json.loads(get(url))
        found += [(tuple(int(x) for x in m.groups()), t["name"]) for t in page["results"] if (m := tag.match(t["name"]))]
        url = page.get("next")
        if not url:
            break
    return max(found)[1] if found else ""


def known(name, pin):
    out = []
    for line in (ROOT / "tests/known-bugs.tsv").read_text().splitlines():
        cols = line.split("\t")
        if len(cols) == 8 and cols[0] == name and cols[1] == pin:
            out.append(f"{cols[2]} with {cols[3]} on {cols[4]}, {cols[5]}/{cols[6]}: {cols[7]}")
    return out


def main():
    data = versions_yaml()
    adapters = [n for n in data["implementations"] if (ROOT / "adapters" / n / "Dockerfile").exists()]
    if sys.argv[1:] == ["--names"]:
        print(json.dumps(adapters + list(data["brokers"])))
        return
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    name = sys.argv[1]
    if name in adapters:
        kind, pin = "adapter", data["implementations"][name]["version"]
        latest, image = latest_adapter(name), ""
        source = ADAPTERS[name][0]
        upstream = data["implementations"][name]["upstream"]
    elif name in data["brokers"]:
        kind = "broker"
        pinned = data["brokers"][name]["image"]
        pin = pinned.split("@")[0].rsplit(":", 1)[1]
        latest = latest_broker(name)
        image = f"{BROKERS[name][0].removeprefix('library/')}:{latest}" if latest else ""
        source = "Docker Hub"
        upstream = data["brokers"][name]["upstream"]
    else:
        raise SystemExit(f"{name} is neither an adapter nor a broker of versions.yaml")
    if not latest:
        raise SystemExit(f"could not read the latest release of {name} from {source}")
    print(f"kind={kind}")
    print(f"upstream={upstream}")
    print(f"source={source}")
    print(f"pin={pin}")
    print(f"latest={latest}")
    print(f"image={image}")
    print(f"update={'yes' if pin != latest else 'no'}")
    print(f"known={'; '.join(known(name, pin)) if kind == 'adapter' else ''}")
    print(f"resolved {name}: pin={pin} latest={latest}", file=sys.stderr)


if __name__ == "__main__":
    main()
