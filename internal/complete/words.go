package complete

import "strings"

// reserved words: never taken as aliases and always quoted as identifiers.
var reserved = setOf(`ALL ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC
	BETWEEN BOTH BY CASE CAST CHECK COLLATE COLUMN CONSTRAINT CREATE CROSS
	CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER DEFAULT
	DEFERRABLE DELETE DESC DESCRIBE DISTINCT DO DROP ELSE END EXCEPT EXISTS
	EXPLAIN FALSE FETCH FOR FOREIGN FROM FULL GRANT GROUP HAVING ILIKE IN
	INNER INSERT INTERSECT INTO IS JOIN LATERAL LEADING LEFT LIKE LIMIT
	NATURAL NOT NULL OFFSET ON ONLY OR ORDER OUTER OVER PRIMARY REFERENCES
	RETURNING RIGHT SELECT SET SOME TABLE THEN TO TRAILING TRUE TRUNCATE
	UNION UNIQUE UPDATE USER USING VALUES WHEN WHERE WINDOW WITH ALTER`)

var stmtKeywords = []string{
	"SELECT", "INSERT INTO", "UPDATE", "DELETE FROM", "WITH", "CREATE",
	"CREATE TABLE", "CREATE INDEX", "CREATE VIEW", "ALTER", "ALTER TABLE",
	"DROP", "DROP TABLE", "TRUNCATE", "EXPLAIN", "EXPLAIN ANALYZE", "BEGIN",
	"COMMIT", "ROLLBACK", "SAVEPOINT", "GRANT", "REVOKE", "SET", "SHOW",
	"VALUES", "CALL", "COPY", "ANALYZE", "VACUUM", "MERGE INTO", "COMMENT ON",
}

var keywords = []string{
	"SELECT", "FROM", "WHERE", "AND", "OR", "NOT", "IN", "IS", "NULL",
	"IS NULL", "IS NOT NULL", "LIKE", "ILIKE", "BETWEEN", "EXISTS", "AS",
	"ON", "JOIN", "LEFT JOIN", "RIGHT JOIN", "INNER JOIN", "FULL JOIN",
	"CROSS JOIN", "GROUP BY", "ORDER BY", "HAVING", "LIMIT", "OFFSET",
	"DISTINCT", "UNION", "UNION ALL", "INTERSECT", "EXCEPT", "CASE", "WHEN",
	"THEN", "ELSE", "END", "ASC", "DESC", "NULLS FIRST", "NULLS LAST", "SET",
	"VALUES", "RETURNING", "USING", "TRUE", "FALSE", "DEFAULT", "INTO", "ALL",
	"ANY", "CAST", "WITH", "OVER", "PARTITION BY", "FETCH FIRST", "FOR UPDATE",
	"LATERAL",
}

// afterTableKeywords follow a table reference (FROM t |).
var afterTableKeywords = []string{
	"WHERE", "JOIN", "LEFT JOIN", "RIGHT JOIN", "INNER JOIN", "FULL JOIN",
	"CROSS JOIN", "ON", "USING", "AS", "GROUP BY", "ORDER BY", "HAVING",
	"LIMIT", "OFFSET", "SET", "VALUES", "UNION", "UNION ALL", "RETURNING",
	"WINDOW", "FOR UPDATE",
}

type sig struct{ name, detail string }

var sqlFunctions = []sig{
	{"count", "count(expr)"}, {"sum", "sum(expr)"}, {"avg", "avg(expr)"},
	{"min", "min(expr)"}, {"max", "max(expr)"},
	{"coalesce", "coalesce(a, b, …)"}, {"nullif", "nullif(a, b)"},
	{"greatest", "greatest(a, b, …)"}, {"least", "least(a, b, …)"},
	{"now", "now()"}, {"current_date", "date"}, {"current_timestamp", "timestamptz"},
	{"date_trunc", "date_trunc(field, ts)"}, {"extract", "extract(field from ts)"},
	{"to_char", "to_char(value, fmt)"}, {"to_date", "to_date(text, fmt)"},
	{"to_timestamp", "to_timestamp(text, fmt)"}, {"age", "age(ts, ts)"},
	{"lower", "lower(text)"}, {"upper", "upper(text)"}, {"length", "length(text)"},
	{"trim", "trim(text)"}, {"substring", "substring(text from n for m)"},
	{"replace", "replace(text, from, to)"}, {"concat", "concat(a, b, …)"},
	{"concat_ws", "concat_ws(sep, a, …)"}, {"split_part", "split_part(text, sep, n)"},
	{"position", "position(sub in text)"}, {"left", "left(text, n)"},
	{"right", "right(text, n)"}, {"lpad", "lpad(text, n, fill)"},
	{"rpad", "rpad(text, n, fill)"}, {"round", "round(x, digits)"},
	{"floor", "floor(x)"}, {"ceil", "ceil(x)"}, {"abs", "abs(x)"},
	{"mod", "mod(a, b)"}, {"power", "power(a, b)"}, {"sqrt", "sqrt(x)"},
	{"random", "random()"}, {"cast", "cast(x as type)"},
	{"string_agg", "string_agg(expr, sep)"}, {"array_agg", "array_agg(expr)"},
	{"json_agg", "json_agg(expr)"}, {"jsonb_agg", "jsonb_agg(expr)"},
	{"json_build_object", "json_build_object(k, v, …)"},
	{"jsonb_build_object", "jsonb_build_object(k, v, …)"},
	{"row_number", "row_number() over (…)"}, {"rank", "rank() over (…)"},
	{"dense_rank", "dense_rank() over (…)"}, {"lag", "lag(expr, n)"},
	{"lead", "lead(expr, n)"}, {"first_value", "first_value(expr)"},
	{"last_value", "last_value(expr)"}, {"ntile", "ntile(n)"},
	{"generate_series", "generate_series(start, stop)"}, {"unnest", "unnest(array)"},
	{"regexp_replace", "regexp_replace(text, re, repl)"}, {"md5", "md5(text)"},
	{"gen_random_uuid", "gen_random_uuid()"},
}

