// Package contracts embeds the copies of contracts/schemas that the daemon validates against.
// The copies are generated: run `make generate` at the repository root, never edit them here.
package contracts

import _ "embed"

//go:embed task.schema.json
var TaskSchema []byte

//go:embed event.schema.json
var EventSchema []byte

//go:embed execution-report.schema.json
var ExecutionReportSchema []byte

const (
	TaskSchemaURL   = "https://harmflow.com/schemas/v1alpha1/task.schema.json"
	EventSchemaURL  = "https://harmflow.com/schemas/v1alpha1/event.schema.json"
	ReportSchemaURL = "https://harmflow.com/schemas/v1alpha1/execution-report.schema.json"
)
