package model

import (
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// AdminToken joins only the owner metadata needed by API key administration.
// Controllers project this internal record to an explicitly masked response.
type AdminToken struct {
	Token
	Username    string
	DisplayName string
	UserGroup   string
	UserRole    int
}

type AdminTokenOwner struct {
	UserID    int
	UserGroup string
	UserRole  int
}

func SearchAdminTokens(keyword string, userID int, group string, offset, limit int) ([]AdminToken, int64, error) {
	query := DB.Model(&Token{}).
		Joins("JOIN users ON users.id = tokens.user_id AND users.deleted_at IS NULL")
	if userID > 0 {
		query = query.Where("tokens.user_id = ?", userID)
	}
	escapeLike := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	if keyword != "" {
		pattern := "%" + escapeLike.Replace(strings.ToLower(keyword)) + "%"
		condition := DB.Where("LOWER(tokens.name) LIKE ? ESCAPE '!' OR LOWER(users.username) LIKE ? ESCAPE '!' OR LOWER(users.display_name) LIKE ? ESCAPE '!'", pattern, pattern, pattern)
		if id, err := strconv.Atoi(keyword); err == nil && id > 0 {
			condition = condition.Or("tokens.user_id = ?", id)
		}
		query = query.Where(condition)
	}
	if group != "" {
		encoded, err := common.Marshal(group)
		if err != nil {
			return nil, 0, err
		}
		// AutoGroups is canonical JSON. Match a complete encoded string, so a
		// renamed group is searchable without matching a longer group name.
		query = query.Where("tokens."+commonGroupCol+" = ? OR (tokens."+commonGroupCol+" = ? AND tokens.auto_groups LIKE ? ESCAPE '!')", group, "auto", "%"+escapeLike.Replace(string(encoded))+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	tokens := make([]AdminToken, 0)
	err := query.Select("tokens.id, tokens.user_id, tokens." + commonKeyCol + ", tokens.name, tokens.status, tokens.created_time, tokens.accessed_time, tokens.expired_time, tokens." + commonGroupCol + ", tokens.auto_groups, tokens.cross_group_retry, users.username, users.display_name, users." + commonGroupCol + " AS user_group, users.role AS user_role").
		Order("tokens.id DESC").Offset(max(offset, 0)).Limit(max(1, min(limit, 100))).Find(&tokens).Error
	return tokens, total, err
}

func GetAdminTokenOwners(ids []int) ([]AdminTokenOwner, error) {
	owners := make([]AdminTokenOwner, 0)
	err := DB.Model(&Token{}).
		Joins("JOIN users ON users.id = tokens.user_id AND users.deleted_at IS NULL").
		Select("tokens.user_id, users."+commonGroupCol+" AS user_group, users.role AS user_role").
		Where("tokens.id IN ?", ids).Find(&owners).Error
	if err == nil && len(owners) != len(ids) {
		err = common.NewMessage("Some API keys or their owners no longer exist")
	}
	return owners, err
}

// UpdateAdminTokenGroups validates every current owner under the same transaction
// as the three-column update. No token quota, status, secret or timestamps are saved.
func UpdateAdminTokenGroups(ids []int, group, autoGroups string, crossGroupRetry bool, validateOwner func(string, int) error) error {
	var tokens []Token
	err := DB.Transaction(func(tx *gorm.DB) error {
		var userIDs []int
		if err := tx.Model(&Token{}).Where("id IN ?", ids).Distinct().Pluck("user_id", &userIDs).Error; err != nil {
			return err
		}
		slices.Sort(userIDs)
		var users []User
		if err := lockForUpdate(tx).Select("id", commonGroupCol, "role").Where("id IN ?", userIDs).Order("id").Find(&users).Error; err != nil {
			return err
		}
		owners := make(map[int]User, len(users))
		for _, user := range users {
			if err := validateOwner(user.Group, user.Role); err != nil {
				return err
			}
			owners[user.Id] = user
		}
		if err := lockForUpdate(tx).Select("id", "user_id", commonKeyCol).Where("id IN ?", ids).Order("id").Find(&tokens).Error; err != nil {
			return err
		}
		if len(tokens) != len(ids) {
			return common.NewMessage("Some API keys or their owners no longer exist")
		}
		for _, token := range tokens {
			if _, exists := owners[token.UserId]; !exists {
				return common.NewMessage("Some API keys or their owners no longer exist")
			}
		}
		// Refuse the mutation if Redis cannot erect its fence. In-flight DB
		// readers must not republish the old routing scope after invalidation.
		if err := invalidateTokensCache(tokens); err != nil {
			return err
		}
		return tx.Model(&Token{}).Where("id IN ?", ids).Updates(map[string]any{
			"group": group, "auto_groups": autoGroups, "cross_group_retry": crossGroupRetry,
		}).Error
	})
	if err != nil {
		return err
	}
	// Refresh the fence after commit as well: a slow database transaction must
	// not leave a cache filled from its pre-commit view.
	if err := invalidateTokensCache(tokens); err != nil {
		// The transaction has committed. Report its actual outcome instead of
		// claiming failure after applying the requested groups. The pre-write
		// fence remains active; do not log keys or Redis error payloads.
		common.SysError(common.LogText("failed to refresh token cache fence after group update"))
	}
	return nil
}
