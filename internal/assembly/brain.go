package assembly

import (
	"context"
	_ "embed"

	"aurora-capcompute/aurora"
)

//go:embed kubernetes-agent.wasm
var brainWasm []byte

type BrainProvider struct{}

func (BrainProvider) DefaultID() string { return "kubernetes-agent" }

func (BrainProvider) List(context.Context) ([]aurora.BrainSource, error) {
	return []aurora.BrainSource{{
		ID: "kubernetes-agent", Wasm: append([]byte(nil), brainWasm...),
	}}, nil
}
