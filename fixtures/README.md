# Sparkplug B Test Fixtures

Reference payloads and scenarios for interoperability testing.

## Payload fixtures

Each `.json` file describes a Sparkplug B message with its expected topic,
QoS, retained flag, and Protobuf-decoded payload contents.

## Scenario fixtures

Lifecycle scenario files define ordered sequences of messages that exercise
complete Edge Node and Host Application workflows.

## Usage

Consuming libraries load these fixtures as test data. The fixtures are
implementation-independent and contain no OT Fabric library code.
