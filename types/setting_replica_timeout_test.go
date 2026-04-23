package types

import (
	. "gopkg.in/check.v1"
)

// These tests pin the contract between longhorn-manager and the
// LONGHORN_V2_REPLICA_* env vars that longhorn-spdk-engine reads at startup.
// If any of these names or defaults drift without a matching change in the
// spdk-engine repo, these tests fail loudly before the image ships.

var replicaTimeoutSettings = []struct {
	name    SettingName
	envVar  string
	defVal  string
	min     int
	hasMax  bool
	max     int
	settingType SettingType
}{
	{SettingNameDataEngineReplicaCtrlrLossTimeoutSec, EnvV2ReplicaCtrlrLossTimeoutSec, "15", 0, false, 0, SettingTypeInt},
	{SettingNameDataEngineReplicaFastIOFailTimeoutSec, EnvV2ReplicaFastIOFailTimeoutSec, "10", 0, false, 0, SettingTypeInt},
	{SettingNameDataEngineReplicaReconnectDelaySec, EnvV2ReplicaReconnectDelaySec, "2", 0, false, 0, SettingTypeInt},
	{SettingNameDataEngineReplicaTransportAckTimeout, EnvV2ReplicaTransportAckTimeout, "10", 0, true, 31, SettingTypeInt},
	{SettingNameDataEngineReplicaKeepAliveTimeoutMs, EnvV2ReplicaKeepAliveTimeoutMs, "10000", 0, false, 0, SettingTypeInt},
}

func (s *TestSuite) TestReplicaTimeoutSettingsAreDangerZoneAndV2Specific(c *C) {
	for _, t := range replicaTimeoutSettings {
		def, ok := GetSettingDefinition(t.name)
		c.Assert(ok, Equals, true, Commentf("setting %s not registered", t.name))
		c.Assert(def.Category, Equals, SettingCategoryDangerZone, Commentf("setting %s must be danger-zone", t.name))
		c.Assert(def.DataEngineSpecific, Equals, true, Commentf("setting %s must be DataEngineSpecific", t.name))
		c.Assert(def.Required, Equals, true, Commentf("setting %s must be Required", t.name))
		c.Assert(def.Type, Equals, t.settingType, Commentf("setting %s type mismatch", t.name))
	}
}

func (s *TestSuite) TestReplicaTimeoutSettingDefaults(c *C) {
	for _, t := range replicaTimeoutSettings {
		def, ok := GetSettingDefinition(t.name)
		c.Assert(ok, Equals, true)
		// Defaults are JSON-shaped because DataEngineSpecific=true.
		c.Assert(def.Default, Equals, `{"v2":"`+t.defVal+`"}`,
			Commentf("setting %s default drifted — spdk-engine's init() assumes this value", t.name))
	}
}

func (s *TestSuite) TestReplicaTimeoutSettingValueRanges(c *C) {
	for _, t := range replicaTimeoutSettings {
		def, ok := GetSettingDefinition(t.name)
		c.Assert(ok, Equals, true)
		c.Assert(def.ValueIntRange, NotNil, Commentf("setting %s must declare ValueIntRange", t.name))
		c.Assert(def.ValueIntRange[ValueIntRangeMinimum], Equals, t.min, Commentf("setting %s min mismatch", t.name))
		if t.hasMax {
			max, present := def.ValueIntRange[ValueIntRangeMaximum]
			c.Assert(present, Equals, true, Commentf("setting %s missing max", t.name))
			c.Assert(max, Equals, t.max, Commentf("setting %s max mismatch", t.name))
		}
	}
}

func (s *TestSuite) TestReplicaTimeoutSettingsInSettingNameList(c *C) {
	inList := map[SettingName]bool{}
	for _, n := range SettingNameList {
		inList[n] = true
	}
	for _, t := range replicaTimeoutSettings {
		c.Assert(inList[t.name], Equals, true,
			Commentf("setting %s is registered via GetSettingDefinition but missing from SettingNameList — CRs won't be created", t.name))
	}
}

func (s *TestSuite) TestReplicaTimeoutEnvVarsDistinct(c *C) {
	// The spdk-engine init() path reads five distinct env vars; a copy-paste
	// error that collapses two into the same name would silently drop a
	// tunable. Enforce uniqueness here so it can't regress.
	seen := map[string]SettingName{}
	for _, t := range replicaTimeoutSettings {
		existing, dup := seen[t.envVar]
		c.Assert(dup, Equals, false,
			Commentf("env var %s bound to both %s and %s", t.envVar, existing, t.name))
		seen[t.envVar] = t.name
	}
	c.Assert(len(seen), Equals, 5)
}
