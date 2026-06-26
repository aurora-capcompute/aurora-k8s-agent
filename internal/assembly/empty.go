package assembly

import (
	"context"

	"aurora-capcompute/aurora"
)

// EmptyProvider is a brain provider with no brains. The agent boots with it when
// no brains are configured at startup (no AURORA_BRAINS); brains are then
// supplied at runtime — typically by Brain CRDs through the control plane, which
// hot-loads them via runtime.SetBrains. With no brain registered, chat/API works
// but brain runs fail with a clear "no brain registered" error until one exists.
type EmptyProvider struct{}

func (EmptyProvider) DefaultID() string { return "" }

func (EmptyProvider) List(context.Context) ([]aurora.BrainSource, error) { return nil, nil }
