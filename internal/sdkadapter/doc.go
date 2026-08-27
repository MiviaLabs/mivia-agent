// Package sdkadapter bridges CLI types and SDK types for the SDK convergence
// work tracked at internal/sdkadapter. Each bridge file in this package maps a
// pair of types that share an intent but differ in field shape: the CLI shapes
// here come from internal/{provider,tools,skills,hooks,contentref,reasoning,
// ledger}; the SDK shapes come from github.com/MiviaLabs/mivia-ai-sdk.
//
// This package is the only seam between the CLI runtime and the SDK. It is
// permitted to import CLI packages and it is permitted to import SDK
// packages, but nothing else in the tree may import both: doing so would let a
// shape drift propagate. The .mivia/policy/import-layers.json row for
// internal/sdkadapter is the contract that pins this seam.
//
// Each bridge file has a companion <name>_test.go whose table of tests is
// the round-trip surface: convert CLI -> SDK -> CLI (or write a struct,
// read its bridge output, and assert the key fields). New behaviour that
// gains its own test must NOT be added here unless it lives in the
// corresponding bridge file; the round-trip tests are the proof that the
// bridge is shape-faithful.
package sdkadapter
