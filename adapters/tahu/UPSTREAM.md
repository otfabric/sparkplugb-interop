# Eclipse Tahu upstream reports

What we reported to [Eclipse Tahu](https://github.com/eclipse-tahu/tahu), found by this adapter and `tahu-js` against Tahu 1.0.21 and sparkplug-payload 1.0.3. Findings are in [docs/COMPATIBILITY.md](../../docs/COMPATIBILITY.md). When a fix is released, update the pin in `versions.yaml` and drop the matching lines of `tests/known-bugs.tsv`.

| Finding | Issue | Proposed fix |
|---|---|---|
| A Template addressed by alias is left out of the payload (Java host) | [#465](https://github.com/eclipse-tahu/tahu/issues/465) | [#466](https://github.com/eclipse-tahu/tahu/pull/466) |
| Int8 and Int16: DataSet cells and template parameters fail to decode, width-sized values read as positive (sparkplug-payload) | [#464](https://github.com/eclipse-tahu/tahu/issues/464) | none, waits for [sparkplug#453](https://github.com/eclipse-sparkplug/sparkplug/issues/453) |
| UInt32 is encoded in `long_value` | [#397](https://github.com/eclipse-tahu/tahu/issues/397), not ours | none |

Not reported: the host rejects a DBIRTH that reuses aliases.
