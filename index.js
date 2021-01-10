#!/usr/bin/env node

const redisModule = require('async-redis');
const TelegramBot = require('node-telegram-bot-api');
const escapeMD = require('markdown-escape');

// Config
const redisURL = process.env.REDIS_URL;
const redisPrefix = process.env.REDIS_PREFIX || 'XPBOT_';
const telegramToken = process.env.TELEGRAM_TOKEN;
const minXP = parseInt(process.env.MIN_XP) || 15;
const rateLimit = parseInt(process.env.RATE_LIMIT) || 15;
const lessBotSpam = process.env.LESS_BOT_SPAM == "true";
const botExpiration = (process.env.BOT_EXPIRATION || 3) * 1000;

if (!telegramToken) {
    console.error("Error: $TELEGRAM_TOKEN not set.");
    process.exit(1);
}

// APIs
const bot = new TelegramBot(telegramToken, {polling: true});
const redis = redisModule.createClient(redisURL);

// Triggers
bot.on('text',    incrementXP);
bot.on('voice',   incrementXP);
bot.on('sticker', incrementXP);
bot.on('photo',    moderateContent);
bot.on('video',    moderateContent);
bot.on('document', moderateContent);

// Commands
bot.onText(/\/start/,     displayHelp);
bot.onText(/\/xp(@\w+)?/, displayRank);
bot.onText(/\/ranks(@\w+)?/, displayTopRanks);

async function incrementXP(msg, match) {
    const uid = msg.from.id;
    const gid = msg.chat.id;
    const key = redisPrefix + gid;

    if (msg.chat.type == "private")
        return;

    if (msg.text && msg.text.match(/\/xp/))
        return;

    const entities = msg.entities || [];
    const isLink = entities.find(e => e.type == 'text_link');

    if (isLink)
        if (!(await moderateContent(msg, match)))
            return;

    if (rateLimit) {
        const ukey = redisPrefix + "_TGUSER_" + uid;

        if (await redis.exists(ukey))
            return;

        await redis.set(ukey, 1);
        await redis.expire(ukey, rateLimit);
    }

    await redis.zincrby(key, 1, uid);
}

async function displayRank(msg, match) {
    const uid = msg.from.id;
    const gid = msg.chat.id;
    const key = redisPrefix + gid;

    if (msg.chat.type == "private") {
        bot.sendMessageNoSpam(gid, "Sorry, you can't gain XP in private chats.", {}, msg);
        return;
    }

    const score = await redis.zscore(key, uid);
    if (!score) {
        bot.sendMention(gid, msg.from, ", you're not ranked yet 👶", msg);
        return;
    }

    const rank = (await redis.zrevrank(key, uid)) + 1;
    const total = await redis.zcard(key);

    let message;
    if (score >= minXP) {
        const next = await redis.zrangebyscore(key, parseInt(score) + 2, '+inf', 'withscores', 'limit', 0, 1);
        if (!next || next.length == 0) {
            message = `, you have ${score} XP  ◎  Rank ${rank} / ${total}  ◎  👑`;
        } else {
            let member = {};
            try {
                member = await bot.getChatMember(gid, next[0]);
            } catch (e) {}
            const rival = member.user || { id: '', first_name: 'an unknown user' };
            message = `, you have ${score} XP  ◎  Rank ${rank} / ${total}  ◎  ${next[1]-score} to beat ${withUser(rival)}`;
        }
    } else {
        message = `, your rank is ${rank} / ${total}.`;
    }
    bot.sendMention(gid, msg.from, message, msg);
}

async function displayTopRanks(msg, match) {
    const gid = msg.chat.id;
    const key = redisPrefix + gid;

    console.log("Displaying top ranks for " + gid);
    if (msg.chat.type == "private") {
        bot.sendMessageNoSpam(gid, "Please add me to a group.");
        return;
    }

    const total = await redis.zcard(key);
    if (total < 3)
        return;

    const scores = await redis.zrevrange(key, 0, 3, "withscores");
    let users = [];
    for (let i = 0; i < 3; i++) {
        const member = await bot.getChatMember(gid, scores[i*2]);
        if (member && member.user)
            users[i] = member.user;
        else
            users[i] = {id: 0, first_name: 'A ghost'};
    }

    bot.sendMessageNoSpam(gid,
        `🥇 ${withUser(users[0])}: ${scores[1]} XP \n` +
        `🥈 ${withUser(users[1])}: ${scores[3]} XP \n` +
        `🥉 ${withUser(users[2])}: ${scores[5]} XP`,
        { parse_mode: 'Markdown', disable_notification: true },
        msg);
}

async function moderateContent(msg, match) {
    const uid = msg.from.id;
    const gid = msg.chat.id;
    const key = redisPrefix + gid;

    if (msg.chat.type == "private")
        return;

    const score = await redis.zscore(key, uid);

    if (score < minXP) {
        bot.deleteMessage(msg.chat.id, msg.message_id);
        let chatName;
        if (msg.chat.title)
            chatName = ` to ${msg.chat.title}`;
        else
            chatName = '';
        bot.sendMessageNoSpam(msg.from.id, `Sorry, but you don't have enough XP to send that${escapeMD(chatName)}. Earn more XP by talking😉`);
        redis.zrem(key, uid);
        redis.incrby(`${redisPrefix}${msg.chat.id}_DELETED_COUNT`, 1);
        return false;
    }

    return true;
}

async function displayHelp(msg, match) {
    if (msg.chat.type != "private")
        return;
    bot.sendMessageNoSpam(msg.chat.id, "Hi, I'm XP Bot. Add me to a group and I will track users' message count (XP). " +
        "Available commands:\n" +
        " - /xp displays the XP count and rank of the user\n" +
        " - /ranks displays the top 3");
}

function withUser(user) {
    return escapeMD(user.first_name);
    //return `[${user.first_name}](tg://user?id=${user.id})`;
}

bot.sendMessageNoSpam = async (gid, text, options, queryMsg) => {
    const msg = await bot.sendMessage(gid, text, options);
    if (lessBotSpam)
        setTimeout(() => {
            if (queryMsg)
                bot.deleteMessage(gid, queryMsg.message_id);
            bot.deleteMessage(gid, msg.message_id);
        }, botExpiration);
}

bot.sendMention = (gid, user, text, queryMsg) => {
    const options = {
        parse_mode: 'Markdown',
        disable_notification: true
    }
    bot.sendMessageNoSpam(gid, withUser(user) + text, options, queryMsg);
}
