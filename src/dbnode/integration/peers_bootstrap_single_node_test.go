//go:build integration
// +build integration

// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/m3db/m3/src/cluster/services"
	"github.com/m3db/m3/src/cluster/shard"
	"github.com/m3db/m3/src/dbnode/integration/generate"
	"github.com/m3db/m3/src/dbnode/namespace"
	"github.com/m3db/m3/src/dbnode/retention"
	"github.com/m3db/m3/src/dbnode/sharding"
	"github.com/m3db/m3/src/dbnode/storage/bootstrap/bootstrapper/uninitialized"
	"github.com/m3db/m3/src/dbnode/topology"
	"github.com/m3db/m3/src/dbnode/topology/testutil"
	xtest "github.com/m3db/m3/src/x/test"
)

// TestPeersBootstrapSingleNodeUninitialized makes sure that we can include the peer bootstrapper
// in a single-node topology of a non-initialized cluster without causing a bootstrap failure or infinite hang.
func TestPeersBootstrapSingleNodeUninitialized(t *testing.T) {
	opts := NewTestOptions(t)

	// Define a topology with initializing shards
	minShard := uint32(0)
	maxShard := uint32(opts.NumShards()) - uint32(1)
	instances := []services.ServiceInstance{
		node(t, 0, newClusterShardsRange(minShard, maxShard, shard.Initializing)),
	}

	hostShardSets := []topology.HostShardSet{}
	for _, instance := range instances {
		h, err := topology.NewHostShardSetFromServiceInstance(instance, sharding.DefaultHashFn(opts.NumShards()))
		require.NoError(t, err)
		hostShardSets = append(hostShardSets, h)
	}

	shards := testutil.ShardsRange(minShard, maxShard, shard.Initializing)
	shardSet, err := sharding.NewShardSet(
		shards,
		sharding.DefaultHashFn(int(maxShard)),
	)
	require.NoError(t, err)

	topoOpts := topology.NewStaticOptions().
		SetReplicas(len(instances)).
		SetHostShardSets(hostShardSets).
		SetShardSet(shardSet)
	topoInit := topology.NewStaticInitializer(topoOpts)

	setupOpts := []BootstrappableTestSetupOptions{
		{
			DisablePeersBootstrapper: false,
			TopologyInitializer:      topoInit,
			// This will bootstrap w/ unfulfilled ranges.
			FinalBootstrapper: uninitialized.UninitializedTopologyBootstrapperName,
		},
	}
	testPeersBootstrapSingleNode(t, setupOpts)
}

// TestPeersBootstrapSingleNodeInitialized makes sure that we can include the peer bootstrapper
// in a single-node topology of already initialized cluster without causing a bootstrap failure or infinite hang.
func TestPeersBootstrapSingleNodeInitialized(t *testing.T) {
	setupOpts := []BootstrappableTestSetupOptions{
		{DisablePeersBootstrapper: false},
	}
	testPeersBootstrapSingleNode(t, setupOpts)
}

func testPeersBootstrapSingleNode(t *testing.T, setupOpts []BootstrappableTestSetupOptions) {
	if testing.Short() {
		t.SkipNow()
	}

	// Test setups
	log := xtest.NewLogger(t)
	retentionOpts := retention.NewOptions().
		SetRetentionPeriod(20 * time.Hour).
		SetBlockSize(2 * time.Hour).
		SetBufferPast(10 * time.Minute).
		SetBufferFuture(2 * time.Minute)
	namesp, err := namespace.NewMetadata(testNamespaces[0], namespace.NewOptions().SetRetentionOptions(retentionOpts))
	require.NoError(t, err)
	opts := NewTestOptions(t).
		SetNamespaces([]namespace.Metadata{namesp}).
		// Use TChannel clients for writing / reading because we want to target individual nodes at a time
		// and not write/read all nodes in the cluster.
		SetUseTChannelClientForWriting(true).
		SetUseTChannelClientForReading(true)

	setups, closeFn := NewDefaultBootstrappableTestSetups(t, opts, setupOpts)
	defer closeFn()

	// Write test data
	now := setups[0].NowFn()()
	blockSize := retentionOpts.BlockSize()
	seriesMaps := generate.BlocksByStart([]generate.BlockConfig{
		{IDs: []string{"foo", "baz"}, NumPoints: 90, Start: now.Add(-4 * blockSize)},
		{IDs: []string{"foo", "baz"}, NumPoints: 90, Start: now.Add(-3 * blockSize)},
		{IDs: []string{"foo", "baz"}, NumPoints: 90, Start: now.Add(-2 * blockSize)},
		{IDs: []string{"foo", "baz"}, NumPoints: 90, Start: now.Add(-blockSize)},
		{IDs: []string{"foo", "baz"}, NumPoints: 90, Start: now},
	})
	require.NoError(t, writeTestDataToDisk(namesp, setups[0], seriesMaps, 0))

	// Set the time to one blockSize in the future (for which we do not have
	// a fileset file) to ensure we try and use the peer bootstrapper.
	setups[0].SetNowFn(now.Add(blockSize))

	// Start the server with peers and filesystem bootstrappers
	require.NoError(t, setups[0].StartServer())
	log.Debug("servers are now up")

	// Stop the servers
	defer func() {
		setups.parallel(func(s TestSetup) {
			require.NoError(t, s.StopServer())
		})
		log.Debug("servers are now down")
	}()

	// Verify in-memory data match what we expect
	for _, setup := range setups {
		verifySeriesMaps(t, setup, namesp.ID(), seriesMaps)
	}
}
