package server

import (
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/mattermost/focalboard/server/api"
	"github.com/mattermost/focalboard/server/app"
	"github.com/mattermost/focalboard/server/auth"
	"github.com/mattermost/focalboard/server/context"
	appModel "github.com/mattermost/focalboard/server/model"
	"github.com/mattermost/focalboard/server/services/config"
	"github.com/mattermost/focalboard/server/services/scheduler"
	"github.com/mattermost/focalboard/server/services/store"
	"github.com/mattermost/focalboard/server/services/store/sqlstore"
	"github.com/mattermost/focalboard/server/services/telemetry"
	"github.com/mattermost/focalboard/server/services/webhook"
	"github.com/mattermost/focalboard/server/web"
	"github.com/mattermost/focalboard/server/ws"
	"github.com/mattermost/mattermost-server/v5/services/filesstore"
	"github.com/mattermost/mattermost-server/v5/utils"
)

type Server struct {
	config              *config.Configuration
	wsServer            *ws.Server
	webServer           *web.Server
	store               store.Store
	filesBackend        filesstore.FileBackend
	telemetry           *telemetry.Service
	logger              *zap.Logger
	cleanUpSessionsTask *scheduler.ScheduledTask

	localRouter     *mux.Router
	localModeServer *http.Server
	api             *api.API
	appBuilder      func() *app.App
}

//web服务
func New(cfg *config.Configuration, singleUserToken string) (*Server, error) {
	logger, err := zap.NewProduction() //初始化日志引擎
	if err != nil {
		return nil, err
	}

	store, err := sqlstore.New(cfg.DBType, cfg.DBConfigString, cfg.DBTablePrefix) //初始化的数据库
	if err != nil {
		log.Print("Unable to start the database", err)
		return nil, err
	}

	auth := auth.New(cfg, store) //验证服务？

	wsServer := ws.NewServer(auth, singleUserToken) //websocket

	filesBackendSettings := filesstore.FileBackendSettings{} //本地的文件存储
	filesBackendSettings.DriverName = "local"
	filesBackendSettings.Directory = cfg.FilesPath
	filesBackend, appErr := filesstore.NewFileBackend(filesBackendSettings)
	if appErr != nil {
		log.Print("Unable to initialize the files storage")

		return nil, errors.New("unable to initialize the files storage")
	}

	webhookClient := webhook.NewClient(cfg)

	appBuilder := func() *app.App { return app.New(cfg, store, auth, wsServer, filesBackend, webhookClient) }
	api := api.NewAPI(appBuilder, singleUserToken, cfg.AuthMode)

	// Local router for admin APIs
	localRouter := mux.NewRouter()
	api.RegisterAdminRoutes(localRouter)

	// Init workspace
	appBuilder().GetRootWorkspace()

	webServer := web.NewServer(cfg.WebPath, cfg.ServerRoot, cfg.Port, cfg.UseSSL, cfg.LocalOnly)
	webServer.AddRoutes(wsServer) //添加websocket路径
	webServer.AddRoutes(api)      //添加http路径

	// Init telemetry
	settings, err := store.GetSystemSettings() //系统设置参数
	if err != nil {
		return nil, err
	}

	telemetryID := settings["TelemetryID"] //
	if len(telemetryID) == 0 {
		telemetryID = uuid.New().String()
		err := store.SetSystemSetting("TelemetryID", uuid.New().String())
		if err != nil {
			return nil, err
		}
	}

	registeredUserCount, err := appBuilder().GetRegisteredUserCount() //注册的用户数
	if err != nil {
		return nil, err
	}

	dailyActiveUsers, err := appBuilder().GetDailyActiveUsers() //日活用户数
	if err != nil {
		return nil, err
	}

	weeklyActiveUsers, err := appBuilder().GetWeeklyActiveUsers() //周活
	if err != nil {
		return nil, err
	}

	monthlyActiveUsers, err := appBuilder().GetMonthlyActiveUsers() //月活
	if err != nil {
		return nil, err
	}

	telemetryService := telemetry.New(telemetryID, zap.NewStdLog(logger))
	telemetryService.RegisterTracker("server", func() map[string]interface{} { //注册服务信息的函数
		return map[string]interface{}{
			"version":          appModel.CurrentVersion,
			"build_number":     appModel.BuildNumber,
			"build_hash":       appModel.BuildHash,
			"edition":          appModel.Edition,
			"operating_system": runtime.GOOS,
		}
	})
	telemetryService.RegisterTracker("config", func() map[string]interface{} { //相关配置的函数
		return map[string]interface{}{
			"serverRoot":  cfg.ServerRoot == config.DefaultServerRoot,
			"port":        cfg.Port == config.DefaultPort,
			"useSSL":      cfg.UseSSL,
			"dbType":      cfg.DBType,
			"single_user": len(singleUserToken) > 0,
		}
	})
	telemetryService.RegisterTracker("activity", func() map[string]interface{} { //活动人员统计
		return map[string]interface{}{
			"registered_users":     registeredUserCount,
			"daily_active_users":   dailyActiveUsers,
			"weekly_active_users":  weeklyActiveUsers,
			"monthly_active_users": monthlyActiveUsers,
		}
	})

	server := Server{ //服务集成
		config:       cfg,              //配置
		wsServer:     wsServer,         //websocket
		webServer:    webServer,        //http服务
		store:        store,            //数据库
		filesBackend: filesBackend,     //资源文件
		telemetry:    telemetryService, //回调,插件？
		logger:       logger,           //日志
		localRouter:  localRouter,      //本地管理的API
		api:          api,              //对外API
		appBuilder:   appBuilder,       //
	}

	server.initHandlers()

	return &server, nil
}

