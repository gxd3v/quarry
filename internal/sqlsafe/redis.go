package sqlsafe

import (
	"fmt"
	"strings"
)

func analyzeRedis(s string) verdict {
	v := verdict{known: true}
	for line := range strings.Lines(s) {
		f := redisFields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "//") {
			continue
		}
		v = v.worse(redisCommand(f))
	}
	return v
}

// redisFields splits a redis-cli line into arguments, honouring "double"
// (with backslash escapes) and 'single' quotes.
func redisFields(line string) []string {
	var (
		out []string
		cur strings.Builder
		in  bool
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case isSpace(c):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case c == '"' || c == '\'':
			in = true
			j := i + 1
			for j < len(line) && line[j] != c {
				if c == '"' && line[j] == '\\' && j+1 < len(line) {
					j++
				}
				cur.WriteByte(line[j])
				j++
			}
			i = j
		default:
			in = true
			cur.WriteByte(c)
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// redisContainers are commands whose first argument is a subcommand.
var redisContainers = set("CONFIG", "CLIENT", "SCRIPT", "FUNCTION", "CLUSTER", "ACL",
	"MEMORY", "OBJECT", "XGROUP", "XINFO", "COMMAND", "SLOWLOG", "LATENCY", "PUBSUB",
	"MODULE")

var redisDestructive = map[string]string{
	"FLUSHALL":           "FLUSHALL deletes every key in every database",
	"FLUSHDB":            "FLUSHDB deletes every key in the current database",
	"SHUTDOWN":           "SHUTDOWN stops the server",
	"CONFIG SET":         "CONFIG SET changes the live server configuration",
	"SCRIPT FLUSH":       "SCRIPT FLUSH removes every cached Lua script",
	"FUNCTION FLUSH":     "FUNCTION FLUSH deletes every function library",
	"CLUSTER RESET":      "CLUSTER RESET resets the node's cluster state",
	"CLUSTER FLUSHSLOTS": "CLUSTER FLUSHSLOTS removes every slot from the node",
	"DEBUG":              "DEBUG can crash or corrupt the server",
	"REPLICAOF":          "REPLICAOF can replace the whole dataset with a primary's",
	"SLAVEOF":            "SLAVEOF can replace the whole dataset with a primary's",
	"SWAPDB":             "SWAPDB swaps the contents of two databases",
	"MODULE UNLOAD":      "MODULE UNLOAD removes a module from the running server",
}

// redisKeyWrites take the key they modify as first argument.
var redisKeyWrites = set("SET", "SETNX", "SETEX", "PSETEX", "MSET", "MSETNX", "GETSET",
	"GETDEL", "GETEX", "APPEND", "SETRANGE", "INCR", "INCRBY", "INCRBYFLOAT", "DECR",
	"DECRBY", "DEL", "UNLINK", "EXPIRE", "PEXPIRE", "EXPIREAT", "PEXPIREAT", "PERSIST",
	"RENAME", "RENAMENX", "MOVE", "COPY", "RESTORE", "HSET", "HSETNX", "HMSET", "HDEL",
	"HINCRBY", "HINCRBYFLOAT", "HEXPIRE", "HPEXPIRE", "HEXPIREAT", "HPEXPIREAT", "HPERSIST",
	"HGETDEL", "HGETEX", "HSETEX", "LPUSH", "RPUSH", "LPUSHX", "RPUSHX", "LPOP", "RPOP",
	"LSET", "LREM", "LTRIM", "LINSERT", "LMOVE", "RPOPLPUSH", "BLPOP", "BRPOP", "BLMOVE",
	"BRPOPLPUSH", "SADD", "SREM", "SPOP", "SMOVE", "SINTERSTORE", "SUNIONSTORE",
	"SDIFFSTORE", "ZADD", "ZREM", "ZINCRBY", "ZPOPMIN", "ZPOPMAX", "BZPOPMIN", "BZPOPMAX",
	"ZREMRANGEBYSCORE", "ZREMRANGEBYRANK", "ZREMRANGEBYLEX", "ZRANGESTORE", "ZUNIONSTORE",
	"ZINTERSTORE", "ZDIFFSTORE", "XADD", "XDEL", "XTRIM", "XACK", "XCLAIM", "XAUTOCLAIM",
	"XSETID", "SETBIT", "BITFIELD", "PFADD", "PFMERGE", "GEOADD", "GEOSEARCHSTORE",
	"JSON.SET", "JSON.DEL", "JSON.FORGET", "JSON.MERGE", "JSON.MSET", "JSON.NUMINCRBY",
	"JSON.ARRAPPEND", "JSON.ARRINSERT", "JSON.ARRPOP", "JSON.ARRTRIM", "JSON.STRAPPEND",
	"JSON.TOGGLE", "JSON.CLEAR")

// redisOtherWrites modify data or server state without a single key.
var redisOtherWrites = set("BITOP", "LMPOP", "BLMPOP", "ZMPOP", "BZMPOP", "XREADGROUP",
	"MIGRATE", "EVAL", "EVALSHA", "FCALL", "SAVE", "BGSAVE", "BGREWRITEAOF", "FAILOVER")

var redisReads = set("GET", "MGET", "GETRANGE", "SUBSTR", "STRLEN", "EXISTS", "TYPE", "TTL",
	"PTTL", "EXPIRETIME", "PEXPIRETIME", "KEYS", "SCAN", "RANDOMKEY", "DUMP", "TOUCH",
	"HGET", "HMGET", "HGETALL", "HKEYS", "HVALS", "HLEN", "HEXISTS", "HSTRLEN", "HSCAN",
	"HRANDFIELD", "HTTL", "HPTTL", "HEXPIRETIME", "HPEXPIRETIME", "LRANGE", "LINDEX", "LLEN",
	"LPOS", "SMEMBERS", "SISMEMBER", "SMISMEMBER", "SCARD", "SRANDMEMBER", "SSCAN", "SINTER",
	"SUNION", "SDIFF", "SINTERCARD", "ZRANGE", "ZRANGEBYSCORE", "ZRANGEBYLEX", "ZREVRANGE",
	"ZREVRANGEBYSCORE", "ZREVRANGEBYLEX", "ZRANK", "ZREVRANK", "ZSCORE", "ZMSCORE", "ZCARD",
	"ZCOUNT", "ZLEXCOUNT", "ZSCAN", "ZRANDMEMBER", "ZINTER", "ZUNION", "ZDIFF", "ZINTERCARD",
	"XRANGE", "XREVRANGE", "XLEN", "XREAD", "XPENDING", "GETBIT", "BITCOUNT", "BITPOS",
	"BITFIELD_RO", "PFCOUNT", "GEOPOS", "GEODIST", "GEOHASH", "GEORADIUS_RO",
	"GEORADIUSBYMEMBER_RO", "GEOSEARCH", "LCS", "SORT_RO", "INFO", "PING", "ECHO", "DBSIZE",
	"SELECT", "TIME", "LASTSAVE", "ROLE", "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH",
	"AUTH", "HELLO", "QUIT", "RESET", "READONLY", "READWRITE", "EVAL_RO", "EVALSHA_RO",
	"FCALL_RO", "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "UNSUBSCRIBE", "PUNSUBSCRIBE",
	"SUNSUBSCRIBE", "PUBLISH", "SPUBLISH", "MONITOR", "WAIT", "WAITAOF", "LOLWUT",
	"JSON.GET", "JSON.MGET", "JSON.TYPE", "JSON.STRLEN", "JSON.ARRLEN", "JSON.ARRINDEX",
	"JSON.OBJKEYS", "JSON.OBJLEN", "JSON.RESP", "JSON.DEBUG", "XINFO", "COMMAND", "PUBSUB",
	"CONFIG GET", "CLIENT LIST", "CLIENT INFO", "CLIENT GETNAME", "CLIENT ID",
	"CLIENT SETNAME", "CLIENT SETINFO", "CLIENT TRACKING", "CLIENT TRACKINGINFO",
	"CLIENT GETREDIR", "CLIENT NO-EVICT", "CLIENT NO-TOUCH", "CLIENT REPLY", "CLIENT CACHING",
	"MEMORY USAGE", "MEMORY STATS", "MEMORY DOCTOR", "MEMORY MALLOC-STATS",
	"OBJECT ENCODING", "OBJECT FREQ", "OBJECT IDLETIME", "OBJECT REFCOUNT", "OBJECT HELP",
	"SLOWLOG GET", "SLOWLOG LEN", "SLOWLOG HELP", "LATENCY LATEST", "LATENCY HISTORY",
	"LATENCY GRAPH", "LATENCY DOCTOR", "LATENCY HISTOGRAM", "LATENCY HELP", "ACL WHOAMI",
	"ACL LIST", "ACL USERS", "ACL CAT", "ACL GETUSER", "ACL LOG", "ACL DRYRUN", "ACL HELP",
	"SCRIPT EXISTS", "FUNCTION LIST", "FUNCTION DUMP", "FUNCTION STATS", "CLUSTER INFO",
	"CLUSTER NODES", "CLUSTER SLOTS", "CLUSTER SHARDS", "CLUSTER MYID", "CLUSTER MYSHARDID",
	"CLUSTER KEYSLOT", "CLUSTER COUNTKEYSINSLOT", "CLUSTER GETKEYSINSLOT", "CLUSTER LINKS",
	"MODULE LIST")

func redisCommand(f []string) verdict {
	cmd := strings.ToUpper(f[0])
	verb := cmd
	if redisContainers[cmd] && len(f) > 1 {
		verb += " " + strings.ToUpper(f[1])
	}
	if r, ok := redisDestructive[verb]; ok {
		return verdict{Report{Destructive, verb, r}, true}
	}
	if r, ok := redisDestructive[cmd]; ok {
		return verdict{Report{Destructive, cmd, r}, true}
	}
	switch {
	case cmd == "SORT" || cmd == "GEORADIUS" || cmd == "GEORADIUSBYMEMBER":
		for _, a := range f[1:] {
			if a := strings.ToUpper(a); a == "STORE" || a == "STOREDIST" {
				return ok(Write, verb, cmd+" … STORE writes its result to a key")
			}
		}
		return ok(None, verb, "")
	case redisReads[verb] || redisReads[cmd]:
		return ok(None, verb, "")
	case redisKeyWrites[cmd]:
		if len(f) > 1 {
			return ok(Write, verb, fmt.Sprintf("%s modifies key %s", cmd, f[1]))
		}
		return ok(Write, verb, cmd+" modifies data")
	case redisOtherWrites[cmd] || redisContainers[cmd]:
		return ok(Write, verb, verb+" may modify data or server state")
	}
	return unknown(verb)
}
