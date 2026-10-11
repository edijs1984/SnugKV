#!lua name=snugq
-- snugq: a BullMQ-style job queue as a function library.
-- Every function takes the queue prefix as KEYS[1], for example "snugq:{emails}:".
-- Job payloads are opaque strings (the Lua environment has no JSON), so clients
-- serialise data, return values and progress themselves.
local VERSION = '1'
local PRIO_MUL = 4294967296
local DELAY_MUL = 4096
local EVENTS_MAXLEN = '10000'
local METRICS_TTL_MS = 90000000

local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local function bad(message)
  return redis.error_reply('ERR ' .. message)
end

local function num(n)
  return string.format('%.0f', n)
end

local function event(p, name, id, extra)
  local a = {'XADD', p .. 'events', 'MAXLEN', '~', EVENTS_MAXLEN, '*', 'event', name, 'jobId', id}
  if extra then
    for i = 1, #extra do a[#a + 1] = extra[i] end
  end
  redis.call(unpack(a))
end

local function metric(p, kind, now)
  local minute = math.floor(now / 60000)
  local key = p .. 'metrics:' .. kind .. ':' .. num(minute)
  if redis.call('INCR', key) == 1 then
    redis.call('PEXPIRE', key, num(METRICS_TTL_MS))
  end
  redis.call('INCR', p .. 'metrics:' .. kind .. ':total')
end

local function push_ready(p, id, prio, front)
  if redis.call('HGET', p .. 'meta', 'paused') == '1' then
    redis.call(front and 'RPUSH' or 'LPUSH', p .. 'paused', id)
    return
  end
  if prio > 0 then
    local c = redis.call('INCR', p .. 'pc') % PRIO_MUL
    redis.call('ZADD', p .. 'prioritized', num(prio * PRIO_MUL + c), id)
  else
    redis.call(front and 'RPUSH' or 'LPUSH', p .. 'wait', id)
  end
  redis.call('PUBLISH', p .. 'wake', '1')
end

local function add_delayed(p, id, due)
  local c = redis.call('INCR', p .. 'dc') % DELAY_MUL
  redis.call('ZADD', p .. 'delayed', num(due * DELAY_MUL + c), id)
  redis.call('PUBLISH', p .. 'wake', 'd')
end

local function release_dedup(p, id, deid)
  if deid and deid ~= '' then
    local key = p .. 'de:' .. deid
    if redis.call('GET', key) == id and redis.call('PTTL', key) == -1 then
      redis.call('DEL', key)
    end
  end
end

local function del_job(p, id)
  local jk = p .. 'job:' .. id
  release_dedup(p, id, redis.call('HGET', jk, 'deid'))
  redis.call('DEL', jk, jk .. ':logs', jk .. ':processed', jk .. ':failed', jk .. ':deps', p .. 'lock:' .. id)
end

local function trim_set(p, set, count, age, now)
  if age > 0 then
    local old = redis.call('ZRANGEBYSCORE', p .. set, '-inf', num(now - age * 1000))
    for _, id in ipairs(old) do
      redis.call('ZREM', p .. set, id)
      del_job(p, id)
    end
  end
  if count >= 0 then
    local card = redis.call('ZCARD', p .. set)
    if card > count then
      local extra = redis.call('ZRANGE', p .. set, 0, card - count - 1)
      for _, id in ipairs(extra) do
        redis.call('ZREM', p .. set, id)
        del_job(p, id)
      end
    end
  end
end

-- A child finished (or was removed). Release the parent when nothing is pending.
local function child_done(pq, pid, ref, rv)
  local pk = pq .. 'job:' .. pid
  if redis.call('EXISTS', pk) == 0 then return end
  if rv then
    redis.call('HSET', pk .. ':processed', ref, rv)
    redis.call('HDEL', pk .. ':failed', ref)
  end
  redis.call('SREM', pk .. ':deps', ref)
  local left = redis.call('HINCRBY', pk, 'pend', -1)
  if left <= 0 and redis.call('ZREM', pq .. 'waiting-children', pid) == 1 then
    push_ready(pq, pid, tonumber(redis.call('HGET', pk, 'prio') or '0'), false)
    event(pq, 'waiting', pid, {'prev', 'waiting-children'})
  end
end

local finalize_failed
finalize_failed = function(p, id, reason, stack, now)
  local jk = p .. 'job:' .. id
  local h = redis.call('HMGET', jk, 'rmf', 'rmfa', 'pq', 'pid', 'onfail', 'deid')
  redis.call('HSET', jk, 'fr', reason, 'st', stack, 'fo', num(now))
  release_dedup(p, id, h[6])
  local keep = tonumber(h[1] or '-1')
  local age = tonumber(h[2] or '0')
  if keep == 0 then
    del_job(p, id)
  else
    redis.call('ZADD', p .. 'failed', num(now), id)
    trim_set(p, 'failed', keep, age, now)
  end
  metric(p, 'failed', now)
  event(p, 'failed', id, {'failedReason', reason, 'prev', 'active'})
  local pq, pid, onfail = h[3], h[4], h[5]
  if pq and pq ~= '' then
    local ref = p .. '|' .. id
    if onfail == 'fail' then
      if redis.call('ZREM', pq .. 'waiting-children', pid) == 1 then
        finalize_failed(pq, pid, 'child ' .. ref .. ' failed: ' .. reason, '', now)
      end
    elseif onfail == 'ignore' or onfail == 'remove' then
      local pk = pq .. 'job:' .. pid
      if onfail == 'ignore' and redis.call('EXISTS', pk) == 1 then
        redis.call('HSET', pk .. ':failed', ref, reason)
      end
      child_done(pq, pid, ref, nil)
    else
      local pk = pq .. 'job:' .. pid
      if redis.call('EXISTS', pk) == 1 then
        redis.call('HSET', pk .. ':failed', ref, reason)
      end
    end
  end
