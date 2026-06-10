package controller

import (
	"testing"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

// buildReplicaTransportAddressMap turns each in-use replica's reported
// listener ports into the transport-qualified address map the engine dials
// from. The invariant it must hold for production correctness: never publish a
// half entry. Every emitted entry carries a TcpAddress (the universally
// reachable fallback); a replica that reports no TCP port -- a transport-
// unaware/legacy IM, or a malformed RDMA-only exposure -- gets no entry, so the
// engine falls back to the legacy address over TCP rather than dialing an
// address it cannot use.
func TestBuildReplicaTransportAddressMap(t *testing.T) {
	repl := func(storageIP string, tcpPort, rdmaPort int) *longhorn.Replica {
		return &longhorn.Replica{
			Status: longhorn.ReplicaStatus{
				InstanceStatus: longhorn.InstanceStatus{
					StorageIP: storageIP,
					TcpPort:   tcpPort,
					RdmaPort:  rdmaPort,
				},
			},
		}
	}

	cases := []struct {
		name              string
		replicaAddressMap map[string]string
		rs                map[string]*longhorn.Replica
		want              map[string]longhorn.ReplicaTransportAddresses
	}{
		{
			name:              "empty address map -> nil (v1 / nothing to dial)",
			replicaAddressMap: map[string]string{},
			rs:                map[string]*longhorn.Replica{},
			want:              nil,
		},
		{
			name:              "legacy replica (no ports reported) -> no entry -> nil",
			replicaAddressMap: map[string]string{"r-1": "10.10.5.19:21000"},
			rs:                map[string]*longhorn.Replica{"r-1": repl("10.10.5.19", 0, 0)},
			want:              nil,
		},
		{
			name:              "TCP replica -> TcpAddress only",
			replicaAddressMap: map[string]string{"r-1": "10.10.5.19:21000"},
			rs:                map[string]*longhorn.Replica{"r-1": repl("10.10.5.19", 21000, 0)},
			want: map[string]longhorn.ReplicaTransportAddresses{
				"r-1": {TcpAddress: "10.10.5.19:21000"},
			},
		},
		{
			name:              "RDMA replica -> both addresses (TCP fallback + RDMA primary)",
			replicaAddressMap: map[string]string{"r-1": "10.10.5.19:21000"},
			rs:                map[string]*longhorn.Replica{"r-1": repl("10.10.5.19", 21001, 21000)},
			want: map[string]longhorn.ReplicaTransportAddresses{
				"r-1": {TcpAddress: "10.10.5.19:21001", RdmaAddress: "10.10.5.19:21000"},
			},
		},
		{
			name:              "malformed RDMA-only (no TCP) -> skipped, no half entry",
			replicaAddressMap: map[string]string{"r-1": "10.10.5.19:21000"},
			rs:                map[string]*longhorn.Replica{"r-1": repl("10.10.5.19", 0, 21000)},
			want:              nil,
		},
		{
			name:              "replica missing from rs -> skipped",
			replicaAddressMap: map[string]string{"r-1": "10.10.5.19:21000"},
			rs:                map[string]*longhorn.Replica{},
			want:              nil,
		},
		{
			name: "mixed: TCP replica kept, legacy replica dropped",
			replicaAddressMap: map[string]string{
				"r-1": "10.10.5.19:21000",
				"r-2": "10.10.3.19:22000",
			},
			rs: map[string]*longhorn.Replica{
				"r-1": repl("10.10.5.19", 21000, 0),
				"r-2": repl("10.10.3.19", 0, 0),
			},
			want: map[string]longhorn.ReplicaTransportAddresses{
				"r-1": {TcpAddress: "10.10.5.19:21000"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildReplicaTransportAddressMap(tc.rs, tc.replicaAddressMap)
			if len(got) != len(tc.want) {
				t.Fatalf("entry count: got %d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for name, wantEntry := range tc.want {
				gotEntry, ok := got[name]
				if !ok {
					t.Fatalf("missing entry for %s; got %v", name, got)
				}
				if gotEntry.TcpAddress != wantEntry.TcpAddress || gotEntry.RdmaAddress != wantEntry.RdmaAddress {
					t.Errorf("%s: got %+v, want %+v", name, gotEntry, wantEntry)
				}
				// Hard invariant: never a half entry.
				if gotEntry.TcpAddress == "" {
					t.Errorf("%s: emitted entry with empty TcpAddress (half entry): %+v", name, gotEntry)
				}
			}
		})
	}
}
