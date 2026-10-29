//go:build faultinject && fault_health_data_as_list

package faultinject

// The health check answers its data object as an empty list.
func init() {
	register(Fault{
		Name:    "health_data_as_list",
		Method:  "GET",
		Path:    "/api/health",
		Break:   replaceField("data", []any{}),
		Example: `{"data":{"status":"ok"}}`,
	})
}
