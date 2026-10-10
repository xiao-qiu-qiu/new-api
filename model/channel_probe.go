package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const ChannelProbeHistoryLimit = 60

// ChannelProbe schedules a channel or group with one or more models. Internal
// scheduling fields never enter the management or public JSON contract.
type ChannelProbe struct {
	ID              int      `json:"id" gorm:"primaryKey"`
	ChannelID       int      `json:"channel_id" gorm:"index"`
	Model           string   `json:"model" gorm:"type:varchar(191)"`
	Scope           string   `json:"scope" gorm:"type:varchar(16)"`
	GroupName       string   `json:"group_name" gorm:"type:varchar(64)"`
	Models          []string `json:"models" gorm:"serializer:json;type:text"`
	SortOrder       int      `json:"sort_order" gorm:"index"`
	IdentityKey     *string  `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_channel_probe_identity"`
	HistoryVersion  int      `json:"-"`
	PublicName      string   `json:"public_name" gorm:"type:varchar(128)"`
	Enabled         bool     `json:"enabled"`
	Public          bool     `json:"public"`
	ActiveEnabled   bool     `json:"active_enabled"`
	PassiveEnabled  bool     `json:"passive_enabled"`
	IntervalSeconds int      `json:"interval_seconds"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	GreenMS         int64    `json:"green_ms"`
	YellowMS        int64    `json:"yellow_ms"`
	Revision        int64    `json:"-"`
	NextRunAt       int64    `json:"-" gorm:"index"`
	LeaseUntil      int64    `json:"-"`
	LeaseToken      string   `json:"-" gorm:"type:varchar(36)"`
}

// No upstream strings, URLs, credentials, or response content are stored here.
type ChannelProbeResult struct {
	ID        int64  `json:"-" gorm:"primaryKey"`
	ProbeID   int    `json:"-" gorm:"index:idx_probe_history,priority:1;index:idx_probe_model_history,priority:1"`
	Model     string `json:"-" gorm:"type:varchar(191);index:idx_probe_model_history,priority:2"`
	CheckedAt int64  `json:"checked_at" gorm:"index;index:idx_probe_history,priority:2;index:idx_probe_model_history,priority:3"`
	Source    string `json:"source" gorm:"type:varchar(16)"`
	Success   bool   `json:"success"`
	TTFTMS    *int64 `json:"ttft_ms"`
	LatencyMS int64  `json:"latency_ms"`
	Status    string `json:"status" gorm:"type:varchar(16)"`
	ErrorCode string `json:"error_code" gorm:"type:varchar(32)"`
}

func ListChannelProbeConfigs() ([]ChannelProbe, error) {
	probes := make([]ChannelProbe, 0)
	err := DB.Order("sort_order asc, id asc").Find(&probes).Error
	return probes, err
}

func GetChannelProbe(id int) (ChannelProbe, error) {
	var probe ChannelProbe
	err := DB.First(&probe, id).Error
	return probe, err
}

func SaveChannelProbe(probe *ChannelProbe, create bool) error {
	probe.Normalize()
	identity := probe.identity()
	probe.IdentityKey = &identity
	probe.Revision = time.Now().UnixNano()
	return DB.Transaction(func(tx *gorm.DB) error {
		if create {
			probe.ID = 0
			probe.HistoryVersion = 1
			return tx.Create(probe).Error
		}
		var previous ChannelProbe
		if err := lockForUpdate(tx).First(&previous, probe.ID).Error; err != nil {
			return err
		}
		// Editing does not release a running lease. Late observations carry the
		// old revision and are ignored instead of appearing under a new model.
		probe.LeaseUntil = previous.LeaseUntil
		probe.LeaseToken = previous.LeaseToken
		probe.HistoryVersion = previous.HistoryVersion
		modelsChanged := !slices.Equal(previous.Models, probe.Models)
		targetChanged := previous.Scope != probe.Scope || previous.ChannelID != probe.ChannelID || previous.GroupName != probe.GroupName
		if err := tx.Model(&previous).Select("*").Updates(probe).Error; err != nil {
			return err
		}
		if targetChanged {
			if err := tx.Where("probe_id = ?", probe.ID).Delete(&ChannelProbeMinute{}).Error; err != nil {
				return err
			}
			return tx.Where("probe_id = ?", probe.ID).Delete(&ChannelProbeResult{}).Error
		}
		// Adding a model preserves all retained models' observations. Removing
		// one deletes only its raw and aggregate rows, so re-adding starts fresh.
		if modelsChanged {
			if err := tx.Where("probe_id = ? AND model NOT IN ?", probe.ID, probe.Models).Delete(&ChannelProbeMinute{}).Error; err != nil {
				return err
			}
			return tx.Where("probe_id = ? AND model NOT IN ?", probe.ID, probe.Models).Delete(&ChannelProbeResult{}).Error
		}
		return nil
	})
}

func DeleteChannelProbe(id int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var probe ChannelProbe
		if err := lockForUpdate(tx).First(&probe, id).Error; err != nil {
			return err
		}
		if err := tx.Where("probe_id = ?", id).Delete(&ChannelProbeResult{}).Error; err != nil {
			return err
		}
		if err := tx.Where("probe_id = ?", id).Delete(&ChannelProbeMinute{}).Error; err != nil {
			return err
		}
		return tx.Delete(&probe).Error
	})
}

func GetChannelProbeHistory(id int, models ...string) ([]ChannelProbeResult, error) {
	history := make([]ChannelProbeResult, 0)
	query := DB.Where("probe_id = ?", id)
	if len(models) > 0 {
		query = query.Where("model = ?", models[0])
	}
	err := query.Order("checked_at desc, id desc").Limit(ChannelProbeHistoryLimit).Find(&history).Error
	return history, err
}

// The shared database lease prevents overlapping probes across master nodes.
func ClaimChannelProbe(probe *ChannelProbe, manual bool) (bool, error) {
	now := time.Now().Unix()
	probe.LeaseToken = uuid.NewString()
	query := DB.Model(&ChannelProbe{}).Where("id = ? AND revision = ? AND enabled = ? AND active_enabled = ? AND lease_until <= ?", probe.ID, probe.Revision, true, true, now)
	if !manual {
		query = query.Where("next_run_at <= ?", now)
	}
	result := query.Updates(map[string]any{"lease_until": now + int64(probe.TimeoutSeconds) + 30, "lease_token": probe.LeaseToken, "next_run_at": now + int64(probe.IntervalSeconds)})
	return result.RowsAffected == 1, result.Error
}

func ReleaseChannelProbe(probe ChannelProbe) error {
	return DB.Model(&ChannelProbe{}).Where("id = ? AND lease_token = ?", probe.ID, probe.LeaseToken).Update("lease_until", 0).Error
}

func RenewChannelProbeLease(probe ChannelProbe) (bool, error) {
	result := DB.Model(&ChannelProbe{}).Where("id = ? AND lease_token = ?", probe.ID, probe.LeaseToken).
		Update("lease_until", time.Now().Unix()+int64(probe.TimeoutSeconds)+30)
	return result.RowsAffected == 1, result.Error
}

func StoreChannelProbeResult(probe ChannelProbe, result ChannelProbeResult) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current ChannelProbe
		if err := lockForUpdate(tx).First(&current, probe.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if !current.Enabled || current.Revision != probe.Revision || (result.Source == "passive" && !current.PassiveEnabled) || (result.Source == "active" && current.LeaseToken != probe.LeaseToken) {
			return nil
		}
		if result.Model == "" {
			result.Model = probe.Model
		}
		if !slices.Contains(current.Models, result.Model) {
			return nil
		}
		result.ID, result.ProbeID = 0, probe.ID
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		if err := addChannelProbeMinute(tx, result); err != nil {
			return err
		}
		// Fetch the cutoff separately: MySQL 5.7 does not support LIMIT inside
		// an IN subquery. Use completion time for both display and retention:
		// an older observation can arrive later after waiting for persistence.
		var cutoff []ChannelProbeResult
		if err := tx.Select("id", "checked_at").Where("probe_id = ? AND model = ?", probe.ID, result.Model).Order("checked_at desc, id desc").Offset(ChannelProbeHistoryLimit - 1).Limit(1).Find(&cutoff).Error; err != nil {
			return err
		}
		if len(cutoff) == 1 {
			return tx.Where("probe_id = ? AND model = ? AND (checked_at < ? OR (checked_at = ? AND id < ?))", probe.ID, result.Model, cutoff[0].CheckedAt, cutoff[0].CheckedAt, cutoff[0].ID).Delete(&ChannelProbeResult{}).Error
		}
		return nil
	})
}

func CleanupChannelProbeResults() error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	if err := DB.Where("minute_at < ?", cutoff).Delete(&ChannelProbeMinute{}).Error; err != nil {
		return err
	}
	return DB.Where("checked_at < ?", cutoff).Delete(&ChannelProbeResult{}).Error
}

// Normalize keeps pre-multi-model rows and callers using Model readable.
func (p *ChannelProbe) Normalize() {
	if p.Scope == "" {
		p.Scope = "upstream"
	}
	if len(p.Models) == 0 && p.Model != "" {
		p.Models = []string{p.Model}
	}
	if len(p.Models) > 0 {
		p.Model = p.Models[0]
	}
}
func (p *ChannelProbe) AfterFind(tx *gorm.DB) error { p.Normalize(); return nil }

// Minute aggregates bound storage independently of traffic. Updates occur while
// holding the parent probe row lock, the same serialization as raw retention.
type ChannelProbeMinute struct {
	ProbeID      int    `gorm:"primaryKey;autoIncrement:false"`
	Model        string `gorm:"primaryKey;type:varchar(191)"`
	MinuteAt     int64  `gorm:"primaryKey;autoIncrement:false;index"`
	Count        int64
	SuccessCount int64
	TimeoutCount int64
	TTFTCount    int64
	TTFTSum      int64
}

func addChannelProbeMinute(tx *gorm.DB, result ChannelProbeResult) error {
	key := ChannelProbeMinute{ProbeID: result.ProbeID, Model: result.Model, MinuteAt: result.CheckedAt / 60 * 60}
	var bucket ChannelProbeMinute
	lookup := tx.Where(&key).Limit(1).Find(&bucket)
	if lookup.Error != nil {
		return lookup.Error
	}
	if lookup.RowsAffected == 0 {
		bucket = key
	}
	bucket.Count++
	if result.Success {
		bucket.SuccessCount++
	}
	if result.ErrorCode == "timeout" {
		bucket.TimeoutCount++
	}
	if result.TTFTMS != nil && *result.TTFTMS >= 0 {
		bucket.TTFTCount++
		bucket.TTFTSum += *result.TTFTMS
	}
	if lookup.RowsAffected == 0 {
		return tx.Create(&bucket).Error
	}
	return tx.Model(&ChannelProbeMinute{}).Where(&key).Select("count", "success_count", "timeout_count", "ttft_count", "ttft_sum").Updates(&bucket).Error
}
func GetChannelProbeMinutes(id int, modelName string, start, end int64) ([]ChannelProbeMinute, error) {
	var rows []ChannelProbeMinute
	err := DB.Where("probe_id = ? AND model = ? AND minute_at >= ? AND minute_at < ?", id, modelName, start, end).Order("minute_at asc").Find(&rows).Error
	return rows, err
}

// MigrateChannelProbes also upgrades the previously released single-model schema.
// HistoryVersion makes importing retained raw observations exactly-once.
func MigrateChannelProbes(db *gorm.DB) error {
	initCol()
	if err := db.AutoMigrate(&ChannelProbe{}, &ChannelProbeResult{}, &ChannelProbeMinute{}); err != nil {
		return err
	}
	if db.Migrator().HasIndex(&ChannelProbe{}, "idx_channel_probe_model") {
		if err := db.Migrator().DropIndex(&ChannelProbe{}, "idx_channel_probe_model"); err != nil {
			return err
		}
	}
	// Newly added integer columns are NULL for old rows. Normalize the
	// default so PostgreSQL and SQLite/MySQL agree on ordering after upgrade.
	if err := db.Model(&ChannelProbe{}).Where("sort_order IS NULL").Update("sort_order", 0).Error; err != nil {
		return err
	}
	var pending []ChannelProbe
	if err := db.Where("history_version IS NULL OR history_version = ? OR identity_key IS NULL", 0).Find(&pending).Error; err != nil {
		return err
	}
	for _, probe := range pending {
		if err := db.Transaction(func(tx *gorm.DB) error {
			var current ChannelProbe
			if err := lockForUpdate(tx).First(&current, probe.ID).Error; err != nil {
				return err
			}
			if current.HistoryVersion >= 1 && current.IdentityKey != nil {
				return nil
			}
			var rows []ChannelProbeResult
			if err := tx.Where("probe_id = ? AND ? < 1", probe.ID, current.HistoryVersion).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if row.Model == "" {
					row.Model = current.Model
					if err := tx.Model(&ChannelProbeResult{}).Where("id = ?", row.ID).Update("model", row.Model).Error; err != nil {
						return err
					}
				}
				if err := addChannelProbeMinute(tx, row); err != nil {
					return err
				}
			}
			current.HistoryVersion = 1
			identity := current.identity()
			current.IdentityKey = &identity
			return tx.Model(&current).Select("scope", "models", "history_version", "identity_key").Updates(&current).Error
		}); err != nil {
			return err
		}
	}
	return nil
}

func OrderChannelProbes(ids []int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var probes []ChannelProbe
		if err := lockForUpdate(tx).Order("id asc").Find(&probes).Error; err != nil {
			return err
		}
		if len(ids) != len(probes) {
			return errors.New("order must include every current probe exactly once")
		}
		expected := make(map[int]bool, len(probes))
		for _, p := range probes {
			expected[p.ID] = true
		}
		for order, id := range ids {
			if !expected[id] {
				return errors.New("order contains an unknown or repeated probe")
			}
			delete(expected, id)
			if err := tx.Model(&ChannelProbe{}).Where("id = ?", id).Update("sort_order", order).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

type ChannelProbeGroup struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

func ListChannelProbeGroups() ([]ChannelProbeGroup, error) {
	var abilities []Ability
	err := DB.Model(&Ability{}).Joins("JOIN channels ON channels.id = abilities.channel_id").Where("abilities.enabled = ? AND channels.status = ?", true, common.ChannelStatusEnabled).Find(&abilities).Error
	if err != nil {
		return nil, err
	}
	groups := make(map[string]map[string]bool)
	for _, a := range abilities {
		if groups[a.Group] == nil {
			groups[a.Group] = make(map[string]bool)
		}
		groups[a.Group][a.Model] = true
	}
	result := make([]ChannelProbeGroup, 0, len(groups))
	for name, models := range groups {
		row := ChannelProbeGroup{Name: name, Models: make([]string, 0, len(models))}
		for name := range models {
			row.Models = append(row.Models, name)
		}
		sort.Strings(row.Models)
		result = append(result, row)
	}
	slices.SortFunc(result, func(a, b ChannelProbeGroup) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return result, nil
}

// Exact target/model-set duplicates add identical public rows and duplicate
// billable upstream traffic. Model ordering itself does not change identity.
func (p ChannelProbe) identity() string {
	p.Normalize()
	names := slices.Clone(p.Models)
	sort.Strings(names)
	encoded, _ := common.Marshal(struct {
		Scope     string
		ChannelID int
		Group     string
		Models    []string
	}{p.Scope, p.ChannelID, p.GroupName, names})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
