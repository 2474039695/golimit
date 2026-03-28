-- KEYS[1]: 限流键名
-- ARGV[1]: 速率 (rate)
-- ARGV[2]: 容量 (capacity)
-- ARGV[3]: 当前时间戳 (毫秒)
-- ARGV[4]: 请求数量

local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'last_time')
local tokens = tonumber(data[1])
local last_time = tonumber(data[2])

if not tokens then
    tokens = capacity
    last_time = now
end

local delta = math.max(0, now - last_time)
local fill_tokens = math.floor(delta * rate / 1000)
tokens = math.min(capacity, tokens + fill_tokens)

if tokens >= requested then
    tokens = tokens - requested
    redis.call('HMSET', key, 'tokens', tokens, 'last_time', now)
    redis.call('EXPIRE', key, 3600)
    return 1
else
    redis.call('HMSET', key, 'tokens', tokens, 'last_time', now)
    redis.call('EXPIRE', key, 3600)
    return 0
end