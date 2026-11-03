//go:build faultinject && fault_accounts_list_null

package faultinject

// The account list answers its data list as null, as a nil Go slice would.
func init() {
	register(Fault{
		Name:    "accounts_list_null",
		Method:  "GET",
		Path:    "/api/v1/accounts",
		Break:   replaceField("data", nil),
		Example: `{"data":[{"id":1,"address":"admin@restmail.test","display_name":"Admin","is_primary":true}]}`,
	})
}
