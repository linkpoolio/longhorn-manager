package monitor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Loaded nvme_rdma/ib_core modules alone do not prove RDMA capability; the
// node must expose an actual device under /sys/class/infiniband. This pins the
// hardware predicate used by checkNvmfRDMACapability before labelling a node
// nvmf-transport=rdma.
func TestHasRDMADevice(t *testing.T) {
	assert := require.New(t)

	emptyDir := t.TempDir()
	assert.False(hasRDMADevice(emptyDir, os.ReadDir), "empty infiniband class dir means no RDMA device")

	missingDir := filepath.Join(emptyDir, "does-not-exist")
	assert.False(hasRDMADevice(missingDir, os.ReadDir), "missing infiniband class dir means no RDMA device")

	populatedDir := t.TempDir()
	assert.NoError(os.Mkdir(filepath.Join(populatedDir, "mlx5_0"), 0o755))
	assert.True(hasRDMADevice(populatedDir, os.ReadDir), "a device entry under the infiniband class dir means RDMA hardware is present")
}
