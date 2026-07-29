// Package server runs the ts-skills registry daemon.
//
// One package owns the daemon; files group features. Interfaces belong here
// only when production has two implementations. The exported surface is the
// small set of entry points used by cmd/ts-skillsd.
package server
