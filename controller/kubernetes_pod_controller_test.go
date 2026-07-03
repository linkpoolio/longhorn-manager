package controller

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	. "gopkg.in/check.v1"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/engineapi"
	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	lhfake "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned/fake"
)

func newTestKubernetesPodController(
	lhClient *lhfake.Clientset, kubeClient *fake.Clientset, extensionsClient *apiextensionsfake.Clientset,
	informerFactories *util.InformerFactories, controllerID string) (*KubernetesPodController, error) {
	ds := datastore.NewDataStore(TestNamespace, lhClient, kubeClient, extensionsClient, informerFactories)

	kc, err := NewKubernetesPodController(logrus.StandardLogger(), ds, scheme.Scheme, kubeClient, controllerID)
	if err != nil {
		return nil, err
	}

	kc.eventRecorder = record.NewFakeRecorder(100)
	for index := range kc.cacheSyncs {
		kc.cacheSyncs[index] = alwaysReady
	}

	return kc, nil
}

type fakeShareManagerHealthServer struct {
	healthpb.UnimplementedHealthServer
	status healthpb.HealthCheckResponse_ServingStatus
}

func (s *fakeShareManagerHealthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: s.status}, nil
}

func startFakeShareManagerHealthServer(c *C, status healthpb.HealthCheckResponse_ServingStatus) func() {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(engineapi.ShareManagerDefaultPort))
	lis, err := net.Listen("tcp", address)
	c.Assert(err, IsNil)

	grpcServer := grpc.NewServer()
	healthpb.RegisterHealthServer(grpcServer, &fakeShareManagerHealthServer{status: status})

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	return func() {
		grpcServer.Stop()
		_ = lis.Close()
	}
}

