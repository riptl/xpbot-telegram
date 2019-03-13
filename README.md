# Telegram XP bot

### Blocks pictures + videos from low XP users

![Statistics](.github/block.gif)

### Keeps track of group statistics

![Statistics](.github/stats.png)

Configuration:
 - `$REDIS_URL` Redis host:port (optional)
 - `$REDIS_PREFIX` Redis ZSET key prefix (one key per group)
 - `$TELEGRAM_TOKEN` BotFather token
 - `$MIN_XP` Minimum XP before pics/vids allowed
 - `$RATE_LIMIT` User cooldown after earning XP (seconds)

Commands:
 - `/xp` Get current XP
 - `/ranks` Get top XP users
