package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/go-redis/redis/v8"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	// Read flags.
	var confPath string
	var logEncoding string
	logLevel := zap.InfoLevel
	flag.StringVar(&confPath, "conf", "", "Path to config file")
	flag.StringVar(&logEncoding, "log-encoding", "console", "Log encoding (json, console)")
	flag.Var(&logLevel, "log-level", "Log level (default info)")
	flag.Parse()
	// Install Ctrl+C handler.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		<-c
		os.Exit(0)
	}()
	// Create logger.
	log := newLogger(logEncoding, logLevel)
	// Read config.
	viper.SetEnvPrefix("xpbot")
	_ = viper.BindEnv("redis_url")
	_ = viper.BindEnv("bot_token")
	if confPath == "" {
		flag.Usage()
		os.Exit(1)
	}
	viper.SetConfigFile(confPath)
	if err := viper.ReadInConfig(); err != nil {
		log.Fatal("Invalid config", zap.Error(err))
	}
	var conf config
	if err := viper.Unmarshal(&conf); err != nil {
		log.Fatal("Config does not map to struct", zap.Error(err))
	}
	if conf.RedisURL == "" {
		log.Fatal("Missing redis_url")
	}
	if conf.BotToken == "" {
		log.Fatal("Missing bot_token")
	}
	if len(conf.Levels) == 0 {
		log.Fatal("No XP levels defined")
	}
	// Connect to Redis.
	dbConf, err := redis.ParseURL(conf.RedisURL)
	if err != nil {
		log.Fatal("Invalid Redis URL", zap.Error(err))
	}
	db := redis.NewClient(dbConf)
	// Ping Redis to make sure it's up.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := db.Ping(ctx).Err(); err != nil {
			log.Fatal("Failed to ping Redis", zap.Error(err))
		}
	}()
	// Initialize Telegram bot.
	bot, err := tgbotapi.NewBotAPI(conf.BotToken)
	if err != nil {
		log.Fatal("Failed to init bot API", zap.Error(err))
	}
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 30 // long poll (seconds)
	updates, err := bot.GetUpdatesChan(updateConfig)
	if err != nil {
		log.Fatal("Failed to stream updates", zap.Error(err))
	}
	// Build handler.
	h := handler{
		log:    log,
		bot:    bot,
		db:     db,
		config: &conf,
	}
	// Stream messages.
	log.Info("Streaming messages")
	for update := range updates {
		h.handleUpdate(&update)
	}
	log.Info("No more updates")
}

type config struct {
	BotToken   string   `mapstructure:"bot_token"`
	RedisURL   string   `mapstructure:"redis_url"`
	Levels     []*level `mapstructure:"levels"`
	Admins     []string `mapstructure:"admins"`
	NotAnAdmin string   `mapstructure:"not_an_admin"`
}

type level struct {
	Level  int
	XP     int64
	Title  string
	Format string
}

func (l *level) formatTitle(username string, xp int64, rankPos, rankTotal int64) string {
	return fmt.Sprintf(l.Format, username, xp, l.Level, rankPos, rankTotal)
}

type handler struct {
	log    *zap.Logger
	bot    *tgbotapi.BotAPI
	db     *redis.Client
	config *config
}

func (h *handler) handleUpdate(update *tgbotapi.Update) {
	if update.Message != nil {
		h.handleMsg(update.Message)
	}
}

func isCommand(str, prefix string) bool {
	return str == prefix || (strings.HasPrefix(str, prefix) && (str[len(prefix)] == ' ' || str[len(prefix)] == '@'))
}

func (h *handler) handleMsg(msg *tgbotapi.Message) {
	if isCommand(msg.Text, "/xp") {
		parts := strings.SplitN(msg.Text, " ", 2)
		if len(parts) == 1 {
			go h.printXP(msg.From.UserName, msg.Chat.ID, msg.MessageID)
		} else {
			username := strings.TrimPrefix(parts[1], "@")
			go h.printXP(username, msg.Chat.ID, msg.MessageID)
		}
		return
	}
	if isCommand(msg.Text, "/givexp") {
		go h.giveXP(msg.Text, msg.Chat.ID, msg.From.UserName, msg.MessageID)
	}
	if isCommand(msg.Text, "/ranks") {
		go h.ranks(msg.Chat.ID)
	}
	go h.incrementXP(msg.From, msg.Chat.ID)
}

func (h *handler) incrementXP(from *tgbotapi.User, chatID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var increment int64 = 1
	newScore, err := h.db.ZIncrBy(ctx, getXPKey(chatID), float64(increment), from.UserName).Result()
	if err != nil {
		h.log.Error("Failed to increment XP", zap.Error(err))
		return
	}
	oldScore := int64(newScore) - increment
	oldLevel, newLevel := h.getLevel(oldScore), h.getLevel(int64(newScore))
	if oldLevel != newLevel {
		h.printXP(from.UserName, chatID, 0)
	}
}

