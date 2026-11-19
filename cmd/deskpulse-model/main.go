// Command deskpulse-model runs one explicitly approved request for a native
// host app. Input is bounded JSON over stdin, including a key held in memory.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
)

type response struct {
	Report *diagnose.Report `json:"report,omitempty"`
	Error  string           `json:"error,omitempty"`
}

func run(in io.Reader) response {
	const maxInput = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(in, maxInput+1))
	if err != nil || len(raw) > maxInput {
		return response{Error: "invalid_input"}
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var req diagnose.PortableRequest
	var extra any
	if decoder.Decode(&req) != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		return response{Error: "invalid_input"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	report, err := diagnose.AnalyzePortable(ctx, req)
	if err != nil {
		return response{Error: diagnose.CodeOf(err)}
	}
	return response{Report: &report}
}

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(run(os.Stdin)); err != nil {
		os.Exit(1)
	}
}
