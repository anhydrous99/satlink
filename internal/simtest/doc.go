// Package simtest is test support for the tests of satnet, mobile and
// cmd/satlink, which drive the simulated handset of package sim:
//   - simulated handsets that close themselves when the test ends
//     ([NewSimulator]), with a short escape guard if wanted
//     ([SimulatorConfig]), and initialised satlink handsets over them
//     ([NewHandset], [InitializedHandset]);
//   - [FaultyTransport], a transport that fails on cue: deaf to the escape
//     or to DTR, panicking, unplugged, losing a hang-up, without +CPAS —
//     the Rust tests' Faulty, StubbornUsb and NoDtrUsb transports in one;
//   - [ByteTransport], a mobile.ByteTransport over any satlink.Transport,
//     standing in for the host app's USB transport;
//   - [Eventually], which waits for a condition instead of sleeping for a
//     guess.
//
// Only tests import it. The sim package's internal tests cannot, as it
// imports sim; and it imports neither satnet nor mobile, so that their
// tests can use it: ByteTransport satisfies mobile.ByteTransport by its
// methods alone.
package simtest
