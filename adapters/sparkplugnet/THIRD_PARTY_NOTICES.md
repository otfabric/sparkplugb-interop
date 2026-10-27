# Third-party notices: sparkplugb-interop-sparkplugnet

This image contains the sparkplugb-interop adapter (MIT, see
`/licenses/sparkplugb-interop/LICENSE`) and the following third-party
software, each under its own licence.

## SparkplugNet

- Upstream: https://github.com/SeppPenner/SparkplugNet (Hämmer Electronics)
- Version: 1.4.1, from NuGet with the content hash in
  `adapters/sparkplugnet/adapter/packages.lock.json`
- Licence: MIT, `/licenses/sparkplugnet/`
- Used unmodified as a library. It includes the Sparkplug B protobuf
  definitions as SparkplugNet generates and distributes them.

## NuGet packages

The dependencies of SparkplugNet, restored in locked mode with the content
hashes of `adapters/sparkplugnet/adapter/packages.lock.json`:

- MQTTnet 5.2.0.1603 (MIT)
- protobuf-net and protobuf-net.Core 3.3.21 (Apache-2.0)
- Microsoft.Extensions.Logging.Abstractions and
  Microsoft.Extensions.DependencyInjection.Abstractions 10.0.11 (MIT)

Their names and versions are in `/licenses/BUNDLED-MODULES.txt`. For each,
`/licenses/<name>/` holds the licence files the package ships and
`LICENSE-EXPRESSION`, the SPDX expression and project URL of its nuspec.

## .NET and Ubuntu

The base image `mcr.microsoft.com/dotnet/runtime:10.0` (pinned by digest)
carries the .NET runtime (MIT, `/usr/share/dotnet/LICENSE.txt` and
`ThirdPartyNotices.txt`) and Ubuntu packages under their own licences, listed
in `/usr/share/doc/*/copyright`.
