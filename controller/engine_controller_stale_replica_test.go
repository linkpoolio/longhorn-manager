package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

func TestShouldRecreateStaleV2Engine(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	beyondTimeout := now.Add(-staleReplicaAddressRecreateTimeout - time.Second).Format(time.RFC3339Nano)
	withinTimeout := now.Add(-staleReplicaAddressRecreateTimeout / 2).Format(time.RFC3339Nano)

	makeEngine := func(state longhorn.InstanceState, endpoint string, modes map[string]longhorn.ReplicaMode, times map[string]string) *longhorn.Engine {
		return &longhorn.Engine{
			Spec: longhorn.EngineSpec{
				InstanceSpec: longhorn.InstanceSpec{DataEngine: longhorn.DataEngineTypeV2},
			},
			Status: longhorn.EngineStatus{
				InstanceStatus: longhorn.InstanceStatus{
					CurrentState: state,
				},
				Endpoint:                 endpoint,
				ReplicaModeMap:           modes,
				ReplicaTransitionTimeMap: times,
			},
		}
	}

	healthyVolume := &longhorn.Volume{}
	migratingVolume := &longhorn.Volume{
		Spec: longhorn.VolumeSpec{MigrationNodeID: "node-2"},
	}

	tests := []struct {
		name               string
		engine             *longhorn.Engine
		volume             *longhorn.Volume // defaults to healthyVolume unless volumeLookupFailed
		volumeLookupFailed bool             // pass a nil volume to simulate a failed volume lookup
		want               bool
	}{
		{
			name:   "nil engine",
			engine: nil,
			want:   false,
		},
		{
			name: "v1 engine ignored",
			engine: &longhorn.Engine{
				Spec: longhorn.EngineSpec{
					InstanceSpec: longhorn.InstanceSpec{DataEngine: longhorn.DataEngineTypeV1},
				},
				Status: longhorn.EngineStatus{
					InstanceStatus:           longhorn.InstanceStatus{CurrentState: longhorn.InstanceStateRunning},
					ReplicaModeMap:           map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
					ReplicaTransitionTimeMap: map[string]string{"r1": beyondTimeout},
				},
			},
			want: false,
		},
		{
			name: "engine not running",
			engine: makeEngine(longhorn.InstanceStateStopped, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout}),
			want: false,
		},
		{
			name: "engine has endpoint",
			engine: makeEngine(longhorn.InstanceStateRunning, "/dev/longhorn/test",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout}),
			want: false,
		},
		{
			name: "empty replica mode map",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{},
				map[string]string{}),
			want: false,
		},
		{
			name: "one replica not ERR",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR, "r2": longhorn.ReplicaModeRW},
				map[string]string{"r1": beyondTimeout, "r2": beyondTimeout}),
			want: false,
		},
		{
			name: "missing transition time",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{}),
			want: false,
		},
		{
			name: "malformed transition time",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{"r1": "not-a-timestamp"}),
			want: false,
		},
		{
			name: "within timeout — still transient",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR, "r2": longhorn.ReplicaModeERR},
				map[string]string{"r1": withinTimeout, "r2": withinTimeout}),
			want: false,
		},
		{
			name: "one replica within timeout, one past",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR, "r2": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout, "r2": withinTimeout}),
			want: false,
		},
		{
			name: "all replicas stuck past timeout",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR, "r2": longhorn.ReplicaModeERR, "r3": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout, "r2": beyondTimeout, "r3": beyondTimeout}),
			want: true,
		},
		{
			name: "single replica past timeout",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout}),
			want: true,
		},
		{
			name: "engine being deleted",
			engine: func() *longhorn.Engine {
				e := makeEngine(longhorn.InstanceStateRunning, "",
					map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
					map[string]string{"r1": beyondTimeout})
				ts := metav1.Now()
				e.DeletionTimestamp = &ts
				return e
			}(),
			want: false,
		},
		{
			name: "frontend disabled",
			engine: func() *longhorn.Engine {
				e := makeEngine(longhorn.InstanceStateRunning, "",
					map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
					map[string]string{"r1": beyondTimeout})
				e.Spec.DisableFrontend = true
				return e
			}(),
			want: false,
		},
		{
			name: "volume is migrating",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout}),
			volume: migratingVolume,
			want:   false,
		},
		{
			name: "volume lookup failed",
			engine: makeEngine(longhorn.InstanceStateRunning, "",
				map[string]longhorn.ReplicaMode{"r1": longhorn.ReplicaModeERR},
				map[string]string{"r1": beyondTimeout}),
			volumeLookupFailed: true,
			want:               false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			volume := tc.volume
			if volume == nil && !tc.volumeLookupFailed {
				volume = healthyVolume
			}
			got, reason := shouldRecreateStaleV2Engine(tc.engine, volume, now)
			require.Equal(t, tc.want, got)
			if tc.want {
				require.NotEmpty(t, reason)
			} else {
				require.Empty(t, reason)
			}
		})
	}
}
