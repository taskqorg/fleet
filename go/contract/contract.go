// Package contract holds the Go types generated from contracts/*.schema.json, shared by the coordinator, the executor
// and the gateway. The generated files are committed; CI fails when go generate changes them.
package contract

//go:generate go run ../gen ../../contracts/host-protocol.schema.json contract host_protocol_gen.go
//go:generate go run ../gen ../../contracts/gateway.schema.json contract gateway_gen.go
//go:generate go run ../gen ../../contracts/ops.schema.json contract ops_gen.go
//go:generate go run ../gen ../../contracts/profile.schema.json contract profile_gen.go

// Operation is one entry of contracts/ops.schema.json "operations": a read snapshot or a write, long (no surrounding
// transaction), and the status codes its handler returns besides 401, 403 and 500.
type Operation struct {
	Read, Long bool
	Errors     []int
}
