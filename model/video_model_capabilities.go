package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const VideoModelCapabilitiesOptionKey = "video_model_setting.models"

// VideoModelCapabilities describes catalog hints for video creation. Omitted
// fields leave the client's provider defaults in place; an explicit reference
// limit of zero means that media type is unsupported. Relay validation remains
// the authority for requests and billing limits.
type VideoModelCapabilities struct {
	Resolutions        []string `json:"resolutions,omitempty"`
	AspectRatios       []string `json:"aspect_ratios,omitempty"`
	Durations          []int    `json:"durations,omitempty"`
	MaxDurationSeconds *int     `json:"max_duration_seconds,omitempty"`
	MaxReferenceImages *int     `json:"max_reference_images,omitempty"`
	MaxReferenceAudio  *int     `json:"max_reference_audio,omitempty"`
	MaxReferenceVideos *int     `json:"max_reference_videos,omitempty"`
}

type VideoModelCapabilitiesSnapshot struct {
	ModelName         string                  `json:"model_name"`
	VideoCapabilities *VideoModelCapabilities `json:"video_capabilities"`
	Version           string                  `json:"version"`
}

var (
	ErrVideoModelCapabilitiesConflict = errors.New("Video model capabilities changed. Reload before saving.")
	videoModelCapabilitiesMutationMu  sync.Mutex
	videoResolutionPattern            = regexp.MustCompile(`^[0-9]+[pP]?$`)
	videoAspectRatioPattern           = regexp.MustCompile(`^(auto|[1-9][0-9]*:[1-9][0-9]*)$`)
)

func normalizeVideoModelCapabilities(value *VideoModelCapabilities) (*VideoModelCapabilities, error) {
	if value == nil {
		return nil, nil
	}
	normalized := *value
	normalized.Resolutions = slices.Clone(value.Resolutions)
	normalized.AspectRatios = slices.Clone(value.AspectRatios)
	value = &normalized
	for i, resolution := range value.Resolutions {
		resolution = strings.TrimSpace(resolution)
		if !videoResolutionPattern.MatchString(resolution) {
			return nil, errors.New("Video resolutions must be whole numbers between 1 and 16384, optionally followed by p.")
		}
		number, err := strconv.Atoi(strings.TrimRight(resolution, "pP"))
		if err != nil || number < 1 || number > 16384 {
			return nil, errors.New("Video resolutions must be whole numbers between 1 and 16384, optionally followed by p.")
		}
		value.Resolutions[i] = strconv.Itoa(number) + "p"
	}
	for i, ratio := range value.AspectRatios {
		ratio = strings.TrimSpace(ratio)
		if !videoAspectRatioPattern.MatchString(ratio) {
			return nil, errors.New("Video aspect ratios must be auto or positive whole numbers in W:H format.")
		}
		value.AspectRatios[i] = ratio
	}
	for _, values := range [][]string{value.Resolutions, value.AspectRatios} {
		if len(values) > 64 {
			return nil, errors.New("Video capability lists support at most 64 options.")
		}
		seen := make(map[string]bool, len(values))
		for _, text := range values {
			if strings.TrimSpace(text) == "" || len(text) > 64 || seen[text] {
				return nil, errors.New("Video capability options must be unique, non-empty strings of at most 64 characters.")
			}
			seen[text] = true
		}
	}
	if value.MaxDurationSeconds != nil && (*value.MaxDurationSeconds <= 0 || *value.MaxDurationSeconds > relaycommon.MaxTaskDurationSeconds) {
		return nil, fmt.Errorf("Video duration must be between 1 and %d seconds.", relaycommon.MaxTaskDurationSeconds)
	}
	if len(value.Durations) > 64 {
		return nil, errors.New("Video capability lists support at most 64 options.")
	}
	seenDurations := make(map[int]bool, len(value.Durations))
	for _, duration := range value.Durations {
		if duration <= 0 || duration > relaycommon.MaxTaskDurationSeconds || value.MaxDurationSeconds != nil && duration > *value.MaxDurationSeconds || seenDurations[duration] {
			return nil, errors.New("Video durations must be unique positive seconds within the configured maximum.")
		}
		seenDurations[duration] = true
	}
	for _, count := range []*int{value.MaxReferenceImages, value.MaxReferenceAudio, value.MaxReferenceVideos} {
		if count != nil && (*count < 0 || *count > 100) {
			return nil, errors.New("Video reference limits must be between 0 and 100.")
		}
	}
	return value, nil
}

