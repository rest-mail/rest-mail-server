//go:build faultinject && fault_health_storage_ref_leaked

package faultinject

// The health check's data object carries a storage_ref its type does not
// declare, as a stored model serialized whole would.
func init() {
	register(Fault{
		Name:    "health_storage_ref_leaked",
		Method:  "GET",
		Path:    "/api/health",
		Break:   addDataField("storage_ref", "attachments/1/original"),
		Example: `{"data":{"db":"connected","status":"healthy"}}`,
	})
}
