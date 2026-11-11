// Package contract holds the Go types generated from contracts/*.schema.json, shared by the coordinator, the executor
// and the gateway. The generated files are committed; CI fails when go generate changes them.
package contract

//go:generate go run ../gen ../../contracts/host-protocol.schema.json contract host_protocol_gen.go
//go:generate go run ../gen ../../contracts/gateway.schema.json contract gateway_gen.go
