# sparkplugb-interop-otfabric

[go-sparkplugb](https://github.com/otfabric/go-sparkplugb), OT Fabric's own
Sparkplug B 3.0.0 stack, behind the [container contract](../../docs/CONTAINER_CONTRACT.md),
so that other stacks can test against it. It has both roles: the edge node is
the library's `edge.Node`, and the host application is its `host.App`, the
primary host of `--host-id`. Anything that uses a `sparkplugb-interop-<adapter>`
image can use this one in its place, on the same brokers. Unlike those, it is
not a reference: its tags are go-sparkplugb versions, and no harness release or
`manifest.json` contains it.

**For testing only.** It connects to a broker over plain MQTT without
authentication, as the contract does. Old images stay available, so check
[go-sparkplugb advisories](https://github.com/otfabric/go-sparkplugb/security/advisories)
for the version you pull.

**Not an independent implementation.** The image declares
`upstream.independentEngine: false`, and `results.json` records
`independent: false`. A result against it says nothing independent about
go-sparkplugb when OT Fabric produces it. A result you publish, naming the
implementation and version, is independent evidence, and we would like to hear
about it.

## Use

```sh
docker network create spb
docker run -d --name broker --network spb eclipse-mosquitto:2.1.2-alpine mosquitto -c /mosquitto-no-auth.conf
docker run -d --name go-host --network spb ghcr.io/otfabric/sparkplugb-interop-otfabric:<tag> \
    host --broker tcp://broker:1883 --host-id my-host
docker run -d --name go-edge --network spb ghcr.io/otfabric/sparkplugb-interop-otfabric:<tag> \
    edge --broker tcp://broker:1883 --fixture baseline --primary-host my-host
docker exec go-edge sparkplugb-interop call publish-data --payload '{"update": 0}'
docker logs go-host    # the messages the host received, as JSON Lines
```

`<tag>` is a go-sparkplugb version such as `v0.3.4`, or `latest`, the newest one
published. A rebuild of a version replaces its tag, so pin the digest if you need
an exact build. The digest is in the summary of the
[workflow run](https://github.com/otfabric/sparkplugb-interop/actions/workflows/own-go-sparkplugb.yml),
and `docker run --rm <image> version` prints the versions and the commit.

| | |
|---|---|
| Upstream | go-sparkplugb, the latest release when the image was built |
| Licence | MIT; every module's licence is in the image under `/licenses` |
| Adapter name | `otfabric` (the name in documents, as the schema allows) |
| Language | Go |
| Protocol engine | OT Fabric's own |
| Sparkplug B | 3.0.0, over MQTT 3.1.1 (Eclipse Paho, the library's transport) |

The edge node publishes the births of the fixture when the library reports it
online: at once, or with `--primary-host` when the host's STATE is online. Data
messages and the reports of writes carry aliases only. The library answers an
NCMD rebirth itself, from its own record of the current values, and on the
primary host's offline STATE it publishes NDEATH, disconnects and connects anew
with the next bdSeq. The adapter learns what the library published, and the
commands and STATE it received, through the transport it hands the library.

The host reports each message from the library's callbacks, after the library
applied it: names resolved from aliases and datatypes taken from the births by
the library (`host.App.GetNode`), `seq` and `bdSeq` from the state it keeps.
The library holds messages that a broker delivers out of order and asks for a
rebirth on a gap or an unknown node; its warning for that request is the
`rebirth-requested` event, as the library has no callback for it.

## Why it is apart from the other adapters

`REQUIREMENTS.md` R1: nothing under `adapters/`, `fixtures/`, `schemas/`,
`scripts/`, `tests/`, the top-level `Makefile` or `compose.yaml` knows a library
under test, and CI enforces that. This directory and
`.github/workflows/own-go-sparkplugb.yml` are the one exception. The scripts take
the image through `ADAPTER_IMAGE_otfabric`, and `scripts/interop.sh` runs only
the pairings with it when it is among the adapters.

No release loop can form (`otfabric/engineering` ADR-0012). This image is built
by hand, from the latest go-sparkplugb release, and nothing is committed: this
repository has no pin of go-sparkplugb to bump. No release or required check of
go-sparkplugb needs the image, and go-sparkplugb commits no pin of it. It is not
in a harness release or `manifest.json`.

## Publishing

Run the **Own stack (go-sparkplugb)** workflow by hand, from `main`. It finds the
latest go-sparkplugb release and the latest harness release, and prints them. It
builds the image, runs the smoke test and the cross-stack self-test against
`tahu`, `pysparkplug`, `sparkplugnet`, `srad`, `tahu-js` and `sparkplughost` on
Mosquitto, on both architectures, then publishes `:<go-sparkplugb version>` and
`:latest`. Run from a branch, it stops before publishing. Nothing runs it
automatically, so after a go-sparkplugb release the image stays on the old
version until someone does.

```sh
make -f own/go-sparkplugb/Makefile image smoke interop    # from the repository root
```

## What it cannot do

Every capability of the contract is declared `true`. What go-sparkplugb offers
beyond the contract is not in the image:

- `app.App`, an application that is not the primary host (no STATE): the
  contract has no such role.
- Several brokers (`ServerURLs`), TLS, user name and password, and a persistent
  `BdSeqStore`: the contract has one plain broker and no credentials, so the
  image keeps bdSeq in memory and starts every run at 1.
- A `ReorderTimeout`: the host uses the library's default (none), so a gap is a
  rebirth request at once, after the library held the message.