func (h *handler) printXP(username string, chatID int64, msgID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	xp, rank, total, err := h.getScore(ctx, username, chatID)
	if errors.Is(err, redis.Nil) {
		return
	} else if err != nil {
		h.log.Error("Failed to retrieve XP", zap.Error(err))
		return
	}
	level := h.getLevel(xp)
	msg := level.formatTitle(username, xp, rank, total)
	_, err = h.bot.Send(tgbotapi.MessageConfig{
		BaseChat: tgbotapi.BaseChat{
			ChatID:           chatID,
			ReplyToMessageID: msgID,
		},
		Text:                  msg,
		ParseMode:             tgbotapi.ModeHTML,
		DisableWebPagePreview: true,
	})
	if err != nil {
		h.log.Error("Failed to reply with XP", zap.Error(err))
		return
	}
}

func (h *handler) giveXP(invoc string, chatID int64, fromUsername string, msgID int) {
	fromAdmin := false
	for _, u := range h.config.Admins {
		if u == fromUsername {
			fromAdmin = true
			break
		}
	}
	if !fromAdmin {
		_, _ = h.bot.Send(tgbotapi.MessageConfig{
			BaseChat: tgbotapi.BaseChat{
				ChatID:           chatID,
				ReplyToMessageID: msgID,
			},
			Text:                  h.config.NotAnAdmin,
			ParseMode:             tgbotapi.ModeMarkdown,
			DisableWebPagePreview: true,
		})
		return
	}
	parts := strings.SplitN(invoc, " ", 3)
	if len(parts) != 3 {
		_, _ = h.bot.Send(tgbotapi.MessageConfig{
			BaseChat: tgbotapi.BaseChat{
				ChatID:           chatID,
				ReplyToMessageID: msgID,
			},
			Text:                  "Usage: `/givexp @terorie 123`",
			ParseMode:             tgbotapi.ModeMarkdown,
			DisableWebPagePreview: true,
		})
		return
	}
	username := strings.TrimPrefix(parts[1], "@")
	num, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = h.db.ZIncrBy(ctx, getXPKey(chatID), float64(num), username).Err()
	if err != nil {
		h.log.Error("Failed to give XP", zap.Error(err))
		return
	}
	h.printXP(username, chatID, msgID)
}

func (h *handler) ranks(chatID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	zs, err := h.db.ZRevRangeWithScores(ctx, getXPKey(chatID), 0, 10).Result()
	if err != nil {
		h.log.Error("Failed to display ranks", zap.Error(err))
		return
	}
	if len(zs) == 0 {
		return
	}
	var builder strings.Builder
	for i, z := range zs {
		if i != 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(
			fmt.Sprintf("%s <b>%s</b> –⁠ %s (%d XP)",
				formatRank(i+1),
				z.Member.(string),
				h.getLevel(int64(z.Score)).Title,
				int64(z.Score),
			),
		)
	}
	_, err = h.bot.Send(tgbotapi.MessageConfig{
		BaseChat: tgbotapi.BaseChat{
			ChatID: chatID,
		},
		Text:                  builder.String(),
		ParseMode:             tgbotapi.ModeHTML,
		DisableWebPagePreview: true,
	})
	if err != nil {
		h.log.Error("Failed to display ranks", zap.Error(err))
		return
	}
}

func formatRank(i int) string {
	switch i {
	case 1:
		return "🥇"
	case 2:
		return "🥈"
	case 3:
		return "🥉"
	default:
		return "#" + strconv.Itoa(i)
	}
}

func (h *handler) getScore(ctx context.Context, userName string, chatID int64) (xp, rank, total int64, err error) {
	pipe := h.db.Pipeline()
	key := getXPKey(chatID)
	scoreCmd := pipe.ZScore(ctx, key, userName)
	rankCmd := pipe.ZRevRank(ctx, key, userName)
	totalCmd := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, 0, 0, err
	}
	return int64(scoreCmd.Val()), rankCmd.Val() + 1, totalCmd.Val(), nil
}

func (h *handler) getLevel(xp int64) *level {
	for _, l := range h.config.Levels {
		if xp < l.XP {
			return l
		}
	}
	return h.config.Levels[len(h.config.Levels)-1]
}

func getXPKey(chatID int64) string {
	return "XPBOT_" + strconv.FormatInt(chatID, 10)
}

func newLogger(encoding string, level zapcore.Level) *zap.Logger {
	conf := zap.Config{
		Level:             zap.NewAtomicLevelAt(level),
		Encoding:          encoding,
		EncoderConfig:     zap.NewDevelopmentEncoderConfig(),
		OutputPaths:       []string{"stderr"},
		ErrorOutputPaths:  []string{"stderr"},
		DisableCaller:     true,
		DisableStacktrace: true,
	}
	log, err := conf.Build()
	if err != nil {
		panic(err.Error())
	}
	return log
}