var mongoMethods = []sig{
	{"find", "find(filter, projection)"}, {"findOne", "findOne(filter, projection)"},
	{"aggregate", "aggregate(pipeline)"}, {"countDocuments", "countDocuments(filter)"},
	{"estimatedDocumentCount", "estimatedDocumentCount()"},
	{"insertOne", "insertOne(doc)"}, {"insertMany", "insertMany(docs)"},
	{"updateOne", "updateOne(filter, update)"}, {"updateMany", "updateMany(filter, update)"},
	{"replaceOne", "replaceOne(filter, doc)"}, {"deleteOne", "deleteOne(filter)"},
	{"deleteMany", "deleteMany(filter)"}, {"distinct", "distinct(field, filter)"},
	{"createIndex", "createIndex(keys, options)"}, {"dropIndex", "dropIndex(name)"},
	{"getIndexes", "getIndexes()"}, {"findOneAndUpdate", "findOneAndUpdate(filter, update)"},
	{"findOneAndReplace", "findOneAndReplace(filter, doc)"},
	{"findOneAndDelete", "findOneAndDelete(filter)"}, {"bulkWrite", "bulkWrite(ops)"},
	{"drop", "drop()"}, {"stats", "stats()"}, {"renameCollection", "renameCollection(name)"},
	{"explain", "explain()"}, {"watch", "watch(pipeline)"},
}

var mongoCursorMethods = []sig{
	{"sort", "sort(spec)"}, {"limit", "limit(n)"}, {"skip", "skip(n)"},
	{"projection", "projection(spec)"}, {"count", "count()"}, {"toArray", "toArray()"},
	{"pretty", "pretty()"}, {"explain", "explain()"}, {"hint", "hint(index)"},
	{"batchSize", "batchSize(n)"}, {"forEach", "forEach(fn)"}, {"map", "map(fn)"},
	{"itcount", "itcount()"}, {"next", "next()"}, {"hasNext", "hasNext()"},
}

var mongoDBMethods = []sig{
	{"getCollection", "getCollection(name)"}, {"getCollectionNames", "getCollectionNames()"},
	{"getName", "getName()"}, {"stats", "stats()"}, {"runCommand", "runCommand(cmd)"},
	{"createCollection", "createCollection(name)"}, {"dropDatabase", "dropDatabase()"},
	{"adminCommand", "adminCommand(cmd)"}, {"getSiblingDB", "getSiblingDB(name)"},
}

var mongoStatementWords = []sig{
	{"db", "current database"}, {"show dbs", "list databases"},
	{"show collections", "list collections"}, {"use", "use <db>"},
}

var mongoOperators = []sig{
	{"$eq", "query"}, {"$ne", "query"}, {"$gt", "query"}, {"$gte", "query"},
	{"$lt", "query"}, {"$lte", "query"}, {"$in", "query"}, {"$nin", "query"},
	{"$and", "query"}, {"$or", "query"}, {"$not", "query"}, {"$nor", "query"},
	{"$exists", "query"}, {"$type", "query"}, {"$regex", "query"},
	{"$expr", "query"}, {"$elemMatch", "query"}, {"$size", "query"},
	{"$all", "query"}, {"$mod", "query"}, {"$text", "query"},
	{"$set", "update · stage"}, {"$unset", "update · stage"}, {"$inc", "update"},
	{"$push", "update · accumulator"}, {"$pull", "update"},
	{"$addToSet", "update · accumulator"}, {"$pop", "update"},
	{"$rename", "update"}, {"$mul", "update"}, {"$min", "update · accumulator"},
	{"$max", "update · accumulator"}, {"$currentDate", "update"},
	{"$setOnInsert", "update"}, {"$match", "stage"}, {"$group", "stage"},
	{"$project", "stage"}, {"$sort", "stage"}, {"$limit", "stage"},
	{"$skip", "stage"}, {"$unwind", "stage"}, {"$lookup", "stage"},
	{"$addFields", "stage"}, {"$count", "stage"}, {"$facet", "stage"},
	{"$replaceRoot", "stage"}, {"$sample", "stage"}, {"$out", "stage"},
	{"$merge", "stage"}, {"$sum", "accumulator"}, {"$avg", "accumulator"},
	{"$first", "accumulator"}, {"$last", "accumulator"},
}