func (s *Server) Start() error {
	s.logger.Info("Server.Start")

	s.webServer.Start() //启动http服务

	if s.config.EnableLocalMode { //本地服务
		if err := s.startLocalModeServer(); err != nil {
			return err
		}
	}

	s.cleanUpSessionsTask = scheduler.CreateRecurringTask("cleanUpSessions", func() { //清楚session缓存任务
		secondsAgo := int64(60 * 60 * 24 * 31)
		if secondsAgo < s.config.SessionExpireTime {
			secondsAgo = s.config.SessionExpireTime
		}
		if err := s.store.CleanUpSessions(secondsAgo); err != nil {
			s.logger.Error("Unable to clean up the sessions", zap.Error(err))
		}
	}, 10*time.Minute)

	if s.config.Telemetry { //
		firstRun := utils.MillisFromTime(time.Now())
		s.telemetry.RunTelemetryJob(firstRun)
	}

	return nil
}

func (s *Server) Shutdown() error { //关闭服务
	if err := s.webServer.Shutdown(); err != nil {
		return err
	}

	s.stopLocalModeServer() //禁止本地服务

	if s.cleanUpSessionsTask != nil {
		s.cleanUpSessionsTask.Cancel()
	}

	s.telemetry.Shutdown()

	defer s.logger.Info("Server.Shutdown")

	return s.store.Shutdown()
}

func (s *Server) Config() *config.Configuration {
	return s.config
}

// Local server

func (s *Server) startLocalModeServer() error {
	s.localModeServer = &http.Server{
		Handler:     s.localRouter,
		ConnContext: context.SetContextConn,
	}

	// TODO: Close and delete socket file on shutdown
	syscall.Unlink(s.config.LocalModeSocketLocation)

	socket := s.config.LocalModeSocketLocation
	unixListener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}

	go func() {
		log.Println("Starting unix socket server")
		err = s.localModeServer.Serve(unixListener)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Error starting unix socket server: %v", err)
		}
	}()

	return nil
}

func (s *Server) stopLocalModeServer() {
	if s.localModeServer != nil {
		s.localModeServer.Close()
		s.localModeServer = nil
	}
}

func (s *Server) GetRootRouter() *mux.Router {
	return s.webServer.Router()
}
