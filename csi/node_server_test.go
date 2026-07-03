package csi

import (
	"context"
	"fmt"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"

	longhornclient "github.com/longhorn/longhorn-manager/client"
)

func TestGetV2VolumeEndpointForNode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		volume      *longhornclient.Volume
		nodeID      string
		expected    string
		expectError bool
	}{
		{
			name:   "non migratable falls back to any ready endpoint",
			nodeID: "node-b",
			volume: &longhornclient.Volume{
				Name:       "vol-a",
				Migratable: false,
				Controllers: []longhornclient.Controller{
					{HostId: "node-a", Endpoint: "/dev/longhorn/vol-a"},
				},
			},
			expected: "/dev/longhorn/vol-a",
		},
		{
			name:   "migratable selects destination node endpoint",
			nodeID: "node-b",
			volume: &longhornclient.Volume{
				Name:       "vol-b",
				Migratable: true,
				Controllers: []longhornclient.Controller{
					{HostId: "node-a", Endpoint: "/dev/longhorn/vol-b"},
					{HostId: "node-b", Endpoint: "/dev/longhorn/vol-b"},
				},
			},
			expected: "/dev/longhorn/vol-b",
		},
		{
			name:   "migratable does not fall back to another node endpoint",
			nodeID: "node-b",
			volume: &longhornclient.Volume{
				Name:       "vol-c",
				Migratable: true,
				Controllers: []longhornclient.Controller{
					{HostId: "node-a", Endpoint: "/dev/longhorn/vol-c"},
				},
			},
			expectError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, err := getV2VolumeEndpointForNode(tc.volume, tc.nodeID)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error, got endpoint %q", endpoint)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if endpoint != tc.expected {
				t.Fatalf("expected endpoint %q, got %q", tc.expected, endpoint)
			}
		})
	}
}

func TestVolumeStageable(t *testing.T) {
	v2 := &longhornclient.Volume{
		Name: "v", DataEngine: "v2", State: "attached", Ready: true,
		Controllers: []longhornclient.Controller{{HostId: "n1", Endpoint: "/dev/longhorn/v"}},
	}
	if dev, _, ok := volumeStageable(v2, "n1"); !ok || dev != "/dev/longhorn/v" {
		t.Fatalf("expected stageable v2 volume, got ok=%v dev=%q", ok, dev)
	}
	// Non-migratable volumes deliberately fall back to any controller
	// endpoint (pre-existing getV2VolumeEndpointForNode semantics).
	if _, _, ok := volumeStageable(v2, "other-node"); !ok {
		t.Fatal("non-migratable v2 volume falls back to any controller endpoint")
	}
	v2.Migratable = true
	if _, reason, ok := volumeStageable(v2, "other-node"); ok {
		t.Fatalf("migratable v2 volume must not be stageable on a different node (%s)", reason)
	}
	v2.Migratable = false
	v2.Ready = false
	if _, _, ok := volumeStageable(v2, "n1"); ok {
		t.Fatal("not-ready volume must not be stageable")
	}
	v2.Ready = true
	v2.State = "attaching"
	if _, _, ok := volumeStageable(v2, "n1"); ok {
		t.Fatal("attaching volume must not be stageable")
	}

	v1 := &longhornclient.Volume{
		Name: "w", DataEngine: "v1", State: "attached", Ready: true,
		Controllers: []longhornclient.Controller{{HostId: "n1", Endpoint: "/dev/longhorn/w"}},
	}
	if dev, _, ok := volumeStageable(v1, "n1"); !ok || dev != "/dev/longhorn/w" {
		t.Fatalf("expected stageable v1 volume, got ok=%v dev=%q", ok, dev)
	}
	v1.Controllers[0].Endpoint = ""
	if _, _, ok := volumeStageable(v1, "n1"); ok {
		t.Fatal("v1 volume without endpoint must not be stageable")
	}
}

func TestStageWaitDeadlineHonorsContext(t *testing.T) {
	// Context deadline sooner than maxWait: clamp to ctx deadline - margin.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d := stageWaitDeadline(ctx, 90*time.Second)
	if until := time.Until(d); until > 25*time.Second {
		t.Fatalf("deadline %v not clamped under context deadline", until)
	}
	// No context deadline: maxWait applies.
	d = stageWaitDeadline(context.Background(), 90*time.Second)
	if until := time.Until(d); until < 80*time.Second || until > 91*time.Second {
		t.Fatalf("expected ~90s deadline, got %v", until)
	}
}

func TestWaitForDeviceReadable(t *testing.T) {
	orig := deviceReadCheckFn
	defer func() { deviceReadCheckFn = orig }()
	log := logrus.NewEntry(logrus.StandardLogger())

	// Becomes readable on the third check: wait must absorb the failures
	// in-call instead of surfacing them.
	calls := 0
	deviceReadCheckFn = func(string) error {
		calls++
		if calls < 3 {
			return fmt.Errorf("no such device or address")
		}
		return nil
	}
	if err := waitForDeviceReadable(context.Background(), "/dev/test", log); err != nil {
		t.Fatalf("expected eventual readability, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 checks, got %d", calls)
	}

	// Never readable + tight context: retryable Unavailable, no hang.
	deviceReadCheckFn = func(string) error { return fmt.Errorf("no such device or address") }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := waitForDeviceReadable(ctx, "/dev/test", log)
	if err == nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable on timeout, got %v", err)
	}
}