func videoModelCapabilitiesSnapshot(name string, values map[string]VideoModelCapabilities) VideoModelCapabilitiesSnapshot {
	snapshot := VideoModelCapabilitiesSnapshot{ModelName: name}
	if value, exists := values[name]; exists {
		snapshot.VideoCapabilities = &value
	}
	encoded, _ := common.Marshal(snapshot.VideoCapabilities)
	snapshot.Version = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return snapshot
}

func parseVideoModelCapabilities(raw string) (map[string]VideoModelCapabilities, error) {
	values := make(map[string]VideoModelCapabilities)
	if raw == "" {
		return values, nil
	}
	if err := common.UnmarshalJsonStr(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("Video model capabilities must be a JSON object.")
	}
	return values, nil
}

func GetVideoModelCapabilities(name string) (VideoModelCapabilitiesSnapshot, error) {
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return VideoModelCapabilitiesSnapshot{}, errors.New("A model name of at most 128 characters is required.")
	}
	var option Option
	err := DB.Where(commonKeyCol+" = ?", VideoModelCapabilitiesOptionKey).Take(&option).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return VideoModelCapabilitiesSnapshot{}, err
	}
	values, err := parseVideoModelCapabilities(option.Value)
	if err != nil {
		return VideoModelCapabilitiesSnapshot{}, err
	}
	return videoModelCapabilitiesSnapshot(name, values), nil
}

// UpdateVideoModelCapabilities merges one model into the locked persisted map.
// The version only covers that model, so independent model edits can succeed.
func UpdateVideoModelCapabilities(name string, value *VideoModelCapabilities, expectedVersion string) (VideoModelCapabilitiesSnapshot, error) {
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return VideoModelCapabilitiesSnapshot{}, errors.New("A model name of at most 128 characters is required.")
	}
	if expectedVersion == "" {
		return VideoModelCapabilitiesSnapshot{}, ErrVideoModelCapabilitiesConflict
	}
	value, err := normalizeVideoModelCapabilities(value)
	if err != nil {
		return VideoModelCapabilitiesSnapshot{}, err
	}
	videoModelCapabilitiesMutationMu.Lock()
	defer videoModelCapabilitiesMutationMu.Unlock()
	var snapshot VideoModelCapabilitiesSnapshot
	var committed string
	err = DB.Transaction(func(tx *gorm.DB) error {
		option := Option{Key: VideoModelCapabilitiesOptionKey, Value: "{}"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&option).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where(commonKeyCol+" = ?", option.Key).Take(&option).Error; err != nil {
			return err
		}
		values, err := parseVideoModelCapabilities(option.Value)
		if err != nil {
			return err
		}
		if videoModelCapabilitiesSnapshot(name, values).Version != expectedVersion {
			return ErrVideoModelCapabilitiesConflict
		}
		if value == nil {
			delete(values, name)
		} else {
			values[name] = *value
		}
		encoded, err := common.Marshal(values)
		if err != nil {
			return err
		}
		committed = string(encoded)
		if err := tx.Model(&Option{}).Where(commonKeyCol+" = ?", option.Key).Update("value", committed).Error; err != nil {
			return err
		}
		snapshot = videoModelCapabilitiesSnapshot(name, values)
		return nil
	})
	if err != nil {
		return VideoModelCapabilitiesSnapshot{}, err
	}
	if err := updateOptionMap(VideoModelCapabilitiesOptionKey, committed); err != nil {
		return VideoModelCapabilitiesSnapshot{}, err
	}
	RefreshPricing()
	return snapshot, nil
}
