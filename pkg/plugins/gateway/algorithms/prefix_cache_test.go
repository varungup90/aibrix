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
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/metrics"
	"github.com/vllm-project/aibrix/pkg/plugins/gateway/algorithms/prefixcacheindexer"
	"github.com/vllm-project/aibrix/pkg/plugins/gateway/algorithms/tokenizer"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_PrefixCache(t *testing.T) {
	prefixCacheRouter := prefixCacheRouter{
		cache:              &cache.Store{},
		tokenizer:          tokenizer.NewCharacterTokenizer(),
		prefixCacheIndexer: prefixcacheindexer.NewPrefixHashTable(),
	}

	pods := map[string]*v1.Pod{
		"p1": {
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Status: v1.PodStatus{
				PodIP: "1.1.1.1",
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			}},
		"p2": {
			ObjectMeta: metav1.ObjectMeta{Name: "p2"},
			Status: v1.PodStatus{
				PodIP: "2.2.2.2",
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			}},
	}

	targetPod, err := prefixCacheRouter.Route(context.Background(), pods, RoutingContext{Model: "m1", Message: "Hello World! What a Good Day! Good day to code and learn new things in LLM!! 你好世界！ 多么美好的一天啊！"})
	assert.NoError(t, err)

	targetPod2, err := prefixCacheRouter.Route(context.Background(), pods, RoutingContext{Model: "m1", Message: "Hello World! What a Good Day! Good day to code and learn new things in LLM!!"})
	assert.NoError(t, err)

	assert.Equal(t, targetPod, targetPod2)
}

func Test_PrefixCacheE2E(t *testing.T) {
	pod1, pod2, pod3, pod4, model1 := "p1", "p2", "p3", "p4", "m1"
	cache := &cache.Store{
		PodModelMetrics: map[string]map[string]map[string]metrics.MetricValue{
			pod1: {model1: map[string]metrics.MetricValue{}},
			pod2: {model1: map[string]metrics.MetricValue{}},
			pod3: {model1: map[string]metrics.MetricValue{}},
			pod4: {model1: map[string]metrics.MetricValue{}},
		},
	}
	prefixCacheRouter := prefixCacheRouter{
		cache:              cache,
		tokenizer:          tokenizer.NewCharacterTokenizer(),
		prefixCacheIndexer: prefixcacheindexer.NewPrefixHashTable(),
	}

	os.Setenv("AIBRIX_PREFIX_CACHE_BLOCK_SIZE", "4")
	// no prefix match -> select least request pod
	// input: abcdegfh
	// pre_request_count: [p1: 0, p2: 0, p3: 0, p4: 0]
	// post_request_count: [p1: 0, p2: 0, p3: 0, p4: 1(abcdefgh)]
	p4, err := prefixCacheRouter.Route(context.Background(), getReadyPods(),
		RoutingContext{Model: model1, Message: "abcdefgh"})
	assert.NoError(t, err)
	cache.PodModelMetrics[getNameFromIP(p4)] = incrCount(1) // increase running req count
	fmt.Println(p4)

	// no prefix match -> select least request pod
	// input: wxyz
	// pre_request_count: [p1: 0, p2: 0, p3: 0, p4: 1(abcdefgh)]
	// post_request_count: [p1: 0, p2: 0, p3: 1 (wxyz), p4: 1(abcdefgh)]
	p3, err := prefixCacheRouter.Route(context.Background(), getReadyPods(),
		RoutingContext{Model: model1, Message: "wxyz"})
	assert.NoError(t, err)
	assert.NotEqual(t, p4, p3)
	cache.PodModelMetrics[getNameFromIP(p3)] = incrCount(1)
	fmt.Println(p3)

	// prefix match, load balanced -> select cached pod
	// input: abcdefgh
	// pre_request_count: [p1: 0, p2: 0, p3: 1 (wxyz), p4: 1(abcdefgh)]
	// post_request_count: [p1: 0, p2: 0, p3: 1 (wxyz), p4: 2(abcdefgh)]
	targetPod, err := prefixCacheRouter.Route(context.Background(), getReadyPods(),
		RoutingContext{Model: model1, Message: "abcdefgh"})
	assert.NoError(t, err)
	cache.PodModelMetrics[getNameFromIP(targetPod)] = incrCount(2)
	assert.Equal(t, p4, targetPod)
	fmt.Println(targetPod)

	// prefix match, load unbalanced -> select least request pod
	// input: abcd
	// pre_request_count: [p1: 0, p2: 0, p3: 1 (wxyz), p4: 1(abcdefgh)]
	// post_request_count: [p1: 0, p2: 1 (abcd), p3: 1 (wxyz), p4: 2(abcdefgh)]
	p2, err := prefixCacheRouter.Route(context.Background(), getReadyPods(),
		RoutingContext{Model: model1, Message: "abcd"})
	assert.NoError(t, err)
	cache.PodModelMetrics[getNameFromIP(p2)] = incrCount(1)
	assert.NotEqual(t, p4, p2)
	fmt.Println(p2)

	// prefix match, load unbalanced -> selects p2 with lower prefix match
	// input: abcdefghijkl
	// pre_request_count: [p1: 0, p2: 1 (abcd), p3: 1 (wxyz), p4: 2 (abcdefgh)]
	// post_request_count: [p1: 0, p2: 2 (abcdefghijkl), p3: 1 (wxyz), p4: 2(abcdefgh)]
	targetPod, err = prefixCacheRouter.Route(context.Background(), getReadyPods(),
		RoutingContext{Model: model1, Message: "abcdefghijkl"})
	assert.NoError(t, err)
	cache.PodModelMetrics[getNameFromIP(targetPod)] = incrCount(2)
	assert.Equal(t, p2, targetPod)
	fmt.Println(targetPod)

	// prefix match, load balanced -> selects p2 or p3
	// input: abcdefgh
	// pre_request_count: [p1: 0, p2: 2 (abcdefghijkl), p3: 1 (wxyz), p4: 2(abcdefgh)]
	// post_request_count: [p1: 0, p2: 3 (abcdefghijkl), p3: 1 (wxyz), p4: 2(abcdefgh)]
	targetPod, err = prefixCacheRouter.Route(context.Background(), getReadyPods(),
		RoutingContext{Model: model1, Message: "abcdefgh"})
	assert.NoError(t, err)
	assert.True(t, slices.Contains([]string{p2, p4}, targetPod))
	fmt.Println(targetPod)
}

func getReadyPods() map[string]*v1.Pod {
	return map[string]*v1.Pod{
		"p1": {
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Status: v1.PodStatus{
				PodIP: "p1",
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			}},
		"p2": {
			ObjectMeta: metav1.ObjectMeta{Name: "p2"},
			Status: v1.PodStatus{
				PodIP: "p2",
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			}},
		"p3": {
			ObjectMeta: metav1.ObjectMeta{Name: "p3"},
			Status: v1.PodStatus{
				PodIP: "p3",
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			}},
		"p4": {
			ObjectMeta: metav1.ObjectMeta{Name: "p4"},
			Status: v1.PodStatus{
				PodIP: "p4",
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			}},
	}
}

func getNameFromIP(targetPodAddress string) string {
	return targetPodAddress[:len(targetPodAddress)-5]
}

func incrCount(cnt float64) map[string]map[string]metrics.MetricValue {
	return map[string]map[string]metrics.MetricValue{
		"m1": {
			metrics.NumRequestsRunning: &metrics.SimpleMetricValue{Value: cnt},
		},
	}
}
