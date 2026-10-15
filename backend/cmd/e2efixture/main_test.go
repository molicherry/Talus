package main

import "testing"

// TestReplaceDatabasePathKeepsConnectionOptions pins F15: rewriting the database name
// must not drop the connection policy the caller asked for.
func TestReplaceDatabasePathKeepsConnectionOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "query parameters survive",
			in:   "postgres://user:pass@db.example:5432/production?sslmode=require&connect_timeout=2&application_name=talus",
			want: "postgres://user:pass@db.example:5432/talus_e2e_1?sslmode=require&connect_timeout=2&application_name=talus",
		},
		{
			name: "no query",
			in:   "postgres://user:pass@127.0.0.1:55432/postgres",
			want: "postgres://user:pass@127.0.0.1:55432/talus_e2e_1",
		},
		{
			// pgx/PostgreSQL accept the database as a query parameter, which overrides
			// the path: rewriting only the path would keep talking to the original
			// database (R06).
			name: "dbname alias is rewritten",
			in:   "postgres://user:pass@db.example:5432/production?dbname=production&sslmode=require",
			want: "postgres://user:pass@db.example:5432/talus_e2e_1?dbname=talus_e2e_1&sslmode=require",
		},
		{
			name: "database alias is rewritten",
			in:   "postgres://user:pass@db.example:5432/production?database=production&connect_timeout=2",
			want: "postgres://user:pass@db.example:5432/talus_e2e_1?connect_timeout=2&database=talus_e2e_1",
		},
		{
			name: "encoded credentials survive",
			in:   "postgres://us%40er:p%40ss@127.0.0.1:55432/postgres?sslmode=verify-full",
			want: "postgres://us%40er:p%40ss@127.0.0.1:55432/talus_e2e_1?sslmode=verify-full",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := replaceDatabasePath(tc.in, "talus_e2e_1")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := replaceDatabasePath("not-a-url", "talus_e2e_1"); err == nil {
		t.Fatal("a URL without scheme or host must be refused")
	}
}
