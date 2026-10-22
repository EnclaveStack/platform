// Package api is the wire contract of the EnclaveStack control plane (L3-002): request and response
// types, kinds, phases, error codes and validation rules. It is the single definition shared by the
// server, the Go SDK, the CLI and the OpenTofu provider, and the source the console's generated
// types are produced from (cmd/gencontracts). Change it together with L3-002 and api/openapi.yaml.
package api
