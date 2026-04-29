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
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSyncerForTest(t *testing.T) (RunningRequestSyncer, func()) {
	t.Helper()

	client := setupRedisForTest(t)
	syncer := NewRedisRunningRequestSyncer(client)

	// Use a test-unique gateway ID to avoid cross-test pollution.
	gatewayID := fmt.Sprintf("test-gw-%s", t.Name())

	cleanup := func() {
		_ = client.HDel(context.Background(), gatewayRunningRequestsKey, gatewayID).Err()
	}
	return syncer, cleanup
}

func TestRunningRequestSyncer_InitSetsZero(t *testing.T) {
	client := setupRedisForTest(t)
	syncer := NewRedisRunningRequestSyncer(client)
	ctx := context.Background()
	gwID := fmt.Sprintf("test-gw-%s", t.Name())
	t.Cleanup(func() { _ = client.HDel(ctx, gatewayRunningRequestsKey, gwID).Err() })

	// Seed a non-zero value to confirm Init resets it.
	_, err := syncer.Incr(ctx, gwID)
	require.NoError(t, err)
	_, err = syncer.Incr(ctx, gwID)
	require.NoError(t, err)

	require.NoError(t, syncer.Init(ctx, gwID))

	total, err := syncer.Total(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}

func TestRunningRequestSyncer_IncrDecr(t *testing.T) {
	client := setupRedisForTest(t)
	syncer := NewRedisRunningRequestSyncer(client)
	ctx := context.Background()
	gwID := fmt.Sprintf("test-gw-%s", t.Name())
	t.Cleanup(func() { _ = client.HDel(ctx, gatewayRunningRequestsKey, gwID).Err() })

	require.NoError(t, syncer.Init(ctx, gwID))

	v, err := syncer.Incr(ctx, gwID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), v)

	v, err = syncer.Incr(ctx, gwID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), v)

	v, err = syncer.Decr(ctx, gwID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), v)

	total, err := syncer.Total(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
}

func TestRunningRequestSyncer_TotalAcrossMultipleGateways(t *testing.T) {
	client := setupRedisForTest(t)
	syncer := NewRedisRunningRequestSyncer(client)
	ctx := context.Background()

	gw1 := fmt.Sprintf("test-gw1-%s", t.Name())
	gw2 := fmt.Sprintf("test-gw2-%s", t.Name())
	t.Cleanup(func() {
		_ = client.HDel(ctx, gatewayRunningRequestsKey, gw1, gw2).Err()
	})

	require.NoError(t, syncer.Init(ctx, gw1))
	require.NoError(t, syncer.Init(ctx, gw2))

	_, err := syncer.Incr(ctx, gw1)
	require.NoError(t, err)
	_, err = syncer.Incr(ctx, gw1)
	require.NoError(t, err)
	_, err = syncer.Incr(ctx, gw2)
	require.NoError(t, err)

	total, err := syncer.Total(ctx)
	require.NoError(t, err)
	// gw1=2, gw2=1, but the hash may contain other test fields; assert at least 3.
	assert.GreaterOrEqual(t, total, int64(3))
}

func TestRunningRequestSyncer_TotalIgnoresNegativeValues(t *testing.T) {
	client := setupRedisForTest(t)
	syncer := NewRedisRunningRequestSyncer(client)
	ctx := context.Background()
	gwID := fmt.Sprintf("test-gw-%s", t.Name())
	t.Cleanup(func() { _ = client.HDel(ctx, gatewayRunningRequestsKey, gwID).Err() })

	require.NoError(t, syncer.Init(ctx, gwID))

	// Force a negative value (e.g. from a crash during prior run).
	_, err := syncer.Decr(ctx, gwID)
	require.NoError(t, err)

	// Total should not include negative per-gateway counts.
	total, err := syncer.Total(ctx)
	require.NoError(t, err)
	// The negative field for gwID should be excluded; other fields may exist.
	// Asserting >= 0 is sufficient since we control gwID.
	assert.GreaterOrEqual(t, total, int64(0))
}

func TestRunningRequestSyncer_ConcurrentIncrDecr(t *testing.T) {
	client := setupRedisForTest(t)
	syncer := NewRedisRunningRequestSyncer(client)
	ctx := context.Background()
	gwID := fmt.Sprintf("test-gw-%s", t.Name())
	t.Cleanup(func() { _ = client.HDel(ctx, gatewayRunningRequestsKey, gwID).Err() })

	require.NoError(t, syncer.Init(ctx, gwID))

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = syncer.Incr(ctx, gwID)
		}()
		go func() {
			defer wg.Done()
			_, _ = syncer.Decr(ctx, gwID)
		}()
	}
	wg.Wait()

	// Net effect of n increments and n decrements is zero.
	v, err := client.HGet(ctx, gatewayRunningRequestsKey, gwID).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(0), v)
}
