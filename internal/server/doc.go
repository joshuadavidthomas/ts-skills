// Package server composes and runs the ts-skills registry daemon.
//
// Catalog persistence, the machine protocol, and the browser UI live in
// focused subpackages. This package owns process lifecycle, tsnet, and the
// small exported entry-point surface used by cmd/ts-skillsd.
package server
