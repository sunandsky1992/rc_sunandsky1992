package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"rc_sunandsky1992/internal/api"
	"rc_sunandsky1992/internal/dispatcher"
	"rc_sunandsky1992/internal/store"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://postgres:postgres@localhost:5432/notifications?sslmode=disable"
	}

	// 连接数据库
	ctx := context.Background()
	s, err := store.NewPGStore(ctx, databaseURL)
	if err != nil {
		log.Printf("failed to connect database, running in mock mode: %v", err)
		s = nil
	}

	// 如果数据库连接失败，使用 MockStore
	var st store.Store
	if s != nil {
		st = s
		defer s.Close()
	} else {
		log.Println("WARNING: using mock store, data will not persist")
		st = store.NewMockStore()
	}

	// 启动 Dispatcher
	httpClient := &http.Client{Timeout: 30 * time.Second}
	d := dispatcher.New(st, httpClient)
	go d.Run(ctx)

	// 启动 HTTP 服务
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	h := api.NewHandler(st)

	r.POST("/api/notifications", h.CreateNotification)
	r.GET("/api/notifications/:id", h.GetNotification)
	r.GET("/api/dead-letters", h.GetDeadLetters)
	r.POST("/api/dead-letters/:id/retry", h.RetryDeadLetter)
	r.GET("/api/stats", h.GetStats)
	r.GET("/api/stats/by-vendor", h.GetStatsByVendor)
	r.GET("/api/stats/retry-distribution", h.GetRetryDistribution)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("server starting on :%s", port)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// 优雅关闭
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server forced shutdown: %v", err)
	}

	log.Println("server exited")
}
