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
	"sort"

	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/plugins/gateway/algorithms/prefixcacheindexer"
	"github.com/vllm-project/aibrix/pkg/plugins/gateway/algorithms/tokenizer"
	"github.com/vllm-project/aibrix/pkg/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

var (
	RouterPrefixCache Algorithms = "prefix-cache"
)

func init() {
	router, err := NewPrefixCacheRouter()
	Register(RouterPrefixCache, func() (Router, error) { return router, err })
}

type prefixCacheRouter struct {
	cache              cache.Cache
	tokenizer          tokenizer.Tokenizer
	prefixCacheIndexer *prefixcacheindexer.PrefixHashTable
}

func NewPrefixCacheRouter() (Router, error) {
	var tokenizerObj tokenizer.Tokenizer
	// TODO: refactor initilization
	// supported tokenizers: ["character", "tiktoken"]
	tokenizerType := utils.LoadEnv("AIBRIX_PREFIX_CACHE_TOKENIZER_TYPE", "string")
	if tokenizerType == "tiktoken" {
		tokenizerObj = tokenizer.NewTiktokenTokenizer()
	} else {
		tokenizerObj = tokenizer.NewCharacterTokenizer()
	}

	// c, err := cache.Get()
	// if err != nil {
	// 	fmt.Println("fail to get cache in prefix cache router")
	// 	return nil, err
	// }

	return prefixCacheRouter{
		// cache:              c,
		tokenizer:          tokenizerObj,
		prefixCacheIndexer: prefixcacheindexer.NewPrefixHashTable(),
	}, nil
}

func (p prefixCacheRouter) Route(ctx context.Context, pods map[string]*v1.Pod, routingCtx RoutingContext) (string, error) {
	c, err := cache.Get()
	if err != nil {
		fmt.Println("fail to get cache in prefix cache router")
		return "nil", err
	}
	p.cache = c

	var targetPod string
	trimMessage := utils.TrimMessage(routingCtx.Message)
	// klog.InfoS("prefix_cache_trim_message",
	// 	"request_id", routingCtx.RequestID,
	// 	"trim_message", trimMessage)
	tokens, err := p.tokenizer.TokenizeInputText(trimMessage)
	if err != nil {
		return "", err
	}
	filterReadyPods := utils.FilterReadyPods(pods)
	if len(filterReadyPods) == 0 {
		return "", fmt.Errorf("no pods to forward request")
	}
	if len(filterReadyPods) == 1 {
		for _, pod := range pods {
			return getPodAddress(pod.Status.PodIP)
		}
	}
	readyPods := map[string]struct{}{}
	for _, pod := range filterReadyPods {
		readyPods[pod.Status.PodIP] = struct{}{}
	}
	// var readyPodNames []string
	// for pod := range readyPods {
	// 	readyPodNames = append(readyPodNames, pod)
	// }

	var unMatchedPrefixHashes []uint64
	var matchedPods map[string]int
	b, value := isLoadImbalanced(p.cache, filterReadyPods)
	if b {
		targetPod = value
		klog.InfoS("prefix_cache_load_imbalanced",
			"target_pod", targetPod,
			"request_id", routingCtx.RequestID)
		unMatchedPrefixHashes = p.prefixCacheIndexer.GetPrefixHashes(tokens)
		p.prefixCacheIndexer.AddPrefix(unMatchedPrefixHashes, routingCtx.Model, targetPod)
		return getPodAddress(value)
	}

	matchedPods, unMatchedPrefixHashes = p.prefixCacheIndexer.MatchPrefix(tokens, routingCtx.Model, readyPods)
	klog.InfoS("prefix_hashes",
		"request_id", routingCtx.RequestID,
		"prefix_hashes", unMatchedPrefixHashes)

	if len(matchedPods) > 0 {
		podRequestCount := getRequestCounts(p.cache, filterReadyPods)
		targetPod = getTargetPodFromMatchedPods(podRequestCount, matchedPods)
		klog.InfoS("prefix_cache_matched_pods",
			"matched_pods", matchedPods,
			"target_pod", targetPod,
			// "ready_pods", readyPodNames,
			"pod_request_count", podRequestCount,
			"request_id", routingCtx.RequestID)
	}

	if len(matchedPods) == 0 || targetPod == "" {
		// no pod with prefix match, select pod with least request count
		targetPod = selectTargetPodWithLeastRequestCount(p.cache, routingCtx.Model, filterReadyPods)
		klog.InfoS("prefix_cache_no_matched_pods",
			"matched_pods", matchedPods,
			// "ready_pods", readyPodNames,
			"target_pod", targetPod,
			"request_id", routingCtx.RequestID)
	}

	if len(unMatchedPrefixHashes) > 0 {
		p.prefixCacheIndexer.AddPrefix(unMatchedPrefixHashes, routingCtx.Model, targetPod)
	}

	return getPodAddress(targetPod)
}

func getTargetPodFromMatchedPods(podRequestCount, matchedPods map[string]int) string {
	var targetPod string
	requestCount := []float64{}
	for _, cnt := range podRequestCount {
		requestCount = append(requestCount, float64(cnt))
	}
	meanRequestCount := mean(requestCount)
	stdDevRequestCount := standardDeviation(requestCount)

	podnames := []string{}
	for podname := range matchedPods {
		podnames = append(podnames, podname)
	}

	// sort pods with decreasing %perfixmatch, for same match sort increasing request count
	sort.SliceStable(podnames, func(i, j int) bool {
		if matchedPods[podnames[i]] == matchedPods[podnames[j]] {
			return podRequestCount[podnames[i]] < podRequestCount[podnames[j]]
		}
		return matchedPods[podnames[i]] > matchedPods[podnames[j]]
	})

	// fmt.Println("Sorted map by value:")
	// for _, podname := range podnames {
	// 	fmt.Printf("%s: %d -- %d\n", podname, matchedPods[podname], podRequestCount[podname])
	// }

	// klog.InfoS("-", "mean", meanRequestCount, "std_dev", stdDevRequestCount)

	// select targetpod with highest %prefixmatch and within stddev
	for _, podname := range podnames {
		reqCnt := float64(podRequestCount[podname])
		if reqCnt <= meanRequestCount+stdDevRequestCount {
			targetPod = podname
			break
		}
	}
	return targetPod
}

// mean calculates the mean of a slice of float64 numbers.
func mean(numbers []float64) float64 {
	sum := 0.0
	for _, number := range numbers {
		sum += number
	}
	return sum / float64(len(numbers))
}

// standardDeviation calculates the standard deviation of a slice of float64 numbers.
func standardDeviation(numbers []float64) float64 {
	avg := mean(numbers)
	sumOfSquares := 0.0
	for _, number := range numbers {
		sumOfSquares += math.Pow(number-avg, 2)
	}
	variance := sumOfSquares / float64(len(numbers)-1)
	return math.Sqrt(variance)
}
