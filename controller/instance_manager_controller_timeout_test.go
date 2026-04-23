package controller

import (
	"context"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	lhfake "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned/fake"

	. "gopkg.in/check.v1"
)

// --- replica-timeout Setting CR plumbing ---

// newReplicaTimeoutSetting returns a DataEngineSpecific Setting with the given
// value keyed under DataEngineTypeV2 — same JSON shape emitted by the real
// datastore defaulter for v2-data-engine-* settings.
func newReplicaTimeoutSetting(name types.SettingName, v2Value string) *longhorn.Setting {
	return &longhorn.Setting{
		ObjectMeta: metav1.ObjectMeta{Name: string(name)},
		Value:      `{"v2":"` + v2Value + `"}`,
	}
}

func seedReplicaTimeoutSettings(c *C, lhClient *lhfake.Clientset, sIndexer cache.Indexer, values map[types.SettingName]string) {
	for name, val := range values {
		setting := newReplicaTimeoutSetting(name, val)
		created, err := lhClient.LonghornV1beta2().Settings(TestNamespace).Create(context.TODO(), setting, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		// The datastore reads through informer caches, so creating the
		// Setting via the fake client alone is invisible. Mirror the
		// pattern in instance_manager_controller_test.go and push into the
		// Settings indexer directly.
		c.Assert(sIndexer.Add(created), IsNil)
	}
}

// buildTimeoutTestController wires up a minimal controller with the 5
// replica-timeout settings pre-created. Returns the controller and the IM used
// by the assertions.
func buildTimeoutTestController(c *C, settings map[types.SettingName]string) (*InstanceManagerController, *longhorn.InstanceManager) {
	kubeClient := fake.NewSimpleClientset()                    // nolint: staticcheck
	lhClient := lhfake.NewSimpleClientset()                    // nolint: staticcheck
	extensionsClient := apiextensionsfake.NewSimpleClientset() // nolint: staticcheck

	informerFactories := util.NewInformerFactories(TestNamespace, kubeClient, lhClient, controller.NoResyncPeriodFunc())
	sIndexer := informerFactories.LhInformerFactory.Longhorn().V1beta2().Settings().Informer().GetIndexer()

	imc, err := newTestInstanceManagerController(lhClient, kubeClient, extensionsClient, informerFactories, TestNode1)
	c.Assert(err, IsNil)

	seedReplicaTimeoutSettings(c, lhClient, sIndexer, settings)

	im := &longhorn.InstanceManager{
		ObjectMeta: metav1.ObjectMeta{Name: "im-v2", Namespace: TestNamespace},
		Spec: longhorn.InstanceManagerSpec{
			NodeID:     TestNode1,
			DataEngine: longhorn.DataEngineTypeV2,
			Image:      "test-im-image",
		},
	}

	_ = scheme.Scheme // reference to keep import used
	return imc, im
}

// --- getV2ReplicaTimeoutEnv ---

func (s *TestSuite) TestGetV2ReplicaTimeoutEnvEmitsAllFiveVars(c *C) {
	values := map[types.SettingName]string{
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec:  "30",
		types.SettingNameDataEngineReplicaFastIOFailTimeoutSec: "20",
		types.SettingNameDataEngineReplicaReconnectDelaySec:    "5",
		types.SettingNameDataEngineReplicaTransportAckTimeout:  "12",
		types.SettingNameDataEngineReplicaKeepAliveTimeoutMs:   "15000",
	}
	imc, _ := buildTimeoutTestController(c, values)

	envs, err := imc.getV2ReplicaTimeoutEnv(longhorn.DataEngineTypeV2)
	c.Assert(err, IsNil)
	c.Assert(len(envs), Equals, 5)

	byName := map[string]string{}
	for _, e := range envs {
		byName[e.Name] = e.Value
	}
	c.Assert(byName[types.EnvV2ReplicaCtrlrLossTimeoutSec], Equals, "30")
	c.Assert(byName[types.EnvV2ReplicaFastIOFailTimeoutSec], Equals, "20")
	c.Assert(byName[types.EnvV2ReplicaReconnectDelaySec], Equals, "5")
	c.Assert(byName[types.EnvV2ReplicaTransportAckTimeout], Equals, "12")
	c.Assert(byName[types.EnvV2ReplicaKeepAliveTimeoutMs], Equals, "15000")
}

// --- isV2ReplicaTimeoutEnvSynced ---

func v2ReplicaTimeoutIM() *longhorn.InstanceManager {
	return &longhorn.InstanceManager{
		ObjectMeta: metav1.ObjectMeta{Name: "im-v2", Namespace: TestNamespace},
		Spec: longhorn.InstanceManagerSpec{
			NodeID:     TestNode1,
			DataEngine: longhorn.DataEngineTypeV2,
			Image:      "test-im-image",
		},
	}
}

func podWithEnv(envs ...corev1.EnvVar) *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "instance-manager", Env: envs},
			},
		},
	}
}

