package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

//go:embed static
var staticFS embed.FS

func openDB(dsn string) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.New(log.New(os.Stdout, "", 0), gormlogger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true, // 查重式查找属正常流程，不刷日志
		}),
	})
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	// SQLite 单写者：限制连接数，避免 database is locked
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&Publication{}, &IssueSchedule{}, &Subscription{}, &Fulfillment{},
		&IssueInventory{}, &ArrivalBatch{}, &ReissueRequest{}, &Refund{}, &RefundItem{},
	); err != nil {
		log.Fatalf("建表失败: %v", err)
	}
	return db
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "demo" {
		runDemo()
		return
	}

	dsn := "kiosk.db"
	if v := os.Getenv("KIOSK_DB"); v != "" {
		dsn = v
	}
	db := openDB(dsn)
	svc := NewService(db)
	if err := Seed(db, svc); err != nil {
		log.Fatalf("初始化演示数据失败: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	registerRoutes(r, svc)

	// 静态资源（Vue 前端），嵌进二进制，单文件即可运行
	staticRoot, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatal(err)
	}
	indexHTML, _ := fs.ReadFile(staticRoot, "index.html")
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})
	r.NoRoute(func(c *gin.Context) {
		c.FileFromFS(c.Request.URL.Path, http.FS(staticRoot))
	})

	addr := ":8080"
	if v := os.Getenv("KIOSK_ADDR"); v != "" {
		addr = v
	}
	fmt.Printf("报刊亭期刊订阅系统已启动: http://localhost%s\n", addr)
	fmt.Println("  读者页/店员台/库存页均在首页切换；运行 `go run . demo` 可查看对账演示")
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}
