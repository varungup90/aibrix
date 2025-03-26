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

package routingalgorithms

import (
	"context"
	"fmt"
	"math"
	"math/rand"

	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/metrics"
	"github.com/vllm-project/aibrix/pkg/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

var (
	RouterLeastRequest Algorithms = "least-request"
)

func init() {
	router, err := NewLeastRequestRouter()
	Register(RouterLeastRequest, func() (Router, error) { return router, err })
}

type leastRequestRouter struct {
	cache cache.Cache
}

func NewLeastRequestRouter() (Router, error) {
	c, err := cache.Get()
	if err != nil {
		return nil, err
	}

	return leastRequestRouter{
		cache: c,
	}, nil
}

func (r leastRequestRouter) Route(ctx context.Context, pods map[string]*v1.Pod, routingCtx RoutingContext) (string, error) {

	if len(pods) == 0 {
		return "", fmt.Errorf("no pods to forward request")
	}

	readyPods := utils.FilterReadyPods(pods)
	if len(readyPods) == 0 {
		return "", fmt.Errorf("no ready pods available for fallback")
	}

	targetPodIP := selectTargetPodWithLeastRequestCount(r.cache, routingCtx.Model, readyPods)

	// Use fallback if no valid metrics
	if targetPodIP == "" {
		klog.Warning("No pods with valid metrics found; selecting a pod randomly as fallback")
		var err error
		targetPodIP, err = selectRandomPod(pods, rand.Intn)
		if err != nil {
			return "", err
		}
	}

	if targetPodIP == "" {
		return "", fmt.Errorf("no pods to forward request")
	}

	return targetPodIP + ":" + podMetricPort, nil
}

func (r *leastRequestRouter) SubscribedMetrics() []string {
	return []string{
		metrics.NumRequestsRunning,
		metrics.NumRequestsWaiting,
		metrics.NumRequestsSwapped,
	}
}

func selectTargetPodWithLeastRequestCount(cache cache.Cache, modelname string, readyPods []*v1.Pod) string {
	var targetPodIP string
	minCount := math.MaxInt32

	podRequestCount := getRequestCounts(cache, readyPods)
	for podname, totalReq := range podRequestCount {
		if totalReq <= minCount {
			minCount = totalReq
			targetPodIP = podname
		}
		klog.V(4).InfoS("total request count", "model", modelname, "pod", podname, "totalReq", totalReq)
	}

	return targetPodIP
}

func getRequestCounts(cache cache.Cache, readyPods []*v1.Pod) map[string]int {
	podRequestCount := map[string]int{}
	for _, pod := range readyPods {
		podname, _ := getPodAddress(pod.Status.PodIP)
		podRequestCount[pod.Status.PodIP] = int(cache.GetRunningRequest(podname))
	}

	return podRequestCount
}

func isLoadImbalanced(cache cache.Cache, readyPods []*v1.Pod) (bool, string) {
	podRequestCount := getRequestCounts(cache, readyPods)

	minValue := math.MaxInt32
	maxValue := math.MinInt32
	var minTargetPod string

	for podname, value := range podRequestCount {
		if value < minValue {
			minValue = value
			minTargetPod = podname
		}
		if value > maxValue {
			maxValue = value
		}
	}

	if maxValue-minValue > 8 {
		return true, minTargetPod
	}

	return false, ""
}
