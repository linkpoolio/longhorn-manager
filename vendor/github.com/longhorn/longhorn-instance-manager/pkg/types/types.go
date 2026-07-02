package types

import (
	"time"
)

const (
	GRPCServiceTimeout = 3 * time.Minute
	// GRPCServiceMutateTimeout bounds instance create/delete calls. Under a
	// node-wide recovery storm a single v2 instance creation legitimately
	// spends minutes in kernel-side retries (nvme connect, udev settle, dm
	// validation); a shorter deadline aborts the client while the server
	// completes the work anyway, poisoning the caller's state machine with
	// spurious errors.
	GRPCServiceMutateTimeout = 6 * time.Minute

	ProcessStateRunning  = "running"
	ProcessStateStarting = "starting"
	ProcessStateStopped  = "stopped"
	ProcessStateStopping = "stopping"
	ProcessStateError    = "error"

	DiskGrpcService           = "disk gRPC server"
	SpdkGrpcService           = "spdk gRPC server"
	ProcessManagerGrpcService = "process-manager gRPC server"
	InstanceGrpcService       = "instance gRPC server"
	ProxyGRPCService          = "proxy gRPC server"
)

const (
	InstanceManagerProcessManagerServiceDefaultPort = 8500
	InstanceManagerProxyServiceDefaultPort          = InstanceManagerProcessManagerServiceDefaultPort + 1 // 8501
	InstanceManagerDiskServiceDefaultPort           = InstanceManagerProcessManagerServiceDefaultPort + 2 // 8502
	InstanceManagerInstanceServiceDefaultPort       = InstanceManagerProcessManagerServiceDefaultPort + 3 // 8503
	InstanceManagerSpdkServiceDefaultPort           = InstanceManagerProcessManagerServiceDefaultPort + 4 // 8504
)

var (
	WaitInterval = 100 * time.Millisecond
	WaitCount    = 600
)

const (
	RetryInterval = 3 * time.Second
	RetryCounts   = 3
)

const (
	InstanceTypeEngine         = "engine"
	InstanceTypeReplica        = "replica"
	InstanceTypeEngineFrontend = "engine-frontend"
)

const (
	EngineConditionFilesystemReadOnly = "FilesystemReadOnly"
)

const TcpAddressPrefix = "tcp://"

func AddTcpPrefixForAddress(address string) string {
	if address == "" {
		return ""
	}

	return TcpAddressPrefix + address
}
