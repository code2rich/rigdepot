// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Column is one column as both databases can describe it.
type Column struct {
	Name       string
	Type       string // normalized: text, integer, bigint, boolean, binary, or the raw type
	NotNull    bool
	PrimaryKey bool
}

// Schema is the tables of a database and their columns in order. It is what
// the test that keeps the two databases from drifting apart compares.
type Schema map[string][]Column

// Inspect reads the tables and columns of the database (the current schema
// on PostgreSQL).
func (db *DB) Inspect(ctx context.Context) (Schema, error) {
	if db.dialect == SQLite {
		return db.inspectSQLite(ctx)
	}
	return db.inspectPostgres(ctx)
}

func (db *DB) inspectSQLite(ctx context.Context) (Schema, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s := Schema{}
	for _, t := range tables {
		cols, err := db.QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, t)
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var c Column
			var notNull, pk int
			if err := cols.Scan(&c.Name, &c.Type, &notNull, &pk); err != nil {
				cols.Close()
				return nil, err
			}
			c.Type, c.NotNull, c.PrimaryKey = normalizeType(c.Type), notNull != 0, pk > 0
			s[t] = append(s[t], c)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (db *DB) inspectPostgres(ctx context.Context) (Schema, error) {
	rows, err := db.QueryContext(ctx, `
SELECT c.table_name, c.column_name, c.data_type, c.is_nullable = 'NO',
       EXISTS (
         SELECT 1 FROM information_schema.table_constraints tc
         JOIN information_schema.key_column_usage k
           ON k.constraint_name = tc.constraint_name AND k.table_schema = tc.table_schema AND k.table_name = tc.table_name
         WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = c.table_schema
           AND tc.table_name = c.table_name AND k.column_name = c.column_name)
FROM information_schema.columns c
JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE'
ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := Schema{}
	for rows.Next() {
		var t string
		var c Column
		if err := rows.Scan(&t, &c.Name, &c.Type, &c.NotNull, &c.PrimaryKey); err != nil {
			return nil, err
		}
		c.Type = normalizeType(c.Type)
		s[t] = append(s[t], c)
	}
	return s, rows.Err()
}

func normalizeType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "text", "character varying", "varchar":
		return "text"
	case "integer", "int", "int4":
		return "integer"
	case "bigint", "int8":
		return "bigint"
	case "boolean", "bool":
		return "boolean"
	case "blob", "bytea":
		return "binary"
	}
	return strings.ToLower(t)
}

// String writes the schema as stable text, one table per paragraph, for
// golden files and readable diffs.
func (s Schema) String() string {
	names := make([]string, 0, len(s))
	for t := range s {
		names = append(names, t)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, t := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(t + "\n")
		for _, c := range s[t] {
			b.WriteString("  " + c.String() + "\n")
		}
	}
	return b.String()
}

func (c Column) String() string {
	s := fmt.Sprintf("%s %s", c.Name, c.Type)
	if c.NotNull {
		s += " not null"
	}
	if c.PrimaryKey {
		s += " pk"
	}
	return s
}

// Diff lists how b differs from a: missing and extra tables, and columns
// that are missing, extra, out of order or described differently. Empty
// means the same.
func Diff(a, b Schema) []string {
	var out []string
	tables := map[string]bool{}
	for t := range a {
		tables[t] = true
	}
	for t := range b {
		tables[t] = true
	}
	names := make([]string, 0, len(tables))
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		ca, inA := a[t]
		cb, inB := b[t]
		switch {
		case !inB:
			out = append(out, "table "+t+": only in the first")
			continue
		case !inA:
			out = append(out, "table "+t+": only in the second")
			continue
		}
		for i := 0; i < max(len(ca), len(cb)); i++ {
			switch {
			case i >= len(cb):
				out = append(out, fmt.Sprintf("%s: column %q only in the first", t, ca[i]))
			case i >= len(ca):
				out = append(out, fmt.Sprintf("%s: column %q only in the second", t, cb[i]))
			case ca[i] != cb[i]:
				out = append(out, fmt.Sprintf("%s: column %d is %q in the first and %q in the second", t, i+1, ca[i], cb[i]))
			}
		}
	}
	return out
}
