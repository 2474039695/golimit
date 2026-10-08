-- 单个 Redis key 对应一个令牌桶。整个脚本在 Redis 中原子执行，多个请求不会同时修改同一个桶。
-- KEYS[1]: 令牌桶的 Redis key
-- ARGV[1]: 每秒补充的令牌数，可为小数，例如 0.5 表示每 2 秒补充 1 个
-- ARGV[2]: 桶容量，即最多可保存多少个令牌
-- ARGV[3]: 当前 Unix 时间戳（毫秒），由 Go 调用方传入；这里没有调用 Redis TIME
-- ARGV[4]: 本次请求需要消耗的令牌数

local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

-- 拒绝非正速率、非整数容量/请求量、无效时间及超出 Lua 精确整数范围的值。
-- capacity / rate 是桶从空到满所需的秒数；限制它可避免极低速率
-- 产生超过约 2^31 秒的过期时间。
if not rate or rate <= 0 or rate == math.huge or rate ~= rate or
   not capacity or capacity < 1 or capacity % 1 ~= 0 or capacity > 9007199254740991 or
   not now or now < 0 or now % 1 ~= 0 or now > 9007199254740991 or
   not requested or requested < 1 or requested % 1 ~= 0 or requested > 9007199254740991 or
   capacity / rate > 2147483646 then
    return redis.error_reply('invalid limiter arguments')
end

-- tokens: 当前完整令牌数。
-- last_time: 上次计算补充令牌时的时间戳（毫秒）。
-- remaining_ms: 尚未凑成一个完整令牌的时间余量，可以有小数部分。
-- 例如每秒 10 个令牌，经过 50 毫秒会留下 50 毫秒余量，下次继续累计。
local data = redis.call('HMGET', key, 'tokens', 'last_time', 'remaining_ms')
local tokens = tonumber(data[1])
local last_time = tonumber(data[2])
local remaining_ms = tonumber(data[3]) or 0

-- 新桶从满额开始。旧版本的桶没有 remaining_ms 字段，上面的 or 0 可兼容读取。
if not tokens or not last_time then
    tokens = capacity
    last_time = now
    remaining_ms = 0
end

-- 时钟回退时不计算负的经过时间，避免错误地扣除已经补充的令牌。
local elapsed_ms = math.max(0, now - last_time)
if tokens < capacity then
    -- 合并本次经过的时间和上次未用完的时间，再换算成完整令牌。
    -- 拒绝的请求也会走到这里，因此持续请求不会反复丢掉不足一个令牌的时间。
    local accumulated_ms = elapsed_ms + remaining_ms
    -- 浮点计算可能把刚好 1 个令牌算成 0.999999...；极小容差避免边界处少补 1 个。
    local fill_tokens = math.floor(accumulated_ms * rate / 1000 + 1e-9)
    if fill_tokens >= capacity - tokens then
        -- 一旦补满，就丢弃超出容量的时间；否则满桶期间的空闲时间会变成额外额度。
        tokens = capacity
        remaining_ms = 0
    else
        tokens = tokens + fill_tokens
        -- 只扣除已经转换为完整令牌的时间，剩余部分留给下一次请求。
        remaining_ms = math.max(0, accumulated_ms - fill_tokens * 1000 / rate)
    end
else
    -- 原本就是满桶，同样不能积攒时间余量。
    remaining_ms = 0
end

-- 上面的 elapsed_ms 已处理回退；这里也不能把保存的时间倒拨。
last_time = math.max(now, last_time)

-- 补充完再判断本次请求。额度不足时不扣令牌，但仍保存补充后的桶状态。
local allowed
if tokens >= requested then
    tokens = tokens - requested
    allowed = 1
else
    allowed = 0
end

-- 小数余量用足够的有效数字写回，避免每次读取时额外损失精度。
redis.call('HSET', key, 'tokens', tokens, 'last_time', last_time,
    'remaining_ms', string.format('%.17g', remaining_ms))
-- 至少保留 1 小时；低速率桶还需活到从空桶补满，否则过早过期会重建满桶。
-- 每次调用都刷新 TTL，闲置足够久的桶最终会自动清理。
local ttl_seconds = math.max(3600, math.ceil(capacity / rate) + 1)
redis.call('EXPIRE', key, ttl_seconds)
-- 1 表示放行，0 表示拒绝；输入无效时在前面直接返回 Redis 错误。
return allowed
