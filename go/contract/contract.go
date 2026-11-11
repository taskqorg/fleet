// Package contract holds the Go types generated from contracts/*.schema.json, shared by the coordinator, the executor
// and the gateway. The generated files are committed; CI fails when go generate changes them.
package contract

//go:generate go run ../gen ../../contracts/host-protocol.schema.json contract host_protocol_gen.go
//go:generate go run ../gen ../../contracts/gateway.schema.json contract gateway_gen.go
//go:generate go run ../gen ../../contracts/ops.schema.json contract ops_gen.go
//go:generate go run ../gen ../../contracts/ops-accounts.schema.json contract ops_accounts_gen.go
//go:generate go run ../gen ../../contracts/ops-artifacts.schema.json contract ops_artifacts_gen.go
//go:generate go run ../gen ../../contracts/ops-definitions.schema.json contract ops_definitions_gen.go
//go:generate go run ../gen ../../contracts/ops-executions.schema.json contract ops_executions_gen.go
//go:generate go run ../gen ../../contracts/ops-lab.schema.json contract ops_lab_gen.go
//go:generate go run ../gen ../../contracts/ops-people.schema.json contract ops_people_gen.go
//go:generate go run ../gen ../../contracts/ops-sessions.schema.json contract ops_sessions_gen.go
//go:generate go run ../gen ../../contracts/ops-tasks.schema.json contract ops_tasks_gen.go
//go:generate go run ../gen ../../contracts/ops-telemetry.schema.json contract ops_telemetry_gen.go
//go:generate go run ../gen ../../contracts/profile.schema.json contract profile_gen.go

// Operation is one entry of an "operations" list in contracts/ops-<domain>.schema.json: a read snapshot or a write, long (no surrounding
// transaction), and the status codes its handler returns besides 401, 403 and 500.
type Operation struct {
	Read, Long bool
	Errors     []int
}

// Operations: every coordinator operation; each name has <Name>Request and <Name>Reply. The generated init of each
// ops-<domain> file fills it.
var Operations = map[string]Operation{}

func addOperation(name string, o Operation) {
	if _, ok := Operations[name]; ok {
		panic("contract: operation " + name + " is in two ops-<domain>.schema.json files")
	}
	Operations[name] = o
}
