// Package contracttest validates producer JSON against the public schemas.
package contracttest

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func Validator(t *testing.T, name string) func([]byte) error {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	schema, err := compiler.Compile(filepath.Join("..", "..", "docs", "contracts", name+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return func(payload []byte) error {
		decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		if err != nil {
			return err
		}
		return schema.Validate(decoded)
	}
}
