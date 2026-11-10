// Package contract holds the Go types generated from contracts/*.schema.json, shared by the coordinator and the executor
// (the gateway follows in wave 2). The generated files are committed; CI fails when go generate changes them.
package contract

//go:generate go run ../gen ../../contracts/host-protocol.schema.json contract host_protocol_gen.go
//go:generate go run ../gen ../../contracts/profile.schema.json contract profile_gen.go
