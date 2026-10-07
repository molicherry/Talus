package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func filterOptionsTestDB(t *testing.T) (*gorm.DB, *UsageLogRepo) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Server{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(&model.UsageLog{}) {
		t.Fatal("filter options must be testable without a usage_logs table")
	}
	return db, NewUsageLogRepo(db)
}

func insertFilterOption(t *testing.T, db *gorm.DB, kind string, id uint, name, prefix string) {
	t.Helper()
	var entity any
	switch kind {
	case "server":
		entity = &model.Server{BaseModel: model.BaseModel{ID: id}, Name: name, Host: "private-host.example", OwnerID: 1}
	case "user":
		entity = &model.User{BaseModel: model.BaseModel{ID: id}, Username: name, PasswordHash: "private-password-hash", Role: "admin"}
	case "api_key":
		entity = &model.APIKey{ID: id, Name: name, KeyPrefix: prefix, KeyHash: fmt.Sprintf("private-key-hash-%d", id), UserID: 1, EncryptedRawKey: "private-encrypted-key", Salt: []byte("private-salt")}
	default:
		t.Fatalf("unsupported fixture kind %q", kind)
	}
	if err := db.Create(entity).Error; err != nil {
		t.Fatal(err)
	}
}

func requireFilterOptions(t *testing.T, repo *UsageLogRepo, kind, q string) ([]UsageLogFilterOption, bool) {
	t.Helper()
	options, more, err := repo.FilterOptions(context.Background(), kind, q)
	if err != nil {
		t.Fatalf("FilterOptions(%q, %q): %v", kind, q, err)
	}
	return options, more
}

func TestUsageFilterOptionsSoftDeletedEntitiesAndHardDeletedKeys(t *testing.T) {
	db, repo := filterOptionsTestDB(t)
	for _, kind := range []string{"server", "user", "api_key"} {
		insertFilterOption(t, db, kind, 11, "current", "current-prefix")
		insertFilterOption(t, db, kind, 12, "removed", "removed-prefix")
	}
	if err := db.Delete(&model.Server{}, 12).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.User{}, 12).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.APIKey{}, 12).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"server", "user"} {
		options, more := requireFilterOptions(t, repo, kind, "")
		if more || len(options) != 2 {
			t.Fatalf("%s: options=%+v, has_more=%v", kind, options, more)
		}
		seen := map[string]UsageLogFilterOption{}
		for _, option := range options {
			seen[option.ID] = option
			if option.Prefix != "" {
				t.Fatalf("%s unexpectedly exposes a key prefix: %+v", kind, option)
			}
		}
		if seen["11"].Name != "current" || seen["11"].Deleted || seen["12"].Name != "removed" || !seen["12"].Deleted {
			t.Fatalf("%s: soft deletion information lost: %+v", kind, options)
		}
	}
	options, more := requireFilterOptions(t, repo, "api_key", "")
	if more || len(options) != 1 || options[0].ID != "11" || options[0].Name != "current" || options[0].Prefix != "current-prefix" || options[0].Deleted {
		t.Fatalf("hard-deleted key exposed or surviving prefix missing: %+v, has_more=%v", options, more)
	}
}

func TestUsageFilterOptionsLiteralUnicodeAndPrefixSearch(t *testing.T) {
	for _, kind := range []string{"server", "user", "api_key"} {
		t.Run(kind, func(t *testing.T) {
			db, repo := filterOptionsTestDB(t)
			insertFilterOption(t, db, kind, 1, `资源_100%\west!`, "plain")
			insertFilterOption(t, db, kind, 2, "资源X100YxwestZ", "plain")
			insertFilterOption(t, db, kind, 3, "MixedCase", "plain")
			for _, q := range []string{`  资源_100%\west! `, "资源_", "%", "_", `\`, "!"} {
				options, more := requireFilterOptions(t, repo, kind, q)
				if more || len(options) != 1 || options[0].ID != "1" {
					t.Fatalf("literal query %q matched a wildcard decoy: %+v, has_more=%v", q, options, more)
				}
			}
			options, more := requireFilterOptions(t, repo, kind, "mixed")
			if more || len(options) != 1 || options[0].ID != "3" {
				t.Fatalf("case-insensitive name search: %+v, has_more=%v", options, more)
			}
			for _, q := range []string{"' OR 1=1 --", `"; DROP TABLE users; --`} {
				options, more = requireFilterOptions(t, repo, kind, q)
				if len(options) != 0 || more {
					t.Fatalf("SQL-looking search must stay literal: query=%q, options=%+v, has_more=%v", q, options, more)
				}
			}
			if kind == "api_key" {
				insertFilterOption(t, db, kind, 4, "unrelated", `前缀_9%\!`)
				options, more = requireFilterOptions(t, repo, kind, `  前缀_9%\!  `)
				if more || len(options) != 1 || options[0].ID != "4" || options[0].Prefix != `前缀_9%\!` {
					t.Fatalf("literal Unicode key prefix search: %+v, has_more=%v", options, more)
				}
			}
		})
	}
}

