package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistillationGuardPolicyIsUpdatedAtomically(t *testing.T) {
	key := operation_setting.DistillationGuardOption
	previous := operation_setting.GetDistillationGuard()
	previousValue, exists := common.OptionMap[key]
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
		t.Cleanup(func() { common.OptionMap = nil })
	}
	t.Cleanup(func() {
		operation_setting.SetDistillationGuard(previous)
		if exists {
			common.OptionMap[key] = previousValue
		} else {
			delete(common.OptionMap, key)
		}
	})
	policy := operation_setting.DefaultDistillationGuard()
	policy.BlockMinutes = 7
	encoded, err := common.Marshal(policy)
	require.NoError(t, err)
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useSystemTaskMatrixDB(t, dialect)
			require.NoError(t, db.AutoMigrate(&Option{}))
			require.NoError(t, UpdateOption(key, string(encoded)))
			var saved Option
			require.NoError(t, db.First(&saved, &Option{Key: key}).Error)
			assert.Equal(t, string(encoded), saved.Value)
			require.NoError(t, db.Migrator().DropTable(&Option{}))
			changed := policy
			changed.Enabled = false
			raw, err := common.Marshal(changed)
			require.NoError(t, err)
			assert.Error(t, UpdateOption(key, string(raw)))
			assert.Equal(t, policy, operation_setting.GetDistillationGuard())
		})
	}
	policy.ConsecutiveRequests = 0
	bad, err := common.Marshal(policy)
	require.NoError(t, err)
	assert.Error(t, validateOptionValue(key, string(bad)))
	assert.Error(t, updateOptionMap(key, string(bad)))
	assert.Equal(t, 7, operation_setting.GetDistillationGuard().BlockMinutes)
	assert.Equal(t, 10, operation_setting.GetDistillationGuard().ConsecutiveRequests)
	assert.Equal(t, string(encoded), common.OptionMap[key])
}

func TestValidateOptionValueRejectsInvalidMaxTokenAutoGroups(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue("MaxTokenAutoGroups", value))
		})
	}
	require.NoError(t, validateOptionValue("MaxTokenAutoGroups", "999999"))
}

func TestValidateOptionValueRejectsSystemTaskRetentionBelowOneDay(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue("system_task_setting.retention_days", value))
		})
	}
	require.NoError(t, validateOptionValue("system_task_setting.retention_days", "1"))
	require.NoError(t, validateOptionValue("system_task_setting.retention_days", "30"))
}
