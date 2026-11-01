# SparkplugNet upstream reports

What we reported to [SparkplugNet](https://github.com/SeppPenner/SparkplugNet), found by this adapter against version 1.4.1. Findings are in [docs/COMPATIBILITY.md](../../docs/COMPATIBILITY.md). When a fix is released, update the pin in `versions.yaml` and drop the matching lines of `tests/known-bugs.tsv`.

| Finding | Issue | Proposed fix |
|---|---|---|
| Int8 and Int16 in the width-sized form drop the payload | [#125](https://github.com/SeppPenner/SparkplugNet/issues/125) | none, waits for [sparkplug#453](https://github.com/eclipse-sparkplug/sparkplug/issues/453) |
| Dead nodes and devices stay online | [#126](https://github.com/SeppPenner/SparkplugNet/issues/126) | [#130](https://github.com/SeppPenner/SparkplugNet/pull/130) |
| Device writes are dropped | [#114](https://github.com/SeppPenner/SparkplugNet/issues/114), [our comment](https://github.com/SeppPenner/SparkplugNet/issues/114#issuecomment-6563350863) | [#131](https://github.com/SeppPenner/SparkplugNet/pull/131) |
| Metric properties are dropped on decode | [#127](https://github.com/SeppPenner/SparkplugNet/issues/127) | [#132](https://github.com/SeppPenner/SparkplugNet/pull/132) |
| Empty `template_ref` on a definition | [#128](https://github.com/SeppPenner/SparkplugNet/issues/128) | [#133](https://github.com/SeppPenner/SparkplugNet/pull/133) |
| UInt32 reads as 0 from the other protobuf field | [#129](https://github.com/SeppPenner/SparkplugNet/issues/129) | [#134](https://github.com/SeppPenner/SparkplugNet/pull/134) |

Not reported: a metric without a datatype throws, and `seq` in commands.
