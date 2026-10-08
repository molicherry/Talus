package repository

import (
	"context"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const UsageLogFilterOptionsLimit = 50

// UsageLogFilterOption contains only entity labels needed to select a log
// filter. IDs are strings so large database IDs survive browser JSON decoding.
type UsageLogFilterOption struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Prefix  string `json:"prefix,omitempty"`
	Deleted bool   `json:"deleted"`
}

// FilterOptions reads bounded projections of entity tables. It never scans log
// history, credentials, or encrypted key material to assemble suggestions.
func (r *UsageLogRepo) FilterOptions(ctx context.Context, kind, text string) ([]UsageLogFilterOption, bool, error) {
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 128 || strings.ContainsRune(text, 0) {
		return nil, false, ErrUsageInvalid
	}
	query := r.db.WithContext(ctx)
	var nameColumn, selectColumns string
	switch kind {
	case "server":
		query = query.Model(&model.Server{}).Unscoped()
		nameColumn, selectColumns = "name", "id, name, deleted_at"
	case "user":
		query = query.Model(&model.User{}).Unscoped()
		nameColumn, selectColumns = "username", "id, username AS name, deleted_at"
	case "api_key":
		query = query.Model(&model.APIKey{})
		nameColumn, selectColumns = "name", "id, name, key_prefix"
	default:
		return nil, false, ErrUsageInvalid
	}

	var exactID uint64
	if text != "" && strings.Trim(text, "0123456789") == "" {
		// Entity IDs use native uint. A larger numeric search can still match
		// a name; it must never wrap or overflow PostgreSQL's signed bigint.
		if parsed, err := strconv.ParseUint(text, 10, strconv.IntSize); err == nil && parsed > 0 && parsed <= math.MaxInt64 {
			exactID = parsed
		}
	}
	if text != "" {
		// A custom escape character keeps backslashes literal too. The SQL
		// fragments and columns are fixed; the search pattern is a parameter.
		pattern := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(text) + "%"
		condition := nameColumn + " ILIKE ? ESCAPE '!'"
		values := []any{pattern}
		if kind == "api_key" {
			condition += " OR key_prefix ILIKE ? ESCAPE '!'"
			values = append(values, pattern)
		}
		if exactID != 0 {
			condition += " OR id = ?"
			values = append(values, exactID)
		}
		query = query.Where("("+condition+")", values...)
	}
	if exactID != 0 {
		query = query.Order(clause.OrderBy{Expression: clause.Expr{
			SQL:  "CASE WHEN id = ? THEN 0 ELSE 1 END, " + nameColumn + " ASC, id ASC",
			Vars: []any{exactID},
		}})
	} else {
		query = query.Order(nameColumn + " ASC").Order("id ASC")
	}
	var rows []struct {
		ID        uint64
		Name      string
		KeyPrefix string
		DeletedAt gorm.DeletedAt
	}
	if err := query.Select(selectColumns).Limit(UsageLogFilterOptionsLimit + 1).Scan(&rows).Error; err != nil {
		return nil, false, usageErr(err)
	}
	hasMore := len(rows) > UsageLogFilterOptionsLimit
	if hasMore {
		rows = rows[:UsageLogFilterOptionsLimit]
	}
	items := make([]UsageLogFilterOption, 0, len(rows))
	for _, row := range rows {
		if row.ID == 0 {
			continue
		}
		items = append(items, UsageLogFilterOption{
			ID: strconv.FormatUint(row.ID, 10), Name: row.Name,
			Prefix: row.KeyPrefix, Deleted: row.DeletedAt.Valid,
		})
	}
	return items, hasMore, nil
}
