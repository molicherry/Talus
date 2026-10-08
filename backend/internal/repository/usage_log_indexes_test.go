package repository

import (
	"strings"
	"testing"
	"time"
)

func TestUsageIdentityFiltersUseOrderedIndexes(t *testing.T) {
	db, _ := usageTestDB(t)
	// Sparse identities across a 90-day window make a time-window scan costly.
	// Leave planner settings at their defaults to validate the actual access path.
	if err := db.Exec(`INSERT INTO usage_logs
		(operation_id,started_at,recorded_at,outcome,phase,state_seq,action,auth_type,source,user_id,server_id,metadata)
		SELECT md5(g::text)::uuid, clock_timestamp() - g * interval '3 minutes', clock_timestamp(),
		'succeeded','closed',2,'server.exec','jwt','operation',g % 200 + 1,g % 400 + 1,'{}'::jsonb
		FROM generate_series(1,40000) AS g`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE usage_logs").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, column := range []string{"user_id", "server_id"} {
		for _, continuation := range []bool{false, true} {
			query := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM usage_logs WHERE " + column + " = ? AND started_at >= ? AND started_at <= ? AND id <= ?"
			args := []interface{}{1, now.Add(-90 * 24 * time.Hour), now, 40000}
			if continuation {
				query += " AND (started_at,id) < (?,?)"
				args = append(args, now.Add(-24*time.Hour), 39999)
			}
			query += " ORDER BY started_at DESC,id DESC LIMIT 26"
			var plan string
			if err := db.Raw(query, args...).Row().Scan(&plan); err != nil {
				t.Fatal(err)
			}
			index := "idx_usage_" + strings.TrimSuffix(column, "_id")
			if !strings.Contains(plan, `"Index Name": "`+index+`"`) || strings.Contains(plan, `"Node Type": "Seq Scan"`) {
				t.Fatalf("%s continuation=%t did not use its ordered index: %s", column, continuation, plan)
			}
			t.Logf("%s continuation=%t uses %s at 90-day scope", column, continuation, index)
		}
	}
}
