package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// outputJSON outputs data as pretty-printed JSON
func outputJSON(v interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// outputJSONError outputs an error as JSON to stderr and exits with code 1.
// Use this when jsonOutput is true and an error occurs, to ensure consistent
// machine-readable error output. The error is formatted as:
//
//	{"error": "error message", "code": "error_code"}
//
// The code parameter is optional (pass "" to omit).
func outputJSONError(err error, code string) {
	errObj := map[string]string{"error": err.Error()}
	if code != "" {
		errObj["code"] = code
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(errObj)
	os.Exit(1)
}
