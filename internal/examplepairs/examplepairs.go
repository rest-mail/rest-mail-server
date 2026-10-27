// Package examplepairs gives the in-process tests every example pair they prove:
// the description's, from openapi-example.json, and the credential and condition
// variants of those responses kept out of the description, in example-matrix.json
// (#1236). Only tests import it, so the matrix never reaches the server binary.
package examplepairs

import (
	_ "embed"
	"encoding/json"
	"fmt"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

//go:embed example-matrix.json
var matrix []byte

// Read is the description's pairs with the matrix's beside them, by operationId and
// name. A name in both is an error: it would be two exchanges under one name.
func Read() (oapifly.ExampleFile, error) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		return nil, err
	}
	var variants oapifly.ExampleFile
	if err := json.Unmarshal(matrix, &variants); err != nil {
		return nil, fmt.Errorf("example-matrix.json: %w", err)
	}
	for op, named := range variants {
		if pairs[op] == nil {
			pairs[op] = map[string]oapifly.ExamplePair{}
		}
		for name, pair := range named {
			if _, both := pairs[op][name]; both {
				return nil, fmt.Errorf("example %s of %s is in openapi-example.json and example-matrix.json", name, op)
			}
			pairs[op][name] = pair
		}
	}
	return pairs, nil
}