func TestUsageFilterOptionsExactIDFirstAndDecimalPrecision(t *testing.T) {
	for _, kind := range []string{"server", "user", "api_key"} {
		t.Run(kind, func(t *testing.T) {
			db, repo := filterOptionsTestDB(t)
			insertFilterOption(t, db, kind, 37, "z exact ID", "exact")
			insertFilterOption(t, db, kind, 2, "a 37 name match", "name-match")
			if kind == "api_key" {
				insertFilterOption(t, db, kind, 3, "b prefix match", "37-prefix")
			}
			options, more := requireFilterOptions(t, repo, kind, "37")
			wantCount := 2
			if kind == "api_key" {
				wantCount = 3
			}
			if more || len(options) != wantCount || options[0].ID != "37" {
				t.Fatalf("exact ID must lead textual matches: %+v, has_more=%v", options, more)
			}
			if strconv.IntSize < 64 {
				return
			}
			largeID, err := strconv.ParseUint("9007199254740993", 10, strconv.IntSize)
			if err != nil {
				t.Fatal(err)
			}
			insertFilterOption(t, db, kind, uint(largeID), "large ID", "large")
			options, more = requireFilterOptions(t, repo, kind, "9007199254740993")
			if more || len(options) != 1 || options[0].ID != "9007199254740993" {
				t.Fatalf("large decimal ID lost precision: %+v, has_more=%v", options, more)
			}
			encoded, err := json.Marshal(options[0])
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire["id"]) != `"9007199254740993"` {
				t.Fatalf("ID must remain a decimal string on the wire: %s", encoded)
			}
		})
	}
}

func TestUsageFilterOptionsFiftyRowBound(t *testing.T) {
	for _, kind := range []string{"server", "user", "api_key"} {
		t.Run(kind, func(t *testing.T) {
			db, repo := filterOptionsTestDB(t)
			for id := uint(1); id <= 51; id++ {
				insertFilterOption(t, db, kind, id, fmt.Sprintf("option-%03d", id), "prefix")
			}
			options, more := requireFilterOptions(t, repo, kind, "")
			if len(options) != 50 || !more {
				t.Fatalf("51 entities must return 50 plus has_more: count=%d, has_more=%v", len(options), more)
			}
			seen := make(map[string]bool)
			for _, option := range options {
				if seen[option.ID] {
					t.Fatalf("duplicate option %s", option.ID)
				}
				seen[option.ID] = true
			}
			var table string
			switch kind {
			case "server":
				table = "servers"
			case "user":
				table = "users"
			case "api_key":
				table = "api_keys"
			}
			if err := db.Table(table).Where("id = ?", 51).Delete(nil).Error; err != nil {
				t.Fatal(err)
			}
			options, more = requireFilterOptions(t, repo, kind, "")
			if len(options) != 50 || more {
				t.Fatalf("exactly 50 entities must not claim another page: count=%d, has_more=%v", len(options), more)
			}
		})
	}
}

func TestUsageFilterOptionsValidationCountsUnicodeCharacters(t *testing.T) {
	_, repo := filterOptionsTestDB(t)
	for _, kind := range []string{"", "credential", "server; DROP TABLE users"} {
		if _, _, err := repo.FilterOptions(context.Background(), kind, ""); err == nil {
			t.Fatalf("unsupported kind %q accepted", kind)
		}
	}
	for _, kind := range []string{"server", "user", "api_key"} {
		for _, q := range []string{strings.Repeat("中", 129), "contains\x00null", string([]byte{0xff})} {
			if _, _, err := repo.FilterOptions(context.Background(), kind, q); err == nil {
				t.Fatalf("%s accepted invalid search %q", kind, q)
			}
		}
		options, more := requireFilterOptions(t, repo, kind, " \t"+strings.Repeat("中", 128)+"\n ")
		if len(options) != 0 || more {
			t.Fatalf("valid long Unicode search unexpectedly matched: %+v, has_more=%v", options, more)
		}
		options, more = requireFilterOptions(t, repo, kind, "18446744073709551616")
		if len(options) != 0 || more {
			t.Fatalf("out-of-range ID unexpectedly matched: %+v, has_more=%v", options, more)
		}
	}
}

