-- 单个 Redis key 对应一个令牌桶。整个脚本在 Redis 中原子执行，多个请求不会同时修改同一个桶。
-- KEYS[1]: 令牌桶的 Redis key
-- ARGV[1]: 每秒补充的令牌数，可为小数，例如 0.5 表示每 2 秒补充 1 个
-- ARGV[2]: 桶容量，即最多可保存多少个令牌
-- ARGV[3]: 当前 Unix 时间戳（毫秒），由 Go 调用方传入；这里没有调用 Redis TIME
-- ARGV[4]: 本次请求需要消耗的令牌数
-- ARGV[5]: 校验后的桶 TTL（秒），至少覆盖从空桶补满再加 1 秒
-- ARGV[6]: 配置允许的最大 TTL（秒），越界必须报错，不能截断补满周期

local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])
local ttl_seconds = tonumber(ARGV[5])
local max_ttl_seconds = tonumber(ARGV[6])

-- 拒绝非正速率、非整数容量/请求量、无效时间及超出 Lua 精确整数范围的值。
-- rate ~= rate 检测 NaN；2^53-1 是双精度浮点数能够精确表达的最大连续整数。
-- Go 端先校验，Lua 再做防御校验，所有检查均在写入状态之前完成。
if not rate or rate <= 0 or rate == math.huge or rate ~= rate or
   not capacity or capacity < 1 or capacity % 1 ~= 0 or capacity > 9007199254740991 or
   not now or now < 0 or now % 1 ~= 0 or now > 9007199254740991 or
   not requested or requested < 1 or requested % 1 ~= 0 or requested > capacity or
   not ttl_seconds or ttl_seconds < 1 or ttl_seconds % 1 ~= 0 or
   not max_ttl_seconds or max_ttl_seconds < 1 or max_ttl_seconds % 1 ~= 0 or max_ttl_seconds > 9223372036 or
   ttl_seconds > max_ttl_seconds or ttl_seconds < math.ceil(capacity / rate) + 1 then
    return redis.error_reply('invalid limiter arguments')
end

-- tokens: 当前完整令牌数。
-- last_time: 上次计算补充令牌时的时间戳（毫秒）。
-- remaining_ms: 尚未凑成一个完整令牌的时间余量，可以有小数部分。
-- 例如每秒 10 个令牌，经过 50 毫秒会留下 50 毫秒余量，下次继续累计。
local data = redis.call('HMGET', key, 'tokens', 'last_time', 'remaining_ms', 'rate', 'capacity')
local tokens = tonumber(data[1])
local last_time = tonumber(data[2])
local remaining_ms = tonumber(data[3]) or 0

-- 多实例必须对同一桶采用相同速率和容量。旧桶没有配置字段，首次访问时补齐；
-- 新桶若被另一套配置访问，拒绝本次操作，不能让两个实例交替改变同一桶的语义。
if (data[4] and tonumber(data[4]) ~= rate) or (data[5] and tonumber(data[5]) ~= capacity) then
    return redis.error_reply('LIMITER_CONFIG_MISMATCH')
end

-- 新桶从满额开始。旧版本的桶没有 remaining_ms 字段，上面的 or 0 可兼容读取。
if not data[1] and not data[2] then
    tokens = capacity
    last_time = now
    remaining_ms = 0
elseif not tokens or not last_time or tokens < 0 or tokens > capacity or
       tokens % 1 ~= 0 or last_time < 0 or last_time % 1 ~= 0 or last_time > 9007199254740991 or
       (data[3] and not tonumber(data[3])) or remaining_ms < 0 or remaining_ms == math.huge or remaining_ms ~= remaining_ms or
       remaining_ms * rate / 1000 >= 1 + 1e-9 then
    -- 损坏的状态不能当作新桶补满，否则可能在数据异常时绕过限流。
    return redis.error_reply('invalid limiter state')
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
    'remaining_ms', string.format('%.17g', remaining_ms),
    'rate', string.format('%.17g', rate), 'capacity', capacity)
-- 使用经过双重校验的 TTL，放行和拒绝都会刷新空闲保留时间。
redis.call('EXPIRE', key, ttl_seconds)

-- 返回等待时间时也要减去时间余量，不能只用“缺少令牌 / rate”。
-- 时钟回退时还需等待时钟追上 last_time；两个等待量都向上取整到毫秒。
local rollback_ms = math.max(0, last_time - now)
local retry_ms = 0
if allowed == 0 then
    retry_ms = math.ceil(math.max(0, (requested - tokens) * 1000 / rate - remaining_ms) + rollback_ms)
end
local reset_ms = 0
if tokens < capacity then
    reset_ms = math.ceil(math.max(0, (capacity - tokens) * 1000 / rate - remaining_ms) + rollback_ms)
end
-- 数组顺序：是否放行、剩余完整令牌、重试等待毫秒、补满等待毫秒。
return {allowed, tokens, retry_ms, reset_ms}
