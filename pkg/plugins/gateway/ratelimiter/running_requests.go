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

package ratelimiter

import (
	"context"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// gatewayRunningRequestsKey is the Redis hash key used to store per-gateway
// running request counts.  Each hash field is a gateway instance ID and its
// value is the number of requests that instance is currently handling.
const gatewayRunningRequestsKey = "aibrix:gateway:running_requests"

// RunningRequestSyncer syncs the active (in-flight) request count for this
// gateway instance to Redis so the aggregate across all instances can be read
// by any peer.
type RunningRequestSyncer interface {
	// Init resets the running request count for gatewayID to zero.
	// Call this at startup to clear counts left by a previous run of the same instance.
	Init(ctx context.Context, gatewayID string) error

	// Incr atomically increments the running request count for gatewayID by one
	// and returns the new value.
	Incr(ctx context.Context, gatewayID string) (int64, error)

	// Decr atomically decrements the running request count for gatewayID by one
	// and returns the new value.
	Decr(ctx context.Context, gatewayID string) (int64, error)

	// Total returns the sum of running requests across all tracked gateway instances.
	Total(ctx context.Context) (int64, error)
}

type redisRunningRequestSyncer struct {
	client *redis.Client
}

// NewRedisRunningRequestSyncer creates a RunningRequestSyncer backed by a
// Redis hash.  Each gateway instance is stored as a field; Total() sums all
// field values.
func NewRedisRunningRequestSyncer(client *redis.Client) RunningRequestSyncer {
	return &redisRunningRequestSyncer{client: client}
}

func (r *redisRunningRequestSyncer) Init(ctx context.Context, gatewayID string) error {
	return r.client.HSet(ctx, gatewayRunningRequestsKey, gatewayID, 0).Err()
}

func (r *redisRunningRequestSyncer) Incr(ctx context.Context, gatewayID string) (int64, error) {
	return r.client.HIncrBy(ctx, gatewayRunningRequestsKey, gatewayID, 1).Result()
}

func (r *redisRunningRequestSyncer) Decr(ctx context.Context, gatewayID string) (int64, error) {
	return r.client.HIncrBy(ctx, gatewayRunningRequestsKey, gatewayID, -1).Result()
}

func (r *redisRunningRequestSyncer) Total(ctx context.Context) (int64, error) {
	vals, err := r.client.HVals(ctx, gatewayRunningRequestsKey).Result()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, v := range vals {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n > 0 {
			total += n
		}
	}
	return total, nil
}