func (s *TestSuite) TestHandlePodDeletionForRWXVolumeRemountScenarios(c *C) {
	datastore.SkipListerCheck = true
	defer func() {
		datastore.SkipListerCheck = false
	}()

	testCases := map[string]struct {
		shareManagerPodExists      bool
		shareManagerPodNoStartTime bool
		shareManagerPodStartsAt    time.Duration
		shareManagerServing        bool
		shareManagerCheckErr       bool
		endpointNetworkRWX         bool
		remountRequestedAtOffset   time.Duration
		expectPodDeletion          bool
		description                string
	}{
		"auto-salvage: share-manager pod not restarted, skip deletion": {
			shareManagerPodExists:   true,
			shareManagerPodStartsAt: -20 * time.Second,
			shareManagerServing:     true,
			expectPodDeletion:       false,
			description:             "NFS server still running, client can recover",
		},
		"failover: share-manager pod restarted and serving after remount request, delete workload pod": {
			shareManagerPodExists:   true,
			shareManagerPodStartsAt: 5 * time.Second,
			shareManagerServing:     true,
			expectPodDeletion:       true,
			description:             "Replacement share-manager is ready to serve I/O",
		},
		"failover in progress: share-manager pod not found, skip deletion": {
			shareManagerPodExists: false,
			expectPodDeletion:     false,
			description:           "Wait for replacement share-manager pod before remounting workload",
		},
		"failover in progress: share-manager pod has no start time, skip deletion": {
			shareManagerPodExists:      true,
			shareManagerPodNoStartTime: true,
			expectPodDeletion:          false,
			description:                "Wait for replacement share-manager pod to start before remounting workload",
		},
		"failover in progress: replacement pod not serving yet, skip deletion": {
			shareManagerPodExists:   true,
			shareManagerPodStartsAt: 5 * time.Second,
			shareManagerServing:     false,
			expectPodDeletion:       false,
			description:             "Give replacement share-manager time to become ready",
		},
		"failover in progress: serving check error, skip deletion": {
			shareManagerPodExists:   true,
			shareManagerPodStartsAt: 5 * time.Second,
			shareManagerCheckErr:    true,
			expectPodDeletion:       false,
			description:             "Retry when serving state cannot be determined yet",
		},
		"endpoint-network RWX: use existing remount path without serving gate": {
			shareManagerPodExists:   true,
			shareManagerPodStartsAt: 5 * time.Second,
			endpointNetworkRWX:      true,
			expectPodDeletion:       true,
			description:             "Endpoint-network RWX should not wait for share-manager gRPC serving gate here",
		},
		"refreshed remount request after replacement pod start: skip deletion with current timestamp gate": {
			shareManagerPodExists:    true,
			shareManagerPodStartsAt:  5 * time.Second,
			shareManagerServing:      true,
			remountRequestedAtOffset: 10 * time.Second,
			expectPodDeletion:        false,
			description:              "Current behavior keeps the workload when RemountRequestedAt is refreshed after replacement start",
		},
	}

	for name, tc := range testCases {
		c.Logf("Running test case: %s", name)

		kubeClient := fake.NewSimpleClientset()
		lhClient := lhfake.NewSimpleClientset() //nolint:staticcheck
		extensionsClient := apiextensionsfake.NewSimpleClientset()
		informerFactories := util.NewInformerFactories(TestNamespace, kubeClient, lhClient, controller.NoResyncPeriodFunc())

		kc, err := newTestKubernetesPodController(lhClient, kubeClient, extensionsClient, informerFactories, TestNode1)
		c.Assert(err, IsNil)

		referenceTime := time.Now().UTC()
		baseTime := referenceTime.Add(-2 * time.Minute)
		initialRemountRequestedAtTime := baseTime.Add(30 * time.Second)
		remountRequestedAtTime := initialRemountRequestedAtTime.Add(tc.remountRequestedAtOffset)
		remountRequestedAt := remountRequestedAtTime.UTC().Format(time.RFC3339)
		podStartTime := metav1.NewTime(baseTime)

		vol := newVolume(TestVolumeName, 1)
		vol.Namespace = TestNamespace
		vol.Spec.AccessMode = longhorn.AccessModeReadWriteMany
		vol.Spec.Migratable = false
		vol.Status.RemountRequestedAt = remountRequestedAt

		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: TestPVName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:       types.LonghornDriverName,
						VolumeHandle: vol.Name,
					},
				},
			},
		}

		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestPVCName,
				Namespace: TestNamespace,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				VolumeName: pv.Name,
			},
		}

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestPod1,
				Namespace: TestNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: appsv1.SchemeGroupVersion.String(),
						Kind:       types.KubernetesKindDeployment,
						Name:       TestDeploymentName,
						Controller: ptrTo(true),
					},
				},
			},
			Spec: corev1.PodSpec{
				NodeName: TestNode1,
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvc.Name,
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{
				StartTime: &podStartTime,
			},
		}

		autoDeleteSetting := newSetting(string(types.SettingNameAutoDeletePodWhenVolumeDetachedUnexpectedly), "true")
		endpointNetworkSetting := newSetting(string(types.SettingNameEndpointNetworkForRWXVolume), "")
		if tc.endpointNetworkRWX {
			endpointNetworkSetting.Value = "10.0.0.1/24"
		}

		c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Settings().Informer().GetIndexer().Add(autoDeleteSetting), IsNil)
		c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Settings().Informer().GetIndexer().Add(endpointNetworkSetting), IsNil)
		c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Volumes().Informer().GetIndexer().Add(vol), IsNil)
		c.Assert(informerFactories.KubeInformerFactory.Core().V1().PersistentVolumeClaims().Informer().GetIndexer().Add(pvc), IsNil)
		c.Assert(informerFactories.KubeInformerFactory.Core().V1().PersistentVolumes().Informer().GetIndexer().Add(pv), IsNil)
		c.Assert(informerFactories.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer().Add(pod), IsNil)

		_, err = lhClient.LonghornV1beta2().Settings(TestNamespace).Create(context.TODO(), autoDeleteSetting, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = lhClient.LonghornV1beta2().Settings(TestNamespace).Create(context.TODO(), endpointNetworkSetting, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = lhClient.LonghornV1beta2().Volumes(TestNamespace).Create(context.TODO(), vol, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = kubeClient.CoreV1().PersistentVolumeClaims(TestNamespace).Create(context.TODO(), pvc, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = kubeClient.CoreV1().PersistentVolumes().Create(context.TODO(), pv, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = kubeClient.CoreV1().Pods(TestNamespace).Create(context.TODO(), pod, metav1.CreateOptions{})
		c.Assert(err, IsNil)

		var stopFakeHealthServer func()
		if tc.shareManagerPodExists && !tc.shareManagerPodNoStartTime && !tc.endpointNetworkRWX && tc.shareManagerPodStartsAt > 0 && !tc.shareManagerCheckErr {
			status := healthpb.HealthCheckResponse_NOT_SERVING
			if tc.shareManagerServing {
				status = healthpb.HealthCheckResponse_SERVING
			}
			stopFakeHealthServer = startFakeShareManagerHealthServer(c, status)
		}

		if tc.shareManagerPodExists {
			smPodName := types.GetShareManagerPodNameFromShareManagerName(vol.Name)
			smPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      smPodName,
					Namespace: TestNamespace,
				},
				Spec: corev1.PodSpec{
					NodeName: TestNode1,
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					PodIP: "127.0.0.1",
				},
			}
			if !tc.shareManagerPodNoStartTime {
				smPodStartTime := metav1.NewTime(initialRemountRequestedAtTime.Add(tc.shareManagerPodStartsAt))
				smPod.Status.StartTime = &smPodStartTime
			}

			smPod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name:  "share-manager",
					Ready: tc.shareManagerServing,
				},
			}
			smPod.Status.Conditions = []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(referenceTime.Add(-time.Minute)),
			}}

			c.Assert(informerFactories.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer().Add(smPod), IsNil)
			_, err = kubeClient.CoreV1().Pods(TestNamespace).Create(context.TODO(), smPod, metav1.CreateOptions{})
			c.Assert(err, IsNil)
		}

		kubeClient.ClearActions()

		err = kc.handlePodDeletionIfVolumeRequestRemount(pod)
		c.Assert(err, IsNil)

		actions := kubeClient.Actions()
		if tc.expectPodDeletion {
			c.Assert(actions, HasLen, 1, Commentf("Test case: %s - %s", name, tc.description))
			c.Assert(actions[0].GetVerb(), Equals, "delete", Commentf("Test case: %s", name))
			c.Assert(actions[0].GetResource().Resource, Equals, "pods", Commentf("Test case: %s", name))
		} else {
			c.Assert(actions, HasLen, 0, Commentf("Test case: %s - %s - should not delete pod", name, tc.description))
		}

		if stopFakeHealthServer != nil {
			stopFakeHealthServer()
		}
	}
}

