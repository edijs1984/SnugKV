package server

import (
	"errors"
	"fmt"
)

// builtinFunctionsLibrary is the "snug" function library. It is loaded at
// startup (unless disabled) with replace semantics, so upgrading the server
// upgrades the library. Every function validates its arguments before it writes
// anything, so none of them can fail half way through.
const builtinFunctionsLibrary = `#!lua name=snug
local VERSION = '1'

local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local function bad(message)
  return redis.error_reply('ERR ' .. message)
end

-- Rate limiter (token bucket).
-- FCALL snug_rate_limit 1 <bucket> <capacity> <refill_per_second> [cost]
-- Returns {allowed (1/0), tokens left, milliseconds until the cost is available}.
local function rate_limit(keys, args)
  local capacity = tonumber(args[1])
  local rate = tonumber(args[2])
  local cost = tonumber(args[3] or '1')
  if not keys[1] or not capacity or not rate or not cost or capacity <= 0 or rate <= 0 or cost <= 0 or cost > capacity then
    return bad('usage: snug_rate_limit 1 bucket capacity refill_per_second [cost]; all numbers positive and cost <= capacity')
  end
  local now = now_ms()
  local state = redis.call('HMGET', keys[1], 'tokens', 'ts')
  local tokens = tonumber(state[1])
  local ts = tonumber(state[2])
  if tokens == nil or ts == nil then
    tokens = capacity
    ts = now
  end
  tokens = math.min(capacity, tokens + math.max(0, now - ts) * rate / 1000)
  local allowed = 0
  local retry = 0
  if tokens >= cost then
    tokens = tokens - cost
    allowed = 1
  else
    retry = math.ceil((cost - tokens) * 1000 / rate)
  end
  redis.call('HSET', keys[1], 'tokens', tostring(tokens), 'ts', tostring(now))
  redis.call('PEXPIRE', keys[1], math.ceil(capacity / rate * 1000) + 1000)
  return {allowed, math.floor(tokens), retry}
end

-- Distributed lock with a fencing token.
-- FCALL snug_lock_acquire 2 <lock> <fence-counter> <owner> <ttl_ms>
-- Returns the fencing token (an integer that grows with every new holder), or 0
-- if another owner holds the lock. The same owner acquiring again extends the
-- lock and gets the same token.
local function split_holder(value)
  local sep = string.find(value, ':', 1, true)
  return tonumber(string.sub(value, 1, sep - 1)), string.sub(value, sep + 1)
end

local function lock_acquire(keys, args)
  local owner = args[1]
  local ttl = tonumber(args[2])
  if not keys[1] or not keys[2] or not owner or owner == '' or not ttl or ttl <= 0 then
    return bad('usage: snug_lock_acquire 2 lock fence-counter owner ttl_ms')
  end
  local current = redis.call('GET', keys[1])
  if current then
    local fence, holder = split_holder(current)
    if holder == owner then
      redis.call('PEXPIRE', keys[1], ttl)
      return fence
    end
    return 0
  end
  local fence = redis.call('INCR', keys[2])
  redis.call('SET', keys[1], fence .. ':' .. owner, 'PX', ttl)
  return fence
end

-- FCALL snug_lock_release 1 <lock> <owner>  -> 1 if released, 0 if not the holder
local function lock_release(keys, args)
  local owner = args[1]
  if not keys[1] or not owner then
    return bad('usage: snug_lock_release 1 lock owner')
  end
  local current = redis.call('GET', keys[1])
  if current then
    local _, holder = split_holder(current)
    if holder == owner then
      return redis.call('DEL', keys[1])
    end
  end
  return 0
end

-- FCALL snug_lock_renew 1 <lock> <owner> <ttl_ms>  -> 1 if extended, 0 if not the holder
local function lock_renew(keys, args)
  local owner = args[1]
  local ttl = tonumber(args[2])
  if not keys[1] or not owner or not ttl or ttl <= 0 then
    return bad('usage: snug_lock_renew 1 lock owner ttl_ms')
  end
  local current = redis.call('GET', keys[1])
  if current then
    local _, holder = split_holder(current)
    if holder == owner then
      redis.call('PEXPIRE', keys[1], ttl)
      return 1
    end
  end
  return 0
end

-- Idempotency keys.
-- FCALL snug_idem_begin 1 <key> <ttl_ms>
--   {1}                      first caller: go ahead and do the work
--   {0, 'pending'}           another caller is doing it
--   {0, 'done', <result>}    already done: reuse the stored result
-- FCALL snug_idem_commit 1 <key> <result> <ttl_ms>  -> 1 stored, 0 if not pending
-- FCALL snug_idem_abort 1 <key>                     -> 1 released, 0 if not pending
local function idem_begin(keys, args)
  local ttl = tonumber(args[1])
  if not keys[1] or not ttl or ttl <= 0 then
    return bad('usage: snug_idem_begin 1 key ttl_ms')
  end
  local state = redis.call('HGET', keys[1], 'state')
  if not state then
    redis.call('HSET', keys[1], 'state', 'pending')
    redis.call('PEXPIRE', keys[1], ttl)
    return {1}
  end
  if state == 'pending' then
    return {0, 'pending'}
  end
  return {0, 'done', redis.call('HGET', keys[1], 'result') or ''}
end

local function idem_commit(keys, args)
  local result = args[1]
  local ttl = tonumber(args[2])
  if not keys[1] or not result or not ttl or ttl <= 0 then
    return bad('usage: snug_idem_commit 1 key result ttl_ms')
  end
  if redis.call('HGET', keys[1], 'state') ~= 'pending' then
    return 0
  end
  redis.call('HSET', keys[1], 'state', 'done', 'result', result)
  redis.call('PEXPIRE', keys[1], ttl)
  return 1
end

local function idem_abort(keys, args)
  if not keys[1] then
    return bad('usage: snug_idem_abort 1 key')
  end
  if redis.call('HGET', keys[1], 'state') ~= 'pending' then
    return 0
  end
  return redis.call('DEL', keys[1])
end

-- Bounded counter (stock, quota, seats). Integers only.
-- FCALL snug_counter_add 1 <key> <delta> <min> <max>
-- Applies the change only if the result stays within [min, max].
-- Returns {applied (1/0), value}.
local function counter_add(keys, args)
  local delta = tonumber(args[1])
  local lo = tonumber(args[2])
  local hi = tonumber(args[3])
  if not keys[1] or not delta or not lo or not hi or lo > hi
     or delta ~= math.floor(delta) or lo ~= math.floor(lo) or hi ~= math.floor(hi) then
    return bad('usage: snug_counter_add 1 key delta min max; integers, min <= max')
  end
  local current = tonumber(redis.call('GET', keys[1]) or '0')
  if current == nil then
    return bad('value is not an integer')
  end
  local nextValue = current + delta
  if nextValue < lo or nextValue > hi then
    return {0, current}
  end
  redis.call('SET', keys[1], tostring(nextValue))
  return {1, nextValue}
end

-- Reliable queue with acknowledgement and redelivery.
-- Keys: <ready> (list), <inflight> (sorted set of id by redelivery deadline),
-- <payloads> (hash of id to payload). Use one hash tag in all three in a cluster.
-- FCALL snug_queue_push 3 ready inflight payloads <payload>        -> id
-- FCALL snug_queue_pop 3 ready inflight payloads <visibility_ms>   -> {id, payload} or false
--   Messages not acknowledged within visibility_ms are delivered again.
-- FCALL snug_queue_ack 3 ready inflight payloads <id>              -> 1 acknowledged, 0 unknown
-- FCALL snug_queue_nack 3 ready inflight payloads <id>             -> 1 requeued, 0 unknown
local function queue_push(keys, args)
  if not keys[1] or not keys[2] or not keys[3] or args[1] == nil then
    return bad('usage: snug_queue_push 3 ready inflight payloads payload')
  end
  local id = redis.call('HINCRBY', keys[3], 'seq', 1)
  redis.call('HSET', keys[3], 'p' .. id, args[1])
  redis.call('RPUSH', keys[1], tostring(id))
  return id
end

local function queue_pop(keys, args)
  local visibility = tonumber(args[1])
  if not keys[1] or not keys[2] or not keys[3] or not visibility or visibility <= 0 then
    return bad('usage: snug_queue_pop 3 ready inflight payloads visibility_ms')
  end
  local now = now_ms()
  local expired = redis.call('ZRANGEBYSCORE', keys[2], '-inf', now)
  for _, id in ipairs(expired) do
    redis.call('ZREM', keys[2], id)
    redis.call('LPUSH', keys[1], id)
  end
  local id = redis.call('LPOP', keys[1])
  if not id then
    return false
  end
  redis.call('ZADD', keys[2], now + visibility, id)
  return {id, redis.call('HGET', keys[3], 'p' .. id) or ''}
end

local function queue_ack(keys, args)
  if not keys[1] or not keys[2] or not keys[3] or not args[1] then
    return bad('usage: snug_queue_ack 3 ready inflight payloads id')
  end
  if redis.call('ZREM', keys[2], args[1]) == 0 then
    return 0
  end
  redis.call('HDEL', keys[3], 'p' .. args[1])
  return 1
end

local function queue_nack(keys, args)
  if not keys[1] or not keys[2] or not keys[3] or not args[1] then
    return bad('usage: snug_queue_nack 3 ready inflight payloads id')
  end
  if redis.call('ZREM', keys[2], args[1]) == 0 then
    return 0
  end
  redis.call('RPUSH', keys[1], args[1])
  return 1
end

-- Leaderboard.
-- FCALL snug_leaderboard_submit 1 <board> <member> <score> [max|min|replace]
--   Records the score (default: keeps the better of old and new, highest wins).
--   Returns {rank (0 is first), score}.
-- FCALL snug_leaderboard_around 1 <board> <member> <radius>
--   Returns {rank, score, {member, score, member, score, ...}} for the member
--   and up to <radius> places above and below, best first. Rank is false if the
--   member is not on the board.
local function leaderboard_submit(keys, args)
  local member = args[1]
  local score = tonumber(args[2])
  local mode = args[3] or 'max'
  if not keys[1] or not member or not score or (mode ~= 'max' and mode ~= 'min' and mode ~= 'replace') then
    return bad('usage: snug_leaderboard_submit 1 board member score [max|min|replace]')
  end
  local current = redis.call('ZSCORE', keys[1], member)
  local write = true
  if current then
    current = tonumber(current)
    if mode == 'max' then write = score > current end
    if mode == 'min' then write = score < current end
  end
  if write then
    redis.call('ZADD', keys[1], tostring(score), member)
  end
  return {redis.call('ZREVRANK', keys[1], member), redis.call('ZSCORE', keys[1], member)}
end

local function leaderboard_around(keys, args)
  local member = args[1]
  local radius = tonumber(args[2])
  if not keys[1] or not member or not radius or radius < 0 or radius ~= math.floor(radius) then
    return bad('usage: snug_leaderboard_around 1 board member radius')
  end
  local rank = redis.call('ZREVRANK', keys[1], member)
  if not rank then
    return {false, false, {}}
  end
  local first = math.max(0, rank - radius)
  return {rank, redis.call('ZSCORE', keys[1], member),
          redis.call('ZREVRANGE', keys[1], first, rank + radius, 'WITHSCORES')}
end

local function version()
  return VERSION
end

redis.register_function{function_name='snug_rate_limit', callback=rate_limit, description='Token-bucket rate limiter'}
redis.register_function{function_name='snug_lock_acquire', callback=lock_acquire, description='Distributed lock with fencing token'}
redis.register_function{function_name='snug_lock_release', callback=lock_release, description='Release a lock held by owner'}
redis.register_function{function_name='snug_lock_renew', callback=lock_renew, description='Extend a lock held by owner'}
redis.register_function{function_name='snug_idem_begin', callback=idem_begin, description='Claim an idempotency key or fetch the stored result'}
redis.register_function{function_name='snug_idem_commit', callback=idem_commit, description='Store the result for an idempotency key'}
redis.register_function{function_name='snug_idem_abort', callback=idem_abort, description='Release a pending idempotency key'}
redis.register_function{function_name='snug_counter_add', callback=counter_add, description='Add to a counter within [min, max]'}
redis.register_function{function_name='snug_queue_push', callback=queue_push, description='Reliable queue: push'}
redis.register_function{function_name='snug_queue_pop', callback=queue_pop, description='Reliable queue: pop with redelivery deadline'}
redis.register_function{function_name='snug_queue_ack', callback=queue_ack, description='Reliable queue: acknowledge'}
redis.register_function{function_name='snug_queue_nack', callback=queue_nack, description='Reliable queue: return to the queue'}
redis.register_function{function_name='snug_leaderboard_submit', callback=leaderboard_submit, description='Submit a leaderboard score'}
redis.register_function{function_name='snug_leaderboard_around', callback=leaderboard_around, description='Leaderboard window around a member', flags={'no-writes'}}
redis.register_function{function_name='snug_functions_version', callback=version, description='Version of the built-in library', flags={'no-writes'}}
`

// LoadBuiltinFunctions loads (or replaces) the built-in "snug" function library.
func (s *TCPServer) LoadBuiltinFunctions() error {
	return s.server.loadBuiltinFunctions()
}

func (s *Server) loadBuiltinFunctions() error {
	lib, err := s.loadFunctionLibrary(builtinFunctionsLibrary)
	if err != nil {
		return fmt.Errorf("built-in functions: %w", err)
	}
	registry := functionRegistryForServer(s)
	if err := registry.load(lib, true); err != nil {
		lib.state.Close()
		return errors.New("built-in functions: " + err.Error())
	}
	return nil
}
