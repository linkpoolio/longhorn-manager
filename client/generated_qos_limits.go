package client

const (
	QOS_LIMITS_TYPE = "qosLimits"
)

// QosLimits maps to SPDK's bdev_set_qos_limit parameters one-to-one. SPDK
// enforces each as a separate token bucket; clients can mix-and-match
// (e.g. cap aggregate IOPS but only writes for bandwidth).
type QosLimits struct {
	RwIOsPerSec int64 `json:"rwIOsPerSec,omitempty" yaml:"rw_ios_per_sec,omitempty"`
	RwMBPerSec  int64 `json:"rwMBPerSec,omitempty" yaml:"rw_mb_per_sec,omitempty"`
	RMBPerSec   int64 `json:"rMBPerSec,omitempty" yaml:"r_mb_per_sec,omitempty"`
	WMBPerSec   int64 `json:"wMBPerSec,omitempty" yaml:"w_mb_per_sec,omitempty"`
}
