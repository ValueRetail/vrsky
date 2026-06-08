package main

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// storeFactory opens an ObjectStore for a resolved config. Defaulted to
// objectstore.New in Configure; tests inject a fake so the poller runs without a
// live bucket (no Docker).
type storeFactory func(ctx context.Context, cfg *objectstore.Config) (objectstore.ObjectStore, error)
