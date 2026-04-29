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

	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/plugins/gateway/ratelimiter"
	"github.com/vllm-project/aibrix/pkg/types"
	"k8s.io/klog/v2"
)

// context key types – unexported so they cannot collide with keys from other packages.
type gwTrackerAddedKeyType struct{}
type gwTrackerDoneKeyType struct{}

var gwTrackerAddedKey = gwTrackerAddedKeyType{}
var gwTrackerDoneKey = gwTrackerDoneKeyType{}

// gatewayRequestTracker implements cache.RequestTracker.  It publishes the
// running request count for this gateway instance to Redis on every request
// start and completion, enabling any peer to compute the cluster-wide total.
type gatewayRequestTracker struct {
	gatewayID string
	syncer    ratelimiter.RunningRequestSyncer
}

// newGatewayRequestTracker returns a RequestTracker that mirrors running
// request counts for gatewayID into Redis via syncer.
func newGatewayRequestTracker(gatewayID string, syncer ratelimiter.RunningRequestSyncer) cache.RequestTracker {
	return &gatewayRequestTracker{gatewayID: gatewayID, syncer: syncer}
}

// AddRequestCount increments this gateway's running request count in Redis.
// It is idempotent: multiple calls for the same request are no-ops after the first.
func (t *gatewayRequestTracker) AddRequestCount(ctx *types.RoutingContext, requestID string, _ string) (traceTerm int64) {
	if ctx == nil || ctx.Value(gwTrackerAddedKey) != nil {
		return 0
	}
	ctx.Context = context.WithValue(ctx.Context, gwTrackerAddedKey, true)

	if _, err := t.syncer.Incr(ctx.Context, t.gatewayID); err != nil {
		klog.ErrorS(err, "failed to increment gateway running request count",
			"request_id", requestID, "gateway_id", t.gatewayID)
	}
	return 0
}

// DoneRequestCount decrements this gateway's running request count in Redis.
// It is idempotent and only acts if AddRequestCount was called for the same request.
func (t *gatewayRequestTracker) DoneRequestCount(ctx *types.RoutingContext, requestID string, _ string, _ int64) {
	t.done(ctx, requestID)
}

// DoneRequestTrace decrements this gateway's running request count in Redis.
// Token counts are not used; the decrement mirrors DoneRequestCount.
func (t *gatewayRequestTracker) DoneRequestTrace(ctx *types.RoutingContext, requestID string, _ string, _, _, _ int64) {
	t.done(ctx, requestID)
}

func (t *gatewayRequestTracker) done(ctx *types.RoutingContext, requestID string) {
	if ctx == nil || ctx.Value(gwTrackerAddedKey) == nil || ctx.Value(gwTrackerDoneKey) != nil {
		return
	}
	ctx.Context = context.WithValue(ctx.Context, gwTrackerDoneKey, true)

	if _, err := t.syncer.Decr(ctx.Context, t.gatewayID); err != nil {
		klog.ErrorS(err, "failed to decrement gateway running request count",
			"request_id", requestID, "gateway_id", t.gatewayID)
	}
}

var _ cache.RequestTracker = (*gatewayRequestTracker)(nil)
