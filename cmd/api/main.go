package main

import (
	"fmt"
	"net/http"
	"net/http/pprof"

	"github.com/TicketsBot-cloud/archiverclient"
	app "github.com/TicketsBot-cloud/import-api/app/http"
	"github.com/TicketsBot-cloud/import-api/config"
	"github.com/TicketsBot-cloud/import-api/database"
	"github.com/TicketsBot-cloud/import-api/log"
	"github.com/TicketsBot-cloud/import-api/redis"
	"github.com/TicketsBot-cloud/import-api/rpc"
	"github.com/TicketsBot-cloud/import-api/rpc/cache"
	"github.com/TicketsBot-cloud/import-api/s3"
	"github.com/TicketsBot-cloud/import-api/utils"
	"github.com/TicketsBot/common/model"
	"github.com/TicketsBot/common/observability"
	"github.com/TicketsBot/common/premium"
	"github.com/TicketsBot/common/secureproxy"
	"github.com/TicketsBot/worker/i18n"
	"github.com/getsentry/sentry-go"
	"github.com/rxdn/gdl/rest/request"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	_ "github.com/joho/godotenv/autoload"
)

var Logger *zap.Logger

func main() {
	startPprof()

	cfg, err := config.LoadConfig()
	utils.Must(err)
	config.Conf = cfg

	if config.Conf.SentryDsn != nil {
		sentryOpts := sentry.ClientOptions{
			Dsn:              *config.Conf.SentryDsn,
			Debug:            config.Conf.Debug,
			AttachStacktrace: true,
			EnableTracing:    true,
			TracesSampleRate: 0.1,
		}

		if err := sentry.Init(sentryOpts); err != nil {
			fmt.Printf("Failed to initialise sentry: %s", err.Error())
		}
	}

	var logger *zap.Logger
	if config.Conf.JsonLogs {
		loggerConfig := zap.NewProductionConfig()
		loggerConfig.Level.SetLevel(config.Conf.LogLevel)

		logger, err = loggerConfig.Build(
			zap.AddCaller(),
			zap.AddStacktrace(zap.ErrorLevel),
			zap.WrapCore(observability.ZapSentryAdapter(observability.EnvironmentProduction)),
		)
	} else {
		loggerConfig := zap.NewDevelopmentConfig()
		loggerConfig.Level.SetLevel(config.Conf.LogLevel)
		loggerConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder

		logger, err = loggerConfig.Build(zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	}

	if err != nil {
		panic(fmt.Errorf("failed to initialise zap logger: %w", err))
	}

	log.Logger = logger

	logger.Info("Connecting to database")
	database.ConnectToDatabase()

	logger.Info("Connecting to cache")
	cache.Instance = cache.NewCache()

	logger.Info("Connecting to import S3")
	s3.ConnectS3(config.Conf.S3Import.Endpoint, config.Conf.S3Import.AccessKey, config.Conf.S3Import.SecretKey)

	logger.Info("Initialising microservice clients")
	utils.ArchiverClient = archiverclient.NewArchiverClient(archiverclient.NewProxyRetriever(config.Conf.Bot.ObjectStore), []byte(config.Conf.Bot.AesKey))
	utils.SecureProxyClient = secureproxy.NewSecureProxy(config.Conf.SecureProxyUrl)

	utils.LoadEmoji()

	i18n.Init()

	if config.Conf.Bot.ProxyUrl != "" {
		request.RegisterHook(utils.ProxyHook)
	}

	logger.Info("Connecting to Redis")
	redis.Client = redis.NewRedisClient()

	if !config.Conf.Debug {
		rpc.PremiumClient = premium.NewPremiumLookupClient(
			redis.Client.Client,
			cache.Instance.PgCache,
			database.Client,
		)
	} else {
		c := premium.NewMockLookupClient(premium.Whitelabel, model.EntitlementSourcePatreon)
		rpc.PremiumClient = &c
	}

	logger.Info("Starting server")
	app.StartServer(logger)
}

func startPprof() {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/{action}", pprof.Index)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)

	go func() {
		http.ListenAndServe(":6060", mux)
	}()
}