end

local function register_queue(p)
  local reg = string.match(p, '^(.*):{') or 'snugq'
  local name = string.match(p, '{(.*)}:$')
  if name then redis.call('SADD', reg .. ':queues', name) end
end

---------------------------------------------------------------------------
-- Adding jobs
---------------------------------------------------------------------------

local JOB_ARGS = 19

-- Parses the 19 job arguments starting at a[off + 1].
local function parse_job(a, off)
  local o = {
    name = a[off + 1], data = a[off + 2], jobid = a[off + 3] or '',
    delay = tonumber(a[off + 4] or '0'), prio = tonumber(a[off + 5] or '0'),
    lifo = a[off + 6] == '1', att = tonumber(a[off + 7] or '1'),
    bt = a[off + 8] or '', bd = tonumber(a[off + 9] or '0'),
    rmc = tonumber(a[off + 10] or '-1'), rmca = tonumber(a[off + 11] or '0'),
    rmf = tonumber(a[off + 12] or '-1'), rmfa = tonumber(a[off + 13] or '0'),
    pq = a[off + 14] or '', pid = a[off + 15] or '', onfail = a[off + 16] or '',
    wc = a[off + 17] == '1', dd = a[off + 18] or '', ddttl = tonumber(a[off + 19] or '0'),
  }
  if not o.name or not o.data or not o.delay or not o.prio or not o.att or not o.bd
      or not o.rmc or not o.rmca or not o.rmf or not o.rmfa or not o.ddttl then
    return nil, 'job arguments must be: name data jobId delay priority lifo attempts backoffType backoffDelay removeOnComplete(count age) removeOnFail(count age) parentPrefix parentId onFail waitChildren dedupId dedupTtl'
  end
  if o.delay < 0 or o.prio < 0 or o.prio > 2097152 or o.att < 1 or o.bd < 0 or o.ddttl < 0 then
    return nil, 'delay, priority (0..2097152), attempts (>=1), backoff delay and dedup ttl must be valid'
  end
  if o.bt ~= '' and o.bt ~= 'fixed' and o.bt ~= 'exponential' then
    return nil, 'backoff type must be fixed or exponential'
  end
  if o.onfail ~= '' and o.onfail ~= 'fail' and o.onfail ~= 'ignore' and o.onfail ~= 'remove' then
    return nil, 'onFail must be fail, ignore or remove'
  end
  if o.jobid ~= '' and string.find(o.jobid, '^%d+$') then
    return nil, 'custom job ids cannot be integers'
  end
  if string.find(o.jobid, '|', 1, true) then
    return nil, "custom job ids cannot contain '|'"
  end
  return o
end

