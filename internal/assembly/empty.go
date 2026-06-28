package assembly

import (
	"context"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
)

// EmptyProvider is a brain provider with no brains. The agent always boots with
// it; brains are supplied at runtime by Brain CRDs through the control plane,
// which hot-loads them via runtime.SetBrains. With no brain registered,
// chat/API works but brain runs fail with a clear "no brain registered" error
// until the control plane delivers one.
type EmptyProvider struct{}

func (EmptyProvider) DefaultID() string { return "" }

func (EmptyProvider) List(context.Context) ([]aurora.BrainSource, error) { return nil, nil }