func (s *TestSuite) TestIsV2ReplicaTimeoutEnvSyncedMatching(c *C) {
	values := map[types.SettingName]string{
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec: "30",
	}
	imc, _ := buildTimeoutTestController(c, values)

	pod := podWithEnv(corev1.EnvVar{Name: types.EnvV2ReplicaCtrlrLossTimeoutSec, Value: "30"})

	synced, err := imc.isV2ReplicaTimeoutEnvSynced(v2ReplicaTimeoutIM(), pod,
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec, types.EnvV2ReplicaCtrlrLossTimeoutSec)
	c.Assert(err, IsNil)
	c.Assert(synced, Equals, true)
}

func (s *TestSuite) TestIsV2ReplicaTimeoutEnvSyncedMismatch(c *C) {
	values := map[types.SettingName]string{
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec: "60",
	}
	imc, _ := buildTimeoutTestController(c, values)

	pod := podWithEnv(corev1.EnvVar{Name: types.EnvV2ReplicaCtrlrLossTimeoutSec, Value: "15"})

	synced, err := imc.isV2ReplicaTimeoutEnvSynced(v2ReplicaTimeoutIM(), pod,
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec, types.EnvV2ReplicaCtrlrLossTimeoutSec)
	c.Assert(err, IsNil)
	// Pod's env reflects the previous value; a change to the Setting must
	// cause this to flip false so the IM pod gets recreated.
	c.Assert(synced, Equals, false)
}

func (s *TestSuite) TestIsV2ReplicaTimeoutEnvSyncedNoEnvOnPodWithSettingSet(c *C) {
	values := map[types.SettingName]string{
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec: "15",
	}
	imc, _ := buildTimeoutTestController(c, values)

	// Pod predates this change and never had the env injected.
	pod := podWithEnv()

	synced, err := imc.isV2ReplicaTimeoutEnvSynced(v2ReplicaTimeoutIM(), pod,
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec, types.EnvV2ReplicaCtrlrLossTimeoutSec)
	c.Assert(err, IsNil)
	// The desired value is non-empty but the env is missing — treat as
	// unsynced so pod recreation re-injects the env.
	c.Assert(synced, Equals, false)
}

func (s *TestSuite) TestIsV2ReplicaTimeoutEnvSyncedV1DataEngineIsNoop(c *C) {
	values := map[types.SettingName]string{
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec: "15",
	}
	imc, _ := buildTimeoutTestController(c, values)

	v1IM := &longhorn.InstanceManager{
		ObjectMeta: metav1.ObjectMeta{Name: "im-v1", Namespace: TestNamespace},
		Spec: longhorn.InstanceManagerSpec{
			NodeID:     TestNode1,
			DataEngine: longhorn.DataEngineTypeV1,
			Image:      "test-im-image",
		},
	}
	pod := podWithEnv() // V1 pods do not carry these env vars.

	synced, err := imc.isV2ReplicaTimeoutEnvSynced(v1IM, pod,
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec, types.EnvV2ReplicaCtrlrLossTimeoutSec)
	c.Assert(err, IsNil)
	// V2-only settings must not force V1 IM pod recreation.
	c.Assert(synced, Equals, true)
}

// --- sanity on the Setting registration itself ---

func (s *TestSuite) TestReplicaTimeoutSettingsRegisteredAsDangerZone(c *C) {
	danger := types.GetDangerZoneSettings()
	c.Assert(danger.Has(types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec), Equals, true)
	c.Assert(danger.Has(types.SettingNameDataEngineReplicaFastIOFailTimeoutSec), Equals, true)
	c.Assert(danger.Has(types.SettingNameDataEngineReplicaReconnectDelaySec), Equals, true)
	c.Assert(danger.Has(types.SettingNameDataEngineReplicaTransportAckTimeout), Equals, true)
	c.Assert(danger.Has(types.SettingNameDataEngineReplicaKeepAliveTimeoutMs), Equals, true)
}

func (s *TestSuite) TestReplicaTimeoutSettingsHaveV2Defaults(c *C) {
	for name, expectedDefault := range map[types.SettingName]string{
		types.SettingNameDataEngineReplicaCtrlrLossTimeoutSec:  "15",
		types.SettingNameDataEngineReplicaFastIOFailTimeoutSec: "10",
		types.SettingNameDataEngineReplicaReconnectDelaySec:    "2",
		types.SettingNameDataEngineReplicaTransportAckTimeout:  "10",
		types.SettingNameDataEngineReplicaKeepAliveTimeoutMs:   "10000",
	} {
		def, ok := types.GetSettingDefinition(name)
		c.Assert(ok, Equals, true, Commentf("setting %s must be registered", name))
		c.Assert(def.DataEngineSpecific, Equals, true, Commentf("setting %s must be data-engine-specific", name))
		c.Assert(def.Default, Equals, `{"v2":"`+expectedDefault+`"}`, Commentf("setting %s default mismatch", name))
		c.Assert(def.Category, Equals, types.SettingCategoryDangerZone)
	}
}