-- Returns id, added (1 when a new job was created).
local function add_job(p, o, now)
  if o.pq ~= '' and redis.call('EXISTS', o.pq .. 'job:' .. o.pid) == 0 then
    return nil, 'parent job not found'
  end
  if o.jobid ~= '' and redis.call('EXISTS', p .. 'job:' .. o.jobid) == 1 then
    event(p, 'duplicated', o.jobid)
    return o.jobid, 0
  end
  if o.dd ~= '' then
    local cur = redis.call('GET', p .. 'de:' .. o.dd)
    if cur then
      event(p, 'deduplicated', cur, {'deduplicationId', o.dd})
      return cur, 0
    end
  end
  local id = o.jobid
  if id == '' then id = tostring(redis.call('INCR', p .. 'id')) end
  register_queue(p)
  if o.dd ~= '' then
    if o.ddttl > 0 then
      redis.call('SET', p .. 'de:' .. o.dd, id, 'PX', num(o.ddttl))
    else
      redis.call('SET', p .. 'de:' .. o.dd, id)
    end
  end
  local jk = p .. 'job:' .. id
  local f = {
    'name', o.name, 'data', o.data, 'ts', num(now), 'delay', num(o.delay), 'prio', num(o.prio),
    'att', num(o.att), 'atm', '0', 'rmc', num(o.rmc), 'rmca', num(o.rmca), 'rmf', num(o.rmf), 'rmfa', num(o.rmfa),
    'pend', '0',
  }
  local function opt(k, v)
    if v and v ~= '' then f[#f + 1] = k; f[#f + 1] = v end
  end
  opt('bt', o.bt)
  if o.bt ~= '' then opt('bd', num(o.bd)) end
  opt('pq', o.pq); opt('pid', o.pid); opt('onfail', o.onfail)
  if o.dd ~= '' then opt('deid', o.dd) end
  opt('rep', o.rep); opt('pat', o.pat)
  if o.lifo then opt('lifo', '1') end
  redis.call('HSET', jk, unpack(f))
  if o.pq ~= '' then
    local pk = o.pq .. 'job:' .. o.pid
    redis.call('HINCRBY', pk, 'pend', 1)
    redis.call('SADD', pk .. ':deps', p .. '|' .. id)
  end
  event(p, 'added', id, {'name', o.name})
  if o.wc then
    redis.call('ZADD', p .. 'waiting-children', num(now), id)
    event(p, 'waiting-children', id)
  elseif o.delay > 0 then
    add_delayed(p, id, now + o.delay)
    event(p, 'delayed', id, {'delay', num(now + o.delay)})
  else
    push_ready(p, id, o.prio, o.lifo)
    event(p, 'waiting', id)
  end
  return id, 1
end

-- FCALL snugq_add 1 <prefix> name data jobId delay priority lifo attempts backoffType backoffDelay
--   rmcCount rmcAge rmfCount rmfAge parentPrefix parentId onFail waitChildren dedupId dedupTtl
-- Returns {id, added}.
local function q_add(keys, args)
  local p = keys[1]
  if not p then return bad('usage: snugq_add 1 prefix name data ...') end
  local o, err = parse_job(args, 0)
  if not o then return bad(err) end
  local id, added = add_job(p, o, now_ms())
  if not id then return bad(added) end
  return {id, added}
end

---------------------------------------------------------------------------
-- Schedulers (repeatable jobs)
---------------------------------------------------------------------------

local TEMPLATE_FIELDS = {'name', 'data', 'prio', 'lifo', 'att', 'bt', 'bd', 'rmc', 'rmca', 'rmf', 'rmfa'}

local function spawn_scheduled(p, sid, nextms, now)
  local sk = p .. 'repeat:' .. sid
  local t = redis.call('HGETALL', sk)
  local h = {}
  for i = 1, #t, 2 do h[t[i]] = t[i + 1] end
  if not h.name then return 0 end
  local limit = tonumber(h.limit or '0')
  local endat = tonumber(h.endAt or '0')
  local count = tonumber(h.count or '0')
  if (limit > 0 and count >= limit) or (endat > 0 and nextms > endat) then
    return 0
  end
  local id = 'repeat:' .. sid .. ':' .. num(nextms)
  if redis.call('EXISTS', p .. 'job:' .. id) == 1 then return 0 end
  local o = {
    name = h.name, data = h.data or '', jobid = id, delay = math.max(0, nextms - now),
    prio = tonumber(h.prio or '0'), lifo = h.lifo == '1', att = tonumber(h.att or '1'),
    bt = h.bt or '', bd = tonumber(h.bd or '0'), rmc = tonumber(h.rmc or '-1'), rmca = tonumber(h.rmca or '0'),
    rmf = tonumber(h.rmf or '-1'), rmfa = tonumber(h.rmfa or '0'),
    pq = '', pid = '', onfail = '', wc = false, dd = '', ddttl = 0, rep = sid, pat = h.pat or '',
  }
  add_job(p, o, now)
  redis.call('HINCRBY', sk, 'count', 1)
  redis.call('HSET', sk, 'next', num(nextms))
  redis.call('ZADD', p .. 'repeat', num(nextms), sid)
  return 1
end

local function every_next(every, prev, now)
  local nextms = prev + every
  if nextms <= now then
    nextms = math.floor(now / every) * every + every
  end
  return nextms
end

-- FCALL snugq_upsert_scheduler 1 <prefix> sid every pattern nextMs limit endAt <19 job args>
-- every (ms) or pattern (opaque, the client computes nextMs from it) must be set.
-- The first job is created now. Returns the first job id, or 0 when nothing was created.
local function q_upsert_scheduler(keys, args)
  local p = keys[1]
  local sid = args[1]
  local every = tonumber(args[2] or '0')
  local pat = args[3] or ''
  local nextms = tonumber(args[4] or '0')
  local limit = tonumber(args[5] or '0')
  local endat = tonumber(args[6] or '0')
  if not p or not sid or sid == '' or not every or not nextms or not limit or not endat then
    return bad('usage: snugq_upsert_scheduler 1 prefix id every pattern nextMs limit endAt ...job args')
  end
  if (every <= 0) == (pat == '') then
    return bad('set exactly one of every and pattern')
  end
  if pat ~= '' and nextms <= 0 then
    return bad('a pattern scheduler needs nextMs')
  end
  local o, err = parse_job(args, 6)
  if not o then return bad(err) end
  local now = now_ms()
  local sk = p .. 'repeat:' .. sid
  -- Replace an existing scheduler and its pending job.
  local prev = redis.call('ZSCORE', p .. 'repeat', sid)
  if prev then
    local pid = 'repeat:' .. sid .. ':' .. num(tonumber(prev))
    if redis.call('ZREM', p .. 'delayed', pid) == 1 then del_job(p, pid) end
  end
  redis.call('DEL', sk)
  local f = {
    'every', num(every), 'pat', pat, 'limit', num(limit), 'endAt', num(endat), 'count', '0',
    'name', o.name, 'data', o.data, 'prio', num(o.prio), 'lifo', o.lifo and '1' or '0', 'att', num(o.att),
    'bt', o.bt, 'bd', num(o.bd), 'rmc', num(o.rmc), 'rmca', num(o.rmca), 'rmf', num(o.rmf), 'rmfa', num(o.rmfa),
  }
  redis.call('HSET', sk, unpack(f))
  if every > 0 then nextms = now end
  redis.call('ZADD', p .. 'repeat', num(nextms), sid)
  register_queue(p)
  return spawn_scheduled(p, sid, nextms, now)
end

-- FCALL snugq_scheduler_next 1 <prefix> sid nextMs
-- Creates the next job of a pattern scheduler (called by the worker that started the previous one).
local function q_scheduler_next(keys, args)
  local p = keys[1]
  local nextms = tonumber(args[2] or '')
  if not p or not args[1] or not nextms then return bad('usage: snugq_scheduler_next 1 prefix id nextMs') end
  return spawn_scheduled(p, args[1], nextms, now_ms())
end

-- FCALL snugq_remove_scheduler 1 <prefix> sid -> 1 when it existed
local function q_remove_scheduler(keys, args)
  local p = keys[1]
  local sid = args[1]
  if not p or not sid then return bad('usage: snugq_remove_scheduler 1 prefix id') end
  local prev = redis.call('ZSCORE', p .. 'repeat', sid)
  local existed = redis.call('DEL', p .. 'repeat:' .. sid)
  redis.call('ZREM', p .. 'repeat', sid)
  if prev then
    local pid = 'repeat:' .. sid .. ':' .. num(tonumber(prev))
    if redis.call('ZREM', p .. 'delayed', pid) == 1 then del_job(p, pid) end
  end
  return existed
end

---------------------------------------------------------------------------
-- Fetching and finishing jobs
---------------------------------------------------------------------------

local function promote_due(p, now)
  local due = redis.call('ZRANGEBYSCORE', p .. 'delayed', '-inf', num(now * DELAY_MUL + DELAY_MUL - 1), 'LIMIT', 0, 1000)
  for _, id in ipairs(due) do
    redis.call('ZREM', p .. 'delayed', id)
    push_ready(p, id, tonumber(redis.call('HGET', p .. 'job:' .. id, 'prio') or '0'), false)
    event(p, 'waiting', id, {'prev', 'delayed'})
  end
end

-- FCALL snugq_fetch 1 <prefix> token lockMs
-- Returns {'job', id, {field, value, ...}}, {'empty', msUntilNextDelayed|-1},
-- {'limited', ms}, {'paused'} or {'busy'}.
local function q_fetch(keys, args)
  local p = keys[1]
  local token = args[1]
  local lockms = tonumber(args[2] or '')
  if not p or not token or token == '' or not lockms or lockms <= 0 then
    return bad('usage: snugq_fetch 1 prefix token lockMs')
  end
  local now = now_ms()
  promote_due(p, now)
  local meta = redis.call('HMGET', p .. 'meta', 'paused', 'gconc', 'max', 'duration')
  if meta[1] == '1' then return {'paused'} end
  local gconc = tonumber(meta[2] or '0')
  if gconc and gconc > 0 and redis.call('LLEN', p .. 'active') >= gconc then return {'busy'} end
  local manual = redis.call('PTTL', p .. 'limited')
  if manual > 0 then return {'limited', manual} end
  local max = tonumber(meta[3] or '0')
  local duration = tonumber(meta[4] or '0')
  local use_limiter = max and max > 0 and duration and duration > 0
  if use_limiter then
    local ttl = redis.call('PTTL', p .. 'limiter')
    if ttl > 0 and tonumber(redis.call('GET', p .. 'limiter') or '0') >= max then
      return {'limited', ttl}
    end
  end
  local id
  for _ = 1, 100 do
    id = redis.call('RPOP', p .. 'wait')
    if not id then
      local z = redis.call('ZPOPMIN', p .. 'prioritized')
      id = z[1]
    end
    if not id then break end
    if redis.call('EXISTS', p .. 'job:' .. id) == 1 then break end
    id = nil
  end
  if not id then
    local nxt = redis.call('ZRANGE', p .. 'delayed', 0, 0, 'WITHSCORES')
    local wait = -1
    if nxt[2] then wait = math.max(0, math.floor(tonumber(nxt[2]) / DELAY_MUL) - now) end
    return {'empty', wait}
  end
  if use_limiter then
    if redis.call('INCR', p .. 'limiter') == 1 then
      redis.call('PEXPIRE', p .. 'limiter', num(duration))
    end
  end
  local jk = p .. 'job:' .. id
  redis.call('LPUSH', p .. 'active', id)
  redis.call('SET', p .. 'lock:' .. id, token, 'PX', num(lockms))
  redis.call('HINCRBY', jk, 'atm', 1)
  redis.call('HSET', jk, 'po', num(now), 'tk', token)
  event(p, 'active', id, {'prev', 'waiting'})
  local rep = redis.call('HGET', jk, 'rep')
  if rep and rep ~= '' and not redis.call('HGET', jk, 'pat') then
    local every = tonumber(redis.call('HGET', p .. 'repeat:' .. rep, 'every') or '0')
    if every and every > 0 then
      local own = tonumber(string.match(id, ':(%d+)$') or num(now))
      spawn_scheduled(p, rep, every_next(every, own, now), now)
    end
  end
  return {'job', id, redis.call('HGETALL', jk)}
end

local function lock_state(p, id, token)
  if redis.call('EXISTS', p .. 'job:' .. id) == 0 then return -1 end
  if redis.call('GET', p .. 'lock:' .. id) ~= token then return -2 end
  return 0
end

-- FCALL snugq_complete 1 <prefix> id token returnValue -> 0 | -1 missing job | -2 lock lost
local function q_complete(keys, args)
  local p, id, token, rv = keys[1], args[1], args[2], args[3]
  if not p or not id or not token or not rv then return bad('usage: snugq_complete 1 prefix id token returnValue') end
  local state = lock_state(p, id, token)
  if state ~= 0 then return state end
  local now = now_ms()
  local jk = p .. 'job:' .. id
  local h = redis.call('HMGET', jk, 'rmc', 'rmca', 'pq', 'pid', 'deid')
  redis.call('LREM', p .. 'active', 1, id)
  redis.call('DEL', p .. 'lock:' .. id)
  redis.call('HSET', jk, 'rv', rv, 'fo', num(now))
  redis.call('HDEL', jk, 'fr', 'st')
  release_dedup(p, id, h[5])
  local keep = tonumber(h[1] or '-1')
  local age = tonumber(h[2] or '0')
  if keep == 0 then
    del_job(p, id)
  else
    redis.call('ZADD', p .. 'completed', num(now), id)
    trim_set(p, 'completed', keep, age, now)
  end
  metric(p, 'completed', now)
  event(p, 'completed', id, {'returnvalue', rv, 'prev', 'active'})
  if h[3] and h[3] ~= '' then
    child_done(h[3], h[4], p .. '|' .. id, rv)
  end
  return 0
end

local function compute_backoff(bt, bd, atm)
  if bt == 'fixed' then return bd end
  if bt == 'exponential' then return bd * (2 ^ (atm - 1)) end
  return 0
end

-- FCALL snugq_fail 1 <prefix> id token reason stack final customBackoffMs
-- final=1 never retries. customBackoffMs >= 0 overrides the configured backoff.
-- Returns 'retry', 'failed', or -1 / -2 like complete.
local function q_fail(keys, args)
  local p, id, token, reason, stack = keys[1], args[1], args[2], args[3], args[4]
  local final = args[5] == '1'
  local custom = tonumber(args[6] or '-1')
  if not p or not id or not token or not reason or not stack or not custom then
    return bad('usage: snugq_fail 1 prefix id token reason stack final customBackoffMs')
  end
  local state = lock_state(p, id, token)
  if state ~= 0 then return state end
  local now = now_ms()
  local jk = p .. 'job:' .. id
  redis.call('LREM', p .. 'active', 1, id)
  redis.call('DEL', p .. 'lock:' .. id)
  local h = redis.call('HMGET', jk, 'atm', 'att', 'bt', 'bd', 'prio')
  local atm, att = tonumber(h[1] or '1'), tonumber(h[2] or '1')
  if not final and atm < att then
    local delay = custom
    if delay < 0 then delay = compute_backoff(h[3] or '', tonumber(h[4] or '0'), atm) end
    redis.call('HSET', jk, 'fr', reason, 'st', stack)
    if delay > 0 then
      add_delayed(p, id, now + delay)
      event(p, 'delayed', id, {'delay', num(now + delay), 'failedReason', reason, 'prev', 'active'})
    else
      push_ready(p, id, tonumber(h[5] or '0'), true)
      event(p, 'waiting', id, {'failedReason', reason, 'prev', 'active'})
    end
    event(p, 'retries', id, {'attemptsMade', num(atm)})
    return 'retry'
  end
  finalize_failed(p, id, reason, stack, now)
  return 'failed'
end

-- FCALL snugq_move_to_delayed 1 <prefix> id token delayMs
local function q_move_to_delayed(keys, args)
  local p, id, token, delay = keys[1], args[1], args[2], tonumber(args[3] or '')
  if not p or not id or not token or not delay or delay < 0 then return bad('usage: snugq_move_to_delayed 1 prefix id token delayMs') end
  local state = lock_state(p, id, token)
  if state ~= 0 then return state end
  local now = now_ms()
  redis.call('LREM', p .. 'active', 1, id)
  redis.call('DEL', p .. 'lock:' .. id)
  redis.call('HSET', p .. 'job:' .. id, 'delay', num(delay))
  add_delayed(p, id, now + delay)
  event(p, 'delayed', id, {'delay', num(now + delay), 'prev', 'active'})
  return 0
end

-- FCALL snugq_move_to_wait 1 <prefix> id token limitMs
-- Puts an active job back without using an attempt; limitMs > 0 also pauses fetching for that long
-- (worker.rateLimit).
local function q_move_to_wait(keys, args)
  local p, id, token, limit = keys[1], args[1], args[2], tonumber(args[3] or '0')
  if not p or not id or not token or not limit or limit < 0 then return bad('usage: snugq_move_to_wait 1 prefix id token limitMs') end
  local state = lock_state(p, id, token)
  if state ~= 0 then return state end
  local jk = p .. 'job:' .. id
  redis.call('LREM', p .. 'active', 1, id)
  redis.call('DEL', p .. 'lock:' .. id)
  redis.call('HINCRBY', jk, 'atm', -1)
  if limit > 0 then redis.call('SET', p .. 'limited', '1', 'PX', num(limit)) end
  push_ready(p, id, tonumber(redis.call('HGET', jk, 'prio') or '0'), true)
  event(p, 'waiting', id, {'prev', 'active'})
  return 0
end

-- FCALL snugq_extend_lock 1 <prefix> id token ms -> 1 extended, 0 lost
local function q_extend_lock(keys, args)
  local p, id, token, ms = keys[1], args[1], args[2], tonumber(args[3] or '')
  if not p or not id or not token or not ms or ms <= 0 then return bad('usage: snugq_extend_lock 1 prefix id token ms') end
  if redis.call('GET', p .. 'lock:' .. id) == token then
    redis.call('PEXPIRE', p .. 'lock:' .. id, num(ms))
    return 1
  end
  return 0
end

-- FCALL snugq_stalled 1 <prefix> maxStalledCount intervalMs
-- Finds active jobs whose lock expired. Runs at most once per interval across all workers.
-- Returns {{stalled ids}, {failed ids}}.
local function q_stalled(keys, args)
  local p, maxs, interval = keys[1], tonumber(args[1] or ''), tonumber(args[2] or '')
  if not p or not maxs or maxs < 0 or not interval or interval <= 0 then return bad('usage: snugq_stalled 1 prefix maxStalledCount intervalMs') end
  if not redis.call('SET', p .. 'stalled-check', '1', 'NX', 'PX', num(interval)) then
    return {{}, {}}
  end
  local now = now_ms()
  local stalled, failed = {}, {}
  for _, id in ipairs(redis.call('LRANGE', p .. 'active', 0, -1)) do
    if redis.call('EXISTS', p .. 'lock:' .. id) == 0 then
      local jk = p .. 'job:' .. id
      redis.call('LREM', p .. 'active', 1, id)
      if redis.call('EXISTS', jk) == 1 then
        local count = redis.call('HINCRBY', jk, 'stc', 1)
        if count > maxs then
          finalize_failed(p, id, 'job stalled more than allowable limit', '', now)
          failed[#failed + 1] = id
        else
          push_ready(p, id, tonumber(redis.call('HGET', jk, 'prio') or '0'), true)
          event(p, 'stalled', id)
          event(p, 'waiting', id, {'prev', 'active'})
          stalled[#stalled + 1] = id
        end
      end
    end
  end
  return {stalled, failed}
end

---------------------------------------------------------------------------
-- Job updates
---------------------------------------------------------------------------

-- FCALL snugq_progress 1 <prefix> id progress -> 1 | 0 missing
local function q_progress(keys, args)
  local p, id, progress = keys[1], args[1], args[2]
  if not p or not id or not progress then return bad('usage: snugq_progress 1 prefix id progress') end
  if redis.call('EXISTS', p .. 'job:' .. id) == 0 then return 0 end
  redis.call('HSET', p .. 'job:' .. id, 'prg', progress)
  event(p, 'progress', id, {'data', progress})
  return 1
end

-- FCALL snugq_update_data 1 <prefix> id data
local function q_update_data(keys, args)
  local p, id, data = keys[1], args[1], args[2]
  if not p or not id or not data then return bad('usage: snugq_update_data 1 prefix id data') end
  if redis.call('EXISTS', p .. 'job:' .. id) == 0 then return 0 end
  redis.call('HSET', p .. 'job:' .. id, 'data', data)
  return 1
end

-- FCALL snugq_log 1 <prefix> id line keepLogs -> log length
local function q_log(keys, args)
  local p, id, line, keep = keys[1], args[1], args[2], tonumber(args[3] or '0')
  if not p or not id or not line or not keep then return bad('usage: snugq_log 1 prefix id line keepLogs') end
  if redis.call('EXISTS', p .. 'job:' .. id) == 0 then return -1 end
  local len = redis.call('RPUSH', p .. 'job:' .. id .. ':logs', line)
  if keep > 0 and len > keep then
    redis.call('LTRIM', p .. 'job:' .. id .. ':logs', -keep, -1)
    len = keep
  end
  return len
end

---------------------------------------------------------------------------
-- Queue control
---------------------------------------------------------------------------

-- FCALL snugq_pause 1 <prefix>
local function q_pause(keys)
  local p = keys[1]
  if not p then return bad('usage: snugq_pause 1 prefix') end
  if redis.call('HGET', p .. 'meta', 'paused') == '1' then return 0 end
  redis.call('HSET', p .. 'meta', 'paused', '1')
  if redis.call('EXISTS', p .. 'wait') == 1 then redis.call('RENAME', p .. 'wait', p .. 'paused') end
  event(p, 'paused', '')
  return 1
end

-- FCALL snugq_resume 1 <prefix>
local function q_resume(keys)
  local p = keys[1]
  if not p then return bad('usage: snugq_resume 1 prefix') end
  if redis.call('HGET', p .. 'meta', 'paused') ~= '1' then return 0 end
  redis.call('HDEL', p .. 'meta', 'paused')
  for _, id in ipairs(redis.call('LRANGE', p .. 'paused', 0, -1)) do
    local prio = tonumber(redis.call('HGET', p .. 'job:' .. id, 'prio') or '0')
    if prio > 0 then
      redis.call('LREM', p .. 'paused', 1, id)
      push_ready(p, id, prio, false)
    end
  end
  if redis.call('EXISTS', p .. 'paused') == 1 then
    if redis.call('EXISTS', p .. 'wait') == 0 then
      redis.call('RENAME', p .. 'paused', p .. 'wait')
    else
      while true do
        local id = redis.call('RPOP', p .. 'paused')
        if not id then break end
        redis.call('LPUSH', p .. 'wait', id)
      end
    end
  end
  redis.call('PUBLISH', p .. 'wake', '1')
  event(p, 'resumed', '')
  return 1
end

local LISTS = {wait = true, paused = true, active = true}
local ZSETS = {delayed = true, prioritized = true, completed = true, failed = true, ['waiting-children'] = true}

-- FCALL_RO snugq_counts 1 <prefix> -> {type, count, ...}
local function q_counts(keys)
  local p = keys[1]
  if not p then return bad('usage: snugq_counts 1 prefix') end
  return {
    'wait', redis.call('LLEN', p .. 'wait'),
    'paused', redis.call('LLEN', p .. 'paused'),
    'active', redis.call('LLEN', p .. 'active'),
    'delayed', redis.call('ZCARD', p .. 'delayed'),
    'prioritized', redis.call('ZCARD', p .. 'prioritized'),
    'completed', redis.call('ZCARD', p .. 'completed'),
    'failed', redis.call('ZCARD', p .. 'failed'),
    'waiting-children', redis.call('ZCARD', p .. 'waiting-children'),
  }
end

-- FCALL_RO snugq_jobs 1 <prefix> type start end asc -> {{id, {field, value, ...}}, ...}
-- Lists are in processing order (next job first); sorted sets by score.
local function q_jobs(keys, args)
  local p, kind = keys[1], args[1]
  local s, e = tonumber(args[2] or ''), tonumber(args[3] or '')
  if not p or not kind or not s or not e or s < 0 or (not LISTS[kind] and not ZSETS[kind]) then
    return bad('usage: snugq_jobs 1 prefix type start end asc')
  end
  local asc = args[4] ~= '0'
  local ids
  if LISTS[kind] then
    local from, to = 0, -(s + 1)
    if e >= 0 then from = -(e + 1) end
    ids = redis.call('LRANGE', p .. kind, from, to)
    local rev = {}
    for i = #ids, 1, -1 do rev[#rev + 1] = ids[i] end
    ids = rev
    if not asc then
      local back = {}
      for i = #ids, 1, -1 do back[#back + 1] = ids[i] end
      ids = back
    end
  else
    ids = redis.call(asc and 'ZRANGE' or 'ZREVRANGE', p .. kind, s, e)
  end
  local out = {}
  for _, id in ipairs(ids) do
    out[#out + 1] = {id, redis.call('HGETALL', p .. 'job:' .. id)}
  end
  return out
end

-- Removes a job from every structure. Returns 1 removed, 0 not found, -1 locked.
local function remove_job(p, id, depth)
  local jk = p .. 'job:' .. id
  if redis.call('EXISTS', jk) == 0 then return 0 end
  if redis.call('EXISTS', p .. 'lock:' .. id) == 1 then return -1 end
  local h = redis.call('HMGET', jk, 'pq', 'pid')
  if depth < 8 then
    for _, ref in ipairs(redis.call('SMEMBERS', jk .. ':deps')) do
      local cp, cid = string.match(ref, '^(.*)|([^|]*)$')
      if cp then remove_job(cp, cid, depth + 1) end
    end
  end
  redis.call('LREM', p .. 'wait', 0, id)
  redis.call('LREM', p .. 'paused', 0, id)
  redis.call('LREM', p .. 'active', 0, id)
  redis.call('ZREM', p .. 'prioritized', id)
  redis.call('ZREM', p .. 'delayed', id)
  redis.call('ZREM', p .. 'completed', id)
  redis.call('ZREM', p .. 'failed', id)
  redis.call('ZREM', p .. 'waiting-children', id)
  del_job(p, id)
  if h[1] and h[1] ~= '' then child_done(h[1], h[2], p .. '|' .. id, nil) end
  event(p, 'removed', id)
  return 1
end

-- FCALL snugq_remove 1 <prefix> id
local function q_remove(keys, args)
  if not keys[1] or not args[1] then return bad('usage: snugq_remove 1 prefix id') end
  return remove_job(keys[1], args[1], 0)
end

-- FCALL snugq_retry 1 <prefix> id state(failed|completed) resetAttempts
-- Returns 0, -1 missing job, -5 job is not in that state.
local function q_retry(keys, args)
  local p, id, from = keys[1], args[1], args[2]
  if not p or not id or (from ~= 'failed' and from ~= 'completed') then return bad('usage: snugq_retry 1 prefix id failed|completed resetAttempts') end
  local jk = p .. 'job:' .. id
  if redis.call('EXISTS', jk) == 0 then return -1 end
  if redis.call('ZREM', p .. from, id) == 0 then return -5 end
  redis.call('HDEL', jk, 'fr', 'st', 'fo', 'rv')
  if args[3] == '1' then redis.call('HSET', jk, 'atm', '0') end
  redis.call('HSET', jk, 'stc', '0')
  push_ready(p, id, tonumber(redis.call('HGET', jk, 'prio') or '0'), false)
  event(p, 'waiting', id, {'prev', from})
  return 0
end

-- FCALL snugq_promote 1 <prefix> id -> 0 | -1 missing | -3 not delayed
local function q_promote(keys, args)
  local p, id = keys[1], args[1]
  if not p or not id then return bad('usage: snugq_promote 1 prefix id') end
  if redis.call('EXISTS', p .. 'job:' .. id) == 0 then return -1 end
  if redis.call('ZREM', p .. 'delayed', id) == 0 then return -3 end
  redis.call('HSET', p .. 'job:' .. id, 'delay', '0')
  push_ready(p, id, tonumber(redis.call('HGET', p .. 'job:' .. id, 'prio') or '0'), false)
  event(p, 'waiting', id, {'prev', 'delayed'})
  return 0
end

-- FCALL snugq_change_priority 1 <prefix> id priority lifo -> 0 | -1 missing
local function q_change_priority(keys, args)
  local p, id, prio = keys[1], args[1], tonumber(args[2] or '')
  if not p or not id or not prio or prio < 0 or prio > 2097152 then return bad('usage: snugq_change_priority 1 prefix id priority(0..2097152) lifo') end
  local jk = p .. 'job:' .. id
  if redis.call('EXISTS', jk) == 0 then return -1 end
  redis.call('HSET', jk, 'prio', num(prio))
  local moved = redis.call('ZREM', p .. 'prioritized', id) + redis.call('LREM', p .. 'wait', 0, id)
  if moved > 0 then push_ready(p, id, prio, args[3] == '1') end
  return 0
end

-- FCALL snugq_change_delay 1 <prefix> id delayMs -> 0 | -1 missing | -3 not delayed
local function q_change_delay(keys, args)
  local p, id, delay = keys[1], args[1], tonumber(args[2] or '')
  if not p or not id or not delay or delay < 0 then return bad('usage: snugq_change_delay 1 prefix id delayMs') end
  if redis.call('EXISTS', p .. 'job:' .. id) == 0 then return -1 end
  if redis.call('ZREM', p .. 'delayed', id) == 0 then return -3 end
  redis.call('HSET', p .. 'job:' .. id, 'delay', num(delay))
  add_delayed(p, id, now_ms() + delay)
  return 0
end

-- FCALL snugq_drain 1 <prefix> includeDelayed -> number of removed jobs
local function q_drain(keys, args)
  local p = keys[1]
  if not p then return bad('usage: snugq_drain 1 prefix includeDelayed') end
  local removed = 0
  local function sweep(ids)
    for _, id in ipairs(ids) do
      if remove_job(p, id, 0) == 1 then removed = removed + 1 end
    end
  end
  sweep(redis.call('LRANGE', p .. 'wait', 0, -1))
  sweep(redis.call('LRANGE', p .. 'paused', 0, -1))
  sweep(redis.call('ZRANGE', p .. 'prioritized', 0, -1))
  if args[1] == '1' then sweep(redis.call('ZRANGE', p .. 'delayed', 0, -1)) end
  return removed
end

-- FCALL snugq_clean 1 <prefix> type graceMs limit -> {removed ids}
-- Removes jobs older than graceMs (finished time for completed/failed, creation time otherwise).
local function q_clean(keys, args)
  local p, kind, grace, limit = keys[1], args[1], tonumber(args[2] or ''), tonumber(args[3] or '0')
  if not p or not kind or not grace or grace < 0 or not limit or limit < 0 or kind == 'active' or (not LISTS[kind] and not ZSETS[kind]) then
    return bad('usage: snugq_clean 1 prefix type graceMs limit; type is not active')
  end
  local cutoff = now_ms() - grace
  local ids
  if kind == 'completed' or kind == 'failed' then
    ids = redis.call('ZRANGEBYSCORE', p .. kind, '-inf', num(cutoff), 'LIMIT', 0, num(limit > 0 and limit or 1000000000))
  elseif LISTS[kind] then
    ids = redis.call('LRANGE', p .. kind, 0, -1)
  else
    ids = redis.call('ZRANGE', p .. kind, 0, -1)
  end
  local removed = {}
  for _, id in ipairs(ids) do
    if limit > 0 and #removed >= limit then break end
    local ts = tonumber(redis.call('HGET', p .. 'job:' .. id, 'ts') or '0')
    if kind == 'completed' or kind == 'failed' or ts <= cutoff then
      if remove_job(p, id, 0) == 1 then removed[#removed + 1] = id end
    end
  end
  return removed
end

-- FCALL snugq_obliterate 1 <prefix> force batch -> -1 when jobs are active (and force=0),
-- 1 when more remains (call again), 0 when the queue is gone.
local function q_obliterate(keys, args)
  local p, batch = keys[1], tonumber(args[2] or '1000')
  if not p or not batch or batch <= 0 then return bad('usage: snugq_obliterate 1 prefix force batch') end
  if args[1] ~= '1' and redis.call('LLEN', p .. 'active') > 0 then return -1 end
  local left = batch
  local function drop_list(name)
    while left > 0 do
      local id = redis.call('RPOP', p .. name)
      if not id then return end
      redis.call('DEL', p .. 'lock:' .. id)
      del_job(p, id)
      left = left - 1
    end
  end
  local function drop_zset(name)
    while left > 0 do
      local ids = redis.call('ZRANGE', p .. name, 0, math.min(left, 500) - 1)
      if #ids == 0 then return end
      for _, id in ipairs(ids) do
        redis.call('ZREM', p .. name, id)
        del_job(p, id)
        left = left - 1
      end
    end
  end
  drop_list('active'); drop_list('wait'); drop_list('paused')
  drop_zset('prioritized'); drop_zset('delayed'); drop_zset('completed'); drop_zset('failed'); drop_zset('waiting-children')
  if left <= 0 then return 1 end
  for _, sid in ipairs(redis.call('ZRANGE', p .. 'repeat', 0, -1)) do
    redis.call('DEL', p .. 'repeat:' .. sid)
  end
  redis.call('DEL', p .. 'repeat', p .. 'id', p .. 'pc', p .. 'dc', p .. 'meta', p .. 'limiter', p .. 'limited',
    p .. 'stalled-check', p .. 'events', p .. 'metrics:completed:total', p .. 'metrics:failed:total')
  local minute = math.floor(now_ms() / 60000)
  for m = minute - 1500, minute do
    for _, kind in ipairs({'completed', 'failed'}) do
      redis.call('DEL', p .. 'metrics:' .. kind .. ':' .. num(m))
    end
  end
  local reg = string.match(p, '^(.*):{') or 'snugq'
  local name = string.match(p, '{(.*)}:$')
  if name then redis.call('SREM', reg .. ':queues', name) end
  return 0
end

local function version()
  return VERSION
end

redis.register_function{function_name='snugq_add', callback=q_add, description='Queue: add a job'}
redis.register_function{function_name='snugq_upsert_scheduler', callback=q_upsert_scheduler, description='Queue: create or replace a repeatable job scheduler'}
redis.register_function{function_name='snugq_scheduler_next', callback=q_scheduler_next, description='Queue: create the next job of a scheduler'}
redis.register_function{function_name='snugq_remove_scheduler', callback=q_remove_scheduler, description='Queue: remove a scheduler'}
redis.register_function{function_name='snugq_fetch', callback=q_fetch, description='Queue: take the next job'}
redis.register_function{function_name='snugq_complete', callback=q_complete, description='Queue: complete an active job'}
redis.register_function{function_name='snugq_fail', callback=q_fail, description='Queue: fail an active job, retrying when attempts remain'}
redis.register_function{function_name='snugq_move_to_delayed', callback=q_move_to_delayed, description='Queue: delay an active job'}
redis.register_function{function_name='snugq_move_to_wait', callback=q_move_to_wait, description='Queue: return an active job to the queue'}
redis.register_function{function_name='snugq_extend_lock', callback=q_extend_lock, description='Queue: extend the lock of an active job'}
redis.register_function{function_name='snugq_stalled', callback=q_stalled, description='Queue: recover jobs whose lock expired'}
redis.register_function{function_name='snugq_progress', callback=q_progress, description='Queue: set job progress'}
redis.register_function{function_name='snugq_update_data', callback=q_update_data, description='Queue: replace job data'}
redis.register_function{function_name='snugq_log', callback=q_log, description='Queue: append to the job log'}
redis.register_function{function_name='snugq_pause', callback=q_pause, description='Queue: pause'}
redis.register_function{function_name='snugq_resume', callback=q_resume, description='Queue: resume'}
redis.register_function{function_name='snugq_counts', callback=q_counts, description='Queue: job counts by state', flags={'no-writes'}}
redis.register_function{function_name='snugq_jobs', callback=q_jobs, description='Queue: list jobs of a state', flags={'no-writes'}}
redis.register_function{function_name='snugq_remove', callback=q_remove, description='Queue: remove a job'}
redis.register_function{function_name='snugq_retry', callback=q_retry, description='Queue: retry a failed or completed job'}
redis.register_function{function_name='snugq_promote', callback=q_promote, description='Queue: run a delayed job now'}
redis.register_function{function_name='snugq_change_priority', callback=q_change_priority, description='Queue: change job priority'}
redis.register_function{function_name='snugq_change_delay', callback=q_change_delay, description='Queue: change the delay of a delayed job'}
redis.register_function{function_name='snugq_drain', callback=q_drain, description='Queue: remove waiting jobs'}
redis.register_function{function_name='snugq_clean', callback=q_clean, description='Queue: remove old jobs'}
redis.register_function{function_name='snugq_obliterate', callback=q_obliterate, description='Queue: delete the queue and all its jobs'}
redis.register_function{function_name='snugq_version', callback=version, description='Version of the queue library', flags={'no-writes'}}
