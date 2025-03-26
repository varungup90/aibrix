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

// const (
// 	defaultPrefixCacheMatchThresholdPercent = 50
// )

// var (
// 	prefixCacheMatchThresholdPercent = getPrefixCacheMatchThresholdPercent()
// )

// func getPrefixCacheMatchThresholdPercent() int {
// 	value := utils.LoadEnv("AIBRIX_PREFIX_CACHE_MATCH_THRESHOLD_PERCENT", "")
// 	if value != "" {
// 		intValue, err := strconv.Atoi(value)
// 		if err != nil || intValue <= 0 || intValue > 100 {
// 			klog.Infof("invalid AIBRIX_PREFIX_CACHE_MATCH_THRESHOLD_PERCENT: %s, valid value between 0 and 100, failing back to default", value)
// 		} else {
// 			klog.Infof("using AIBRIX_PREFIX_CACHE_MATCH_THRESHOLD_PERCENT env value for prefix cache match threshold percent: %d", intValue)
// 			return intValue
// 		}
// 	}
// 	klog.Infof("using default prefix cache match threshold percent: %d", defaultPrefixCacheMatchThresholdPercent)
// 	return defaultPrefixCacheMatchThresholdPercent
// }

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

	c, err := cache.Get()
	if err != nil {
		return nil, err
	}

	return prefixCacheRouter{
		cache:              c,
		tokenizer:          tokenizerObj,
		prefixCacheIndexer: prefixcacheindexer.NewPrefixHashTable(),
	}, nil
}

func (p prefixCacheRouter) Route(ctx context.Context, pods map[string]*v1.Pod, routingCtx RoutingContext) (string, error) {
	var targetPod string
	tokens, err := p.tokenizer.TokenizeInputText(routingCtx.Message)
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

	matchedPods, unMatchedPrefixHashes := p.prefixCacheIndexer.MatchPrefix(tokens, routingCtx.Model, readyPods)

	if len(matchedPods) > 0 {
		podRequestCount := getRequestCounts(p.cache, routingCtx.Model, filterReadyPods)
		requestCount := []float64{}
		for _, cnt := range podRequestCount {
			requestCount = append(requestCount, cnt)
		}
		meanRequestCount := mean(requestCount)
		stdDevRequestCount := standardDeviation(requestCount)

		podnames := []string{}
		for podname := range matchedPods {
			podnames = append(podnames, podname)
		}

		// sort pods with decreasing %perfixmatch
		sort.SliceStable(podnames, func(i, j int) bool {
			return matchedPods[podnames[i]] > matchedPods[podnames[j]]
		})

		// select targetpod with highest %prefixmatch and within stddev
		for _, podname := range podnames {
			reqCnt := podRequestCount[podname]
			if reqCnt <= meanRequestCount+stdDevRequestCount {
				targetPod = podname
				break
			}
		}
	}

	if len(matchedPods) == 0 || targetPod == "" {
		// no pod with prefix match, select pod with least request count
		targetPod = selectTargetPodWithLeastRequestCount(p.cache, routingCtx.Model, filterReadyPods)
	}

	if len(unMatchedPrefixHashes) > 0 {
		p.prefixCacheIndexer.AddPrefix(unMatchedPrefixHashes, routingCtx.Model, targetPod)
	}

	var matchedPodNames, readyPodNames []string
	for pod := range matchedPods {
		matchedPodNames = append(matchedPodNames, pod)
	}
	for pod := range readyPods {
		readyPodNames = append(readyPodNames, pod)
	}
	klog.V(4).InfoS("prefix cache route",
		"matched_pods", matchedPodNames,
		"ready_pods", readyPodNames,
		"target_pod", targetPod)

	return getPodAddress(targetPod)
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
