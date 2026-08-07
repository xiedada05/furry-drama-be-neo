// neo-server 服务入口。
//
// 用法：
//
//	bin/server --config=/etc/furry-drama-be-neo.ini --listen=tcp:0.0.0.0:5000
//	bin/server --listen=unix:/run/furry-drama-be-neo.sock   # Unix socket（"@name" 为 abstract）
//
// 启动顺序：加载配置 → 连接 MongoDB → 装配仓储/签名器 → 装配 Gin → 启动 HTTP →
// 监听 SIGTERM/SIGINT 优雅关停（10s 兜底强退）。M2 起在 listen 前执行启动迁移与 cron。
//
// @title 兽剧聚合平台 API
// @version 1.0.0
// @description 兽剧内容聚合平台后端服务 API 文档（neo-server，行为对齐 Express 版）
// @host localhost:5000
// @BasePath /api
// @securityDefinitions.apikey bearerAuth
// @in header
// @name Authorization
// @description 输入 JWT access token（不含 Bearer 前缀；亦可通过 accessToken cookie）
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
)

func main() {
	var configPath, listen string
	flag.StringVar(&configPath, "config", "", "配置文件路径（默认 "+config.DefaultPath+"）")
	flag.StringVar(&listen, "listen", "", "监听地址：tcp:HOST:PORT 或 unix:/path/to.sock（覆盖 ini）")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("[FATAL] 配置加载失败: %v", err)
	}
	if listen != "" {
		cfg.Server.Listen = listen
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := repository.Connect(ctx, cfg.Database.URI, cfg.Database.Name, cfg.Database.Pool)
	if err != nil {
		log.Fatalf("[FATAL] MongoDB 连接失败: %v", err)
	}
	log.Printf("MongoDB 连接成功: %s", cfg.Database.URI)

	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})

	ln, err := netListen(cfg.Server.Listen)
	if err != nil {
		log.Fatalf("[FATAL] 监听失败: %v", err)
	}
	defer ln.Close()
	log.Printf("server listening on %s", cfg.Server.Listen)

	srv := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 65 * time.Second, // 对齐 Express headersTimeout
		ReadTimeout:       30 * time.Second, // 对齐 requestTimeout
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       5 * time.Second, // 对齐 keepAliveTimeout
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	// 优雅关停：SIGTERM/SIGINT，10s 兜底强退（对齐 src/index.js:479-503）。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	select {
	case sig := <-stop:
		log.Printf("收到信号 %s，开始优雅关停…", sig)
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
		return
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("优雅关停超时: %v", err)
	}
	log.Printf("server stopped")
}

// netListen 根据配置创建监听器。
//
//	"tcp:HOST:PORT"        → TCP（默认）
//	"unix:/path/to.sock"   → Unix domain socket（清理残留 + chmod 0660）
//	"unix:@abstract"       → Linux abstract socket
func netListen(spec string) (net.Listener, error) {
	if strings.HasPrefix(spec, "unix:") {
		p := strings.TrimPrefix(spec, "unix:")
		if strings.HasPrefix(p, "@") {
			// abstract socket：无需清理、无文件权限。
			return net.Listen("unix", p)
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		ln, err := net.Listen("unix", p)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(p, 0o660); err != nil {
			ln.Close()
			return nil, err
		}
		return ln, nil
	}
	// 默认 TCP："tcp:..." 或裸 "HOST:PORT"。
	addr := strings.TrimPrefix(spec, "tcp:")
	return net.Listen("tcp", addr)
}
