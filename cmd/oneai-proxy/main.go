package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/instance"
	"github.com/4xian/oneai-proxy/internal/secret"
	"github.com/4xian/oneai-proxy/internal/server"
	"github.com/4xian/oneai-proxy/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("用法: oneai-proxy serve [--proxy-listen 地址] [--admin-listen 地址] [--data-dir 目录]")
	}

	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	proxyListen := flags.String("proxy-listen", "127.0.0.1:9988", "代理监听地址")
	adminListen := flags.String("admin-listen", "127.0.0.1:9989", "管理监听地址")
	dataDir := flags.String("data-dir", "", "数据目录")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	settings, err := config.New(*proxyListen, *adminListen, *dataDir)
	if err != nil {
		return err
	}
	instanceLock, err := instance.Acquire(settings.DataDirectory)
	if err != nil {
		return err
	}
	defer instanceLock.Close()
	database, err := storage.Open(settings.DataDirectory)
	if err != nil {
		return err
	}
	defer database.Close()
	settings, err = storage.LoadRuntimeSettings(database, settings)
	if err != nil {
		return err
	}
	settings.AllowLocalDefaultTokens = true
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	secrets := secret.NewResilientStore(settings.DataDirectory, logger)
	authTokens, err := storage.LoadOrCreateAuthTokens(database, secrets)
	if err != nil {
		return err
	}
	service := server.New(settings, database, logger, authTokens, secrets)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("oneai-proxy 已启动", "proxy", settings.ProxyListen, "admin", settings.AdminListen, "data", settings.DataDirectory)
	return service.Run(ctx)
}