func TestUsageFilterOptionsOversizedNumericSearchStillMatchesNames(t *testing.T) {
	db, repo := filterOptionsTestDB(t)
	for _, kind := range []string{"server", "user", "api_key"} {
		insertFilterOption(t, db, kind, 1, "9223372036854775808", "first")
		insertFilterOption(t, db, kind, 2, "18446744073709551616", "second")
		for index, q := range []string{"9223372036854775808", "18446744073709551616"} {
			options, more := requireFilterOptions(t, repo, kind, q)
			if len(options) != 1 || more || options[0].ID != strconv.Itoa(index+1) {
				t.Fatalf("%s numeric text search changed: q=%s options=%+v more=%v", kind, q, options, more)
			}
		}
	}
}

func TestUsageFilterOptionsPropagatesCanceledLookupContext(t *testing.T) {
	_, repo := filterOptionsTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := repo.FilterOptions(ctx, "server", ""); err == nil {
		t.Fatal("canceled lookup continued into the database")
	}
}

func TestUsageFilterOptionsSelectOnlySafeEntityFields(t *testing.T) {
	db, repo := filterOptionsTestDB(t)
	for _, kind := range []string{"server", "user", "api_key"} {
		insertFilterOption(t, db, kind, 1, "projection", "safe-prefix")
	}
	var selects []string
	captureSelect := func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "SELECT ") {
			selects = append(selects, sql)
		}
	}
	if err := db.Callback().Query().After("gorm:query").Register("test:filter_option_projection", captureSelect); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().After("gorm:row").Register("test:filter_option_projection", captureSelect); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"server", "user", "api_key"} {
		options, more := requireFilterOptions(t, repo, kind, "projection")
		if len(options) != 1 || more {
			t.Fatalf("%s: projection query failed: %+v, has_more=%v", kind, options, more)
		}
	}
	if len(selects) < 3 {
		t.Fatalf("did not observe all entity projections: %v", selects)
	}
	for _, sql := range selects {
		upper := strings.ToUpper(sql)
		from := strings.Index(upper, " FROM ")
		if from == -1 {
			t.Fatalf("unexpected entity query: %s", sql)
		}
		projection := strings.ToLower(sql[:from])
		for _, forbidden := range []string{"*", "host", "password", "hash", "salt", "encrypted", "scopes", "server_ids", "owner_id", "user_id", "created_at", "updated_at", "usage_logs"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("unsafe or unnecessarily broad projection (%s): %s", forbidden, sql)
			}
		}
		if strings.Contains(strings.ToLower(sql), "usage_logs") {
			t.Fatalf("options must query entities rather than scan usage logs: %s", sql)
		}
	}
	// Removing unrelated columns makes any accidental dependency on credentials
	// or other entity fields fail even when the wire DTO still appears safe.
	for _, statement := range []string{
		"ALTER TABLE users DROP COLUMN password_hash, DROP COLUMN api_key_hash, DROP COLUMN role, DROP COLUMN created_at, DROP COLUMN updated_at",
		"ALTER TABLE servers DROP COLUMN host, DROP COLUMN host_key, DROP COLUMN host_key_seen, DROP COLUMN owner_id, DROP COLUMN credential_id, DROP COLUMN created_at, DROP COLUMN updated_at",
		"ALTER TABLE api_keys DROP COLUMN key_hash, DROP COLUMN encrypted_raw_key, DROP COLUMN salt, DROP COLUMN user_id, DROP COLUMN scopes, DROP COLUMN server_ids, DROP COLUMN created_at",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"server", "user", "api_key"} {
		options, more := requireFilterOptions(t, repo, kind, "")
		if len(options) != 1 || more || options[0].ID != "1" || options[0].Name != "projection" {
			t.Fatalf("%s depended on unrelated columns: %+v, has_more=%v", kind, options, more)
		}
	}
}