func ptrTo[T any](v T) *T {
	return &v
}

// TestHandlePodDeletionForceDeletesOnV2EngineFrontendError verifies the v2
// EngineFrontend-death remount path force-deletes the workload pod (grace 0)
// instead of the default graceful 30s. A graceful termination against a dead
// dm-linear device parks in uninterruptible D-state (the workload tries to
// flush to a device that can never complete the I/O), so when the EF is
// confirmed in Error at delete time the pod must be SIGKILLed immediately.
// The negative case (EF Running) keeps the graceful 30s.
func (s *TestSuite) TestHandlePodDeletionForceDeletesOnV2EngineFrontendError(c *C) {
	datastore.SkipListerCheck = true
	defer func() {
		datastore.SkipListerCheck = false
	}()

	testCases := map[string]struct {
		efState         longhorn.InstanceState
		efExists        bool
		expectGraceZero bool
		description     string
	}{
		"EF in Error -> force-delete (grace 0)": {
			efState:         longhorn.InstanceStateError,
			efExists:        true,
			expectGraceZero: true,
			description:     "Dead dm-linear device; graceful flush would wedge in D-state",
		},
		"EF Running -> graceful (grace 30)": {
			efState:         longhorn.InstanceStateRunning,
			efExists:        true,
			expectGraceZero: false,
			description:     "Live device; let the workload flush before recreate",
		},
		"EF missing -> graceful (grace 30), fail-safe default": {
			efExists:        false,
			expectGraceZero: false,
			description:     "EF not found / lookup error -> device state unknown -> fall back to graceful (do not force-delete on ambiguous state)",
		},
	}

	for name, tc := range testCases {
		c.Logf("Running test case: %s", name)

		kubeClient := fake.NewSimpleClientset()
		lhClient := lhfake.NewSimpleClientset() //nolint:staticcheck
		extensionsClient := apiextensionsfake.NewSimpleClientset()
		informerFactories := util.NewInformerFactories(TestNamespace, kubeClient, lhClient, controller.NoResyncPeriodFunc())

		kc, err := newTestKubernetesPodController(lhClient, kubeClient, extensionsClient, informerFactories, TestNode1)
		c.Assert(err, IsNil)

		referenceTime := time.Now().UTC()
		baseTime := referenceTime.Add(-2 * time.Minute)
		remountRequestedAtTime := baseTime.Add(30 * time.Second)
		remountRequestedAt := remountRequestedAtTime.UTC().Format(time.RFC3339)
		podStartTime := metav1.NewTime(baseTime)

		vol := newVolume(TestVolumeName, 1)
		vol.Namespace = TestNamespace
		vol.Spec.DataEngine = longhorn.DataEngineTypeV2
		vol.Status.State = longhorn.VolumeStateAttached
		vol.Status.Robustness = longhorn.VolumeRobustnessFaulted
		vol.Status.CurrentNodeID = TestNode1
		vol.Status.RemountRequestedAt = remountRequestedAt

		ef := newEngineFrontendForVolume(vol, TestEngineName, TestNode1, "")
		ef.Status.CurrentState = tc.efState

		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: TestPVName},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:       types.LonghornDriverName,
						VolumeHandle: vol.Name,
					},
				},
			},
		}
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: TestPVCName, Namespace: TestNamespace},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name},
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestPod1,
				Namespace: TestNamespace,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: appsv1.SchemeGroupVersion.String(),
					Kind:       types.KubernetesKindDeployment,
					Name:       TestDeploymentName,
					Controller: ptrTo(true),
				}},
			},
			Spec: corev1.PodSpec{
				NodeName: TestNode1,
				Volumes: []corev1.Volume{{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name},
					},
				}},
			},
			Status: corev1.PodStatus{StartTime: &podStartTime},
		}

		autoDeleteSetting := newSetting(string(types.SettingNameAutoDeletePodWhenVolumeDetachedUnexpectedly), "true")
		c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Settings().Informer().GetIndexer().Add(autoDeleteSetting), IsNil)
		c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Volumes().Informer().GetIndexer().Add(vol), IsNil)
		if tc.efExists {
			c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().EngineFrontends().Informer().GetIndexer().Add(ef), IsNil)
		}
		c.Assert(informerFactories.KubeInformerFactory.Core().V1().PersistentVolumeClaims().Informer().GetIndexer().Add(pvc), IsNil)
		c.Assert(informerFactories.KubeInformerFactory.Core().V1().PersistentVolumes().Informer().GetIndexer().Add(pv), IsNil)
		c.Assert(informerFactories.KubeInformerFactory.Core().V1().Pods().Informer().GetIndexer().Add(pod), IsNil)

		_, err = lhClient.LonghornV1beta2().Settings(TestNamespace).Create(context.TODO(), autoDeleteSetting, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = lhClient.LonghornV1beta2().Volumes(TestNamespace).Create(context.TODO(), vol, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		if tc.efExists {
			_, err = lhClient.LonghornV1beta2().EngineFrontends(TestNamespace).Create(context.TODO(), ef, metav1.CreateOptions{})
			c.Assert(err, IsNil)
		}
		_, err = kubeClient.CoreV1().PersistentVolumeClaims(TestNamespace).Create(context.TODO(), pvc, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = kubeClient.CoreV1().PersistentVolumes().Create(context.TODO(), pv, metav1.CreateOptions{})
		c.Assert(err, IsNil)
		_, err = kubeClient.CoreV1().Pods(TestNamespace).Create(context.TODO(), pod, metav1.CreateOptions{})
		c.Assert(err, IsNil)

		kubeClient.ClearActions()
		c.Assert(kc.handlePodDeletionIfVolumeRequestRemount(pod), IsNil)

		actions := kubeClient.Actions()
		c.Assert(actions, HasLen, 1, Commentf("Test case: %s - %s", name, tc.description))
		c.Assert(actions[0].GetVerb(), Equals, "delete", Commentf("Test case: %s", name))
		c.Assert(actions[0].GetResource().Resource, Equals, "pods", Commentf("Test case: %s", name))

		deleteAction := actions[0].(ktesting.DeleteAction)
		opts := deleteAction.GetDeleteOptions()
		c.Assert(opts.GracePeriodSeconds, NotNil, Commentf("Test case: %s", name))
		if tc.expectGraceZero {
			c.Assert(*opts.GracePeriodSeconds, Equals, int64(0), Commentf("Test case: %s - %s", name, tc.description))
		} else {
			c.Assert(*opts.GracePeriodSeconds, Equals, int64(30), Commentf("Test case: %s - %s", name, tc.description))
		}
	}
}

// A dead v2 instance manager pod must fan out workload pod deletion to every
// attached v2 volume on its node in one pass — the per-volume kick cascade
// lands minutes late and causes doomed reattach cycles. Only
// controller-managed pods on the IM's node are kicked; bare pods, other
// nodes' volumes, and v1 volumes are left alone.
func (s *TestSuite) TestHandleWorkloadPodDeletionIfInstanceManagerPodIsDown(c *C) {
	datastore.SkipListerCheck = true
	defer func() { datastore.SkipListerCheck = false }()

	kubeClient := fake.NewSimpleClientset()
	lhClient := lhfake.NewSimpleClientset() //nolint:staticcheck
	extensionsClient := apiextensionsfake.NewSimpleClientset()
	informerFactories := util.NewInformerFactories(TestNamespace, kubeClient, lhClient, controller.NoResyncPeriodFunc())

	kc, err := newTestKubernetesPodController(lhClient, kubeClient, extensionsClient, informerFactories, TestNode1)
	c.Assert(err, IsNil)

	now := time.Now().UTC()
	imDeletion := metav1.NewTime(now)
	staleStart := metav1.NewTime(now.Add(-5 * time.Minute)) // running before the IM died -> kick
	freshStart := metav1.NewTime(now.Add(time.Second))      // started after the IM died -> replacement, no kick

	imPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "instance-manager-deadbeef",
			Namespace:         TestNamespace,
			UID:               "im-uid-1",
			DeletionTimestamp: &imDeletion,
			Labels: map[string]string{
				types.GetLonghornLabelComponentKey():                     types.LonghornLabelInstanceManager,
				types.GetLonghornLabelKey(types.LonghornLabelDataEngine): string(longhorn.DataEngineTypeV2),
			},
		},
		Spec: corev1.PodSpec{NodeName: TestNode1},
	}
	c.Assert(isV2InstanceManagerPod(imPod), Equals, true)

	mkVol := func(name string, engine longhorn.DataEngineType, node, podName string) *longhorn.Volume {
		v := newVolume(name, 1)
		v.Namespace = TestNamespace
		v.Spec.DataEngine = engine
		v.Spec.NodeID = node
		v.Status.State = longhorn.VolumeStateAttached
		v.Status.KubernetesStatus = longhorn.KubernetesStatus{
			Namespace:       TestNamespace,
			WorkloadsStatus: []longhorn.WorkloadStatus{{PodName: podName}},
		}
		return v
	}
	mkPod := func(name string, managed bool, start metav1.Time) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: TestNamespace},
			Spec:       corev1.PodSpec{NodeName: TestNode1},
			Status:     corev1.PodStatus{StartTime: &start},
		}
		if managed {
			p.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: appsv1.SchemeGroupVersion.String(),
				Kind:       types.KubernetesKindStatefulSet,
				Name:       "sts",
				Controller: ptrTo(true),
			}}
		}
		return p
	}

	vols := []*longhorn.Volume{
		mkVol("vol-kick", longhorn.DataEngineTypeV2, TestNode1, "pod-kick"),
		mkVol("vol-fresh", longhorn.DataEngineTypeV2, TestNode1, "pod-fresh"),
		mkVol("vol-bare", longhorn.DataEngineTypeV2, TestNode1, "pod-bare"),
		mkVol("vol-other", longhorn.DataEngineTypeV2, TestNode2, "pod-other"),
		mkVol("vol-v1", longhorn.DataEngineTypeV1, TestNode1, "pod-v1"),
	}
	pods := []*corev1.Pod{
		mkPod("pod-kick", true, staleStart),
		mkPod("pod-fresh", true, freshStart),
		mkPod("pod-bare", false, staleStart),
		mkPod("pod-other", true, staleStart),
		mkPod("pod-v1", true, staleStart),
	}

	autoDeleteSetting := newSetting(string(types.SettingNameAutoDeletePodWhenVolumeDetachedUnexpectedly), "true")
	c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Settings().Informer().GetIndexer().Add(autoDeleteSetting), IsNil)
	_, err = lhClient.LonghornV1beta2().Settings(TestNamespace).Create(context.TODO(), autoDeleteSetting, metav1.CreateOptions{})
	c.Assert(err, IsNil)
	for _, v := range vols {
		c.Assert(informerFactories.LhInformerFactory.Longhorn().V1beta2().Volumes().Informer().GetIndexer().Add(v), IsNil)
		_, err = lhClient.LonghornV1beta2().Volumes(TestNamespace).Create(context.TODO(), v, metav1.CreateOptions{})
		c.Assert(err, IsNil)
	}
	for _, p := range pods {
		_, err = kubeClient.CoreV1().Pods(TestNamespace).Create(context.TODO(), p, metav1.CreateOptions{})
		c.Assert(err, IsNil)
	}

	deletedNames := func() []string {
		kubeClient.ClearActions()
		c.Assert(kc.handleWorkloadPodDeletionIfInstanceManagerPodIsDown(imPod), IsNil)
		out := []string{}
		for _, a := range kubeClient.Actions() {
			if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
				out = append(out, a.(ktesting.DeleteAction).GetName())
			}
		}
		return out
	}

	// First fire: only the stale, managed, same-node, v2 pod is kicked.
	c.Assert(deletedNames(), DeepEquals, []string{"pod-kick"})

	// Same-death burst (the observed quirk): the pod controller reprocesses
	// the terminating IM pod key repeatedly. Re-firing with the SAME UID must
	// kick nothing — dedup by imPod.UID.
	for i := 0; i < 5; i++ {
		c.Assert(deletedNames(), HasLen, 0, Commentf("re-fire %d of same IM death must not re-kick", i))
	}

	// After the kick, the replacement pod-kick comes back fresh (started after
	// the death). Even a genuinely new IM death (different UID, which bypasses
	// dedup) must not kick the fresh replacement — the StartTime guard holds.
	freshReplacement := mkPod("pod-kick", true, metav1.NewTime(now.Add(2*time.Second)))
	_, err = kubeClient.CoreV1().Pods(TestNamespace).Create(context.TODO(), freshReplacement, metav1.CreateOptions{})
	c.Assert(err, IsNil)
	imPod2 := imPod.DeepCopy()
	imPod2.UID = "im-uid-2"
	kubeClient.ClearActions()
	c.Assert(kc.handleWorkloadPodDeletionIfInstanceManagerPodIsDown(imPod2), IsNil)
	got := []string{}
	for _, a := range kubeClient.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			got = append(got, a.(ktesting.DeleteAction).GetName())
		}
	}
	c.Assert(got, HasLen, 0, Commentf("a new IM death must not kick the fresh replacement pod"))

	// A live (non-deleting) IM pod is always a no-op.
	kubeClient.ClearActions()
	livePod := imPod.DeepCopy()
	livePod.UID = "im-uid-live"
	livePod.DeletionTimestamp = nil
	c.Assert(kc.handleWorkloadPodDeletionIfInstanceManagerPodIsDown(livePod), IsNil)
	c.Assert(kubeClient.Actions(), HasLen, 0)
}

