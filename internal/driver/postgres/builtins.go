package postgres

import "slices"

// builtinFunctions is a curated list of commonly used pg_catalog functions.
// Objects leaves pg_catalog out (thousands of entries, mostly internal), so
// autocomplete offers these instead.
var builtinFunctions = []string{
	// Aggregates.
	"array_agg", "avg", "bit_and", "bit_or", "bool_and", "bool_or", "count", "every",
	"json_agg", "json_object_agg", "jsonb_agg", "jsonb_object_agg", "max", "min",
	"mode", "percentile_cont", "percentile_disc", "stddev", "stddev_pop", "stddev_samp",
	"string_agg", "sum", "var_pop", "var_samp", "variance",
	// Window functions.
	"cume_dist", "dense_rank", "first_value", "lag", "last_value", "lead", "nth_value",
	"ntile", "percent_rank", "rank", "row_number",
	// Conditionals.
	"coalesce", "greatest", "least", "nullif",
	// Strings.
	"ascii", "btrim", "char_length", "character_length", "chr", "concat", "concat_ws",
	"convert_from", "convert_to", "decode", "encode", "format", "initcap", "left",
	"length", "lower", "lpad", "ltrim", "md5", "octet_length", "overlay", "position",
	"quote_ident", "quote_literal", "quote_nullable", "regexp_count", "regexp_like",
	"regexp_match", "regexp_matches", "regexp_replace", "regexp_split_to_array",
	"regexp_split_to_table", "regexp_substr", "repeat", "replace", "reverse", "right",
	"rpad", "rtrim", "split_part", "starts_with", "string_to_array", "string_to_table",
	"strpos", "substr", "substring", "to_hex", "translate", "trim", "unistr", "upper",
	// Math.
	"abs", "cbrt", "ceil", "ceiling", "div", "exp", "floor", "gcd", "lcm", "ln", "log",
	"mod", "pi", "power", "random", "round", "scale", "sign", "sqrt", "trunc", "width_bucket",
	// Formatting.
	"to_char", "to_date", "to_number", "to_timestamp",
	// Date and time.
	"age", "clock_timestamp", "current_date", "current_time", "current_timestamp",
	"date_add", "date_bin", "date_part", "date_subtract", "date_trunc", "extract",
	"isfinite", "justify_days", "justify_hours", "justify_interval", "localtime",
	"localtimestamp", "make_date", "make_interval", "make_time", "make_timestamp",
	"make_timestamptz", "now", "statement_timestamp", "timeofday", "transaction_timestamp",
	// Set-returning.
	"generate_series", "generate_subscripts", "unnest",
	// Arrays.
	"array_append", "array_cat", "array_dims", "array_fill", "array_length", "array_lower",
	"array_position", "array_positions", "array_prepend", "array_remove", "array_replace",
	"array_to_string", "array_upper", "cardinality",
	// JSON.
	"json_array_elements", "json_array_elements_text", "json_array_length",
	"json_build_array", "json_build_object", "json_each", "json_each_text",
	"json_extract_path", "json_extract_path_text", "json_object_keys", "json_typeof",
	"jsonb_array_elements", "jsonb_array_elements_text", "jsonb_array_length",
	"jsonb_build_array", "jsonb_build_object", "jsonb_each", "jsonb_each_text",
	"jsonb_extract_path", "jsonb_extract_path_text", "jsonb_insert", "jsonb_object_keys",
	"jsonb_path_exists", "jsonb_path_query", "jsonb_path_query_array",
	"jsonb_path_query_first", "jsonb_pretty", "jsonb_set", "jsonb_strip_nulls",
	"jsonb_typeof", "row_to_json", "to_json", "to_jsonb",
	// Sequences.
	"currval", "lastval", "nextval", "setval",
	// UUIDs and hashing.
	"gen_random_uuid", "sha256", "sha512",
	// Session and system information.
	"current_database", "current_schema", "current_schemas", "current_setting",
	"current_user", "has_table_privilege", "pg_backend_pid", "pg_cancel_backend",
	"pg_column_size", "pg_database_size", "pg_get_functiondef", "pg_get_viewdef",
	"pg_indexes_size", "pg_relation_size", "pg_size_pretty", "pg_sleep",
	"pg_table_size", "pg_terminate_backend", "pg_total_relation_size", "pg_typeof",
	"session_user", "set_config", "version",
}

// BuiltinFunctions returns the names of commonly used PostgreSQL built-in
// functions, sorted, for autocomplete. The caller owns the returned slice.
func BuiltinFunctions() []string {
	out := slices.Clone(builtinFunctions)
	slices.Sort(out)
	return out
}
