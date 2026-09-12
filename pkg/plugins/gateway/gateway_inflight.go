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
	"errors"
	"fmt"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	envoyTypePb "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/vllm-project/aibrix/pkg/types"
)

// errReplicaInflightExceeded is returned by selectTargetPod when every routable
// replica is already at its config profile's requestsInflight cap.
var errReplicaInflightExceeded = errors.New("all replicas have reached inflight limit")

func replicaInflightLimit(routingCtx *types.RoutingContext) int64 {
	if routingCtx == nil || routingCtx.ConfigProfile == nil {
		return 0
	}
	return routingCtx.ConfigProfile.RequestsInflight
}

// enforceReplicaInflight checks whether the already-selected target pod still has
// capacity for one more concurrent request under its config profile's requestsInflight cap.
//
// The count comes from the same cross-gateway realtime running-request signal used
// elsewhere for routing (metrics.RealtimeNumRequestsRunning via getRunningRequestsByPod),
// so this is a read-only admission check: every routed request already updates that
// counter via the normal request-tracking path regardless of this cap, and a rejected
// request never touched it in the first place.
func (s *Server) enforceReplicaInflight(ctx context.Context, model string, routingCtx *types.RoutingContext) *extProcPb.ProcessingResponse {
	limit := replicaInflightLimit(routingCtx)
	if limit <= 0 {
		return nil
	}
	if routingCtx == nil || !routingCtx.HasRouted() || routingCtx.TargetPod() == nil {
		return nil
	}
	pod := routingCtx.TargetPod()
	running := int64(getRunningRequestsByPod(s, pod.Name, pod.Namespace))
	if running >= limit {
		klog.InfoS("replica_inflight_exceeded", "requestID", routingCtx.RequestID, "model", model,
			"targetPod", pod.Name, "running", running, "limit", limit, "reason", "replica_at_capacity")
		return replicaInflightExceededResponse(model, limit)
	}
	return nil
}

func replicaInflightExceededResponse(model string, limit int64) *extProcPb.ProcessingResponse {
	return buildErrorResponseWithType(envoyTypePb.StatusCode_TooManyRequests,
		fmt.Sprintf("model: %v has exceeded replica inflight limit: %v", model, limit),
		ErrorTypeOverloaded, ErrorCodeReplicaInflightExceeded, "",
		HeaderErrorReplicaInflightExceeded, "true")
}

// filterSaturatedReplicaInflight drops pods whose current running-request count is
// already at the cap. A pod missing a metric value (not yet scraped) is kept so
// enforceReplicaInflight can fail-open on it later.
func (s *Server) filterSaturatedReplicaInflight(pods []*v1.Pod, limit int64) []*v1.Pod {
	if limit <= 0 || len(pods) == 0 {
		return pods
	}
	kept := make([]*v1.Pod, 0, len(pods))
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		running := int64(getRunningRequestsByPod(s, pod.Name, pod.Namespace))
		if running < limit {
			kept = append(kept, pod)
		}
	}
	return kept
}