var redisCommands = []sig{
	{"GET", "key"}, {"SET", "key value [EX s] [NX|XX]"}, {"DEL", "key [key …]"},
	{"EXISTS", "key [key …]"}, {"EXPIRE", "key seconds"}, {"TTL", "key"},
	{"PTTL", "key"}, {"PERSIST", "key"}, {"KEYS", "pattern"},
	{"SCAN", "cursor [MATCH p] [COUNT n]"}, {"TYPE", "key"}, {"RENAME", "key newkey"},
	{"INCR", "key"}, {"INCRBY", "key n"}, {"DECR", "key"}, {"DECRBY", "key n"},
	{"APPEND", "key value"}, {"STRLEN", "key"}, {"MGET", "key [key …]"},
	{"MSET", "key value [key value …]"}, {"SETNX", "key value"},
	{"SETEX", "key seconds value"}, {"GETDEL", "key"}, {"GETEX", "key [EX s]"},
	{"HGET", "key field"}, {"HSET", "key field value [field value …]"},
	{"HDEL", "key field [field …]"}, {"HGETALL", "key"}, {"HKEYS", "key"},
	{"HVALS", "key"}, {"HLEN", "key"}, {"HEXISTS", "key field"},
	{"HINCRBY", "key field n"}, {"HMGET", "key field [field …]"},
	{"HSCAN", "key cursor [MATCH p]"}, {"LPUSH", "key element [element …]"},
	{"RPUSH", "key element [element …]"}, {"LPOP", "key [count]"},
	{"RPOP", "key [count]"}, {"LRANGE", "key start stop"}, {"LLEN", "key"},
	{"LINDEX", "key index"}, {"LSET", "key index element"},
	{"LREM", "key count element"}, {"LTRIM", "key start stop"},
	{"BLPOP", "key [key …] timeout"}, {"BRPOP", "key [key …] timeout"},
	{"SADD", "key member [member …]"}, {"SREM", "key member [member …]"},
	{"SMEMBERS", "key"}, {"SISMEMBER", "key member"}, {"SCARD", "key"},
	{"SINTER", "key [key …]"}, {"SUNION", "key [key …]"}, {"SDIFF", "key [key …]"},
	{"SSCAN", "key cursor [MATCH p]"}, {"ZADD", "key score member [score member …]"},
	{"ZREM", "key member [member …]"}, {"ZRANGE", "key start stop [WITHSCORES]"},
	{"ZREVRANGE", "key start stop [WITHSCORES]"}, {"ZRANGEBYSCORE", "key min max"},
	{"ZSCORE", "key member"}, {"ZCARD", "key"}, {"ZINCRBY", "key n member"},
	{"ZRANK", "key member"}, {"ZCOUNT", "key min max"}, {"ZSCAN", "key cursor [MATCH p]"},
	{"XADD", "key id field value [field value …]"}, {"XRANGE", "key start end"},
	{"XREAD", "[COUNT n] STREAMS key id"}, {"XLEN", "key"},
	{"PUBLISH", "channel message"}, {"SUBSCRIBE", "channel [channel …]"},
	{"PING", "[message]"}, {"ECHO", "message"}, {"INFO", "[section]"},
	{"DBSIZE", ""}, {"SELECT", "index"}, {"FLUSHDB", "[ASYNC]"}, {"FLUSHALL", "[ASYNC]"},
	{"CONFIG GET", "parameter"}, {"CONFIG SET", "parameter value"},
	{"CLIENT LIST", ""}, {"SLOWLOG GET", "[count]"}, {"MEMORY USAGE", "key"},
	{"OBJECT ENCODING", "key"}, {"UNLINK", "key [key …]"}, {"COPY", "source destination"},
	{"DUMP", "key"}, {"MULTI", ""}, {"EXEC", ""}, {"DISCARD", ""},
	{"WATCH", "key [key …]"}, {"UNWATCH", ""},
	{"EVAL", "script numkeys [key …] [arg …]"}, {"EVALSHA", "sha1 numkeys [key …] [arg …]"},
	{"SCRIPT LOAD", "script"}, {"TIME", ""}, {"LASTSAVE", ""}, {"BGSAVE", ""},
	{"PFADD", "key element [element …]"}, {"PFCOUNT", "key [key …]"},
	{"GEOADD", "key lon lat member"}, {"GEODIST", "key m1 m2 [unit]"},
}

func setOf(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}