// Regression for the .43 wiring gap: enqueuePodChange must admit v2 instance
// manager pods (which carry no Longhorn PVC and so are skipped by the PVC
// scan). Without this the fan-out handler is dead code — it was in .43 and
// only a live IM-kick test caught it.
func (s *TestSuite) TestEnqueuePodChangeAdmitsV2InstanceManagerPod(c *C) {
	datastore.SkipListerCheck = true
	defer func() { datastore.SkipListerCheck = false }()

	kubeClient := fake.NewSimpleClientset()
	lhClient := lhfake.NewSimpleClientset() //nolint:staticcheck
	extensionsClient := apiextensionsfake.NewSimpleClientset()
	informerFactories := util.NewInformerFactories(TestNamespace, kubeClient, lhClient, controller.NoResyncPeriodFunc())

	kc, err := newTestKubernetesPodController(lhClient, kubeClient, extensionsClient, informerFactories, TestNode1)
	c.Assert(err, IsNil)

	imPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-manager-abc",
			Namespace: TestNamespace,
			Labels: map[string]string{
				types.GetLonghornLabelComponentKey():                     types.LonghornLabelInstanceManager,
				types.GetLonghornLabelKey(types.LonghornLabelDataEngine): string(longhorn.DataEngineTypeV2),
			},
		},
		Spec: corev1.PodSpec{NodeName: TestNode1}, // same node as controllerID
	}

	c.Assert(kc.queue.Len(), Equals, 0)
	kc.enqueuePodChange(imPod)
	c.Assert(kc.queue.Len(), Equals, 1, Commentf("v2 IM pod on the controller's node must be enqueued"))

	// An IM pod on a different node must NOT be enqueued by this controller.
	imOther := imPod.DeepCopy()
	imOther.Name = "instance-manager-def"
	imOther.Spec.NodeName = TestNode2
	kc.enqueuePodChange(imOther)
	c.Assert(kc.queue.Len(), Equals, 1, Commentf("IM pod on another node must not be enqueued here"))
}
