/*
Copyright 2024 The Aibrix Team.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gateway

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vllm-project/aibrix/pkg/types"
)

// stubSyncer counts Incr and Decr calls for unit tests, with no Redis dependency.
type stubSyncer struct {
	incrCalls atomic.Int64
	decrCalls atomic.Int64
}

func (s *stubSyncer) Init(_ context.Context, _ string) error { return nil }

func (s *stubSyncer) Incr(_ context.Context, _ string) (int64, error) {
	return s.incrCalls.Add(1), nil
}

func (s *stubSyncer) Decr(_ context.Context, _ string) (int64, error) {
	s.decrCalls.Add(1)
	return s.incrCalls.Load() - s.decrCalls.Load(), nil
}

func (s *stubSyncer) Total(_ context.Context) (int64, error) {
	return s.incrCalls.Load() - s.decrCalls.Load(), nil
}

func newTestRoutingContext() *types.RoutingContext {
	return types.NewRoutingContext(context.Background(), "test-algo", "model", "", "req-1", "")
}

func TestGatewayRequestTracker_AddThenDone(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)
	ctx := newTestRoutingContext()

	tracker.AddRequestCount(ctx, "req-1", "model")
	assert.Equal(t, int64(1), syncer.incrCalls.Load())
	assert.Equal(t, int64(0), syncer.decrCalls.Load())

	tracker.DoneRequestCount(ctx, "req-1", "model", 0)
	assert.Equal(t, int64(1), syncer.incrCalls.Load())
	assert.Equal(t, int64(1), syncer.decrCalls.Load())
}

func TestGatewayRequestTracker_AddIsIdempotent(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)
	ctx := newTestRoutingContext()

	tracker.AddRequestCount(ctx, "req-1", "model")
	tracker.AddRequestCount(ctx, "req-1", "model") // second call must be a no-op
	tracker.AddRequestCount(ctx, "req-1", "model") // third call must be a no-op

	assert.Equal(t, int64(1), syncer.incrCalls.Load())
}

func TestGatewayRequestTracker_DoneIsIdempotent(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)
	ctx := newTestRoutingContext()

	tracker.AddRequestCount(ctx, "req-1", "model")
	tracker.DoneRequestCount(ctx, "req-1", "model", 0)
	tracker.DoneRequestCount(ctx, "req-1", "model", 0) // duplicate must be a no-op

	assert.Equal(t, int64(1), syncer.decrCalls.Load())
}

func TestGatewayRequestTracker_DoneWithoutAddIsNoOp(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)
	ctx := newTestRoutingContext()

	// DoneRequestCount before AddRequestCount should not decrement.
	tracker.DoneRequestCount(ctx, "req-1", "model", 0)
	assert.Equal(t, int64(0), syncer.decrCalls.Load())
}

func TestGatewayRequestTracker_DoneRequestTraceDecrementsOnce(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)
	ctx := newTestRoutingContext()

	tracker.AddRequestCount(ctx, "req-1", "model")
	tracker.DoneRequestTrace(ctx, "req-1", "model", 10, 20, 0)
	tracker.DoneRequestTrace(ctx, "req-1", "model", 10, 20, 0) // duplicate

	assert.Equal(t, int64(1), syncer.incrCalls.Load())
	assert.Equal(t, int64(1), syncer.decrCalls.Load())
}

func TestGatewayRequestTracker_NilContextIsNoOp(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)

	tracker.AddRequestCount(nil, "req-1", "model")
	tracker.DoneRequestCount(nil, "req-1", "model", 0)

	assert.Equal(t, int64(0), syncer.incrCalls.Load())
	assert.Equal(t, int64(0), syncer.decrCalls.Load())
}

func TestGatewayRequestTracker_DoneCountAndTraceBothGuardedBySameDoneKey(t *testing.T) {
	syncer := &stubSyncer{}
	tracker := newGatewayRequestTracker("gw-1", syncer)
	ctx := newTestRoutingContext()

	tracker.AddRequestCount(ctx, "req-1", "model")
	// Calling DoneRequestCount first marks done.
	tracker.DoneRequestCount(ctx, "req-1", "model", 0)
	// A subsequent DoneRequestTrace must be a no-op.
	tracker.DoneRequestTrace(ctx, "req-1", "model", 5, 10, 0)

	assert.Equal(t, int64(1), syncer.decrCalls.Load())
}
