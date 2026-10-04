package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

//go:embed web/dist
var webFS embed.FS

type Server struct {
	store *Store
}

func main() {
	dsn := os.Getenv("KIOSK_DB")
	if dsn == "" {
		dsn = "kiosk.db"
	}
	db, err := gorm.Open(openSQLite(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	if err := autoMigrate(db); err != nil {
		log.Fatalf("迁移失败: %v", err)
	}
	st := NewStore(db)
	seed(st)

	srv := &Server{store: st}
	r := gin.Default()
	api := r.Group("/api")
	{
		api.GET("/readers", srv.listReaders)
		api.POST("/readers", srv.createReader)
		api.GET("/magazines", srv.listMagazines)
		api.POST("/magazines", srv.createMagazine)
		api.POST("/subscriptions", srv.createSubscription)
		api.GET("/subscriptions", srv.listSubscriptions)
		api.GET("/subscriptions/:id", srv.getSubscription)
		api.POST("/subscriptions/:id/pickup-mode", srv.changePickup)
		api.GET("/subscriptions/:id/issues", srv.issueSummary)
		api.POST("/subscriptions/:id/stop", srv.stopSub)

		api.POST("/arrivals", srv.registerArrival)
		api.GET("/arrivals", srv.listArrivals)
		api.GET("/stock", srv.stock)

		api.POST("/missing", srv.reportMissing)
		api.GET("/reissues", srv.listReissues)
		api.POST("/publisher/respond", srv.publisherRespond)

		api.POST("/receive", srv.receive)

		api.GET("/dashboard", srv.dashboard)
	}

	// 托管 Vue 构建产物
	dist, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		log.Fatal(err)
	}
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) >= 4 && path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		f, err := dist.Open(path)
		if err == nil {
			f.Close()
			c.FileFromFS(path, http.FS(dist))
			return
		}
		c.FileFromFS("/", http.FS(dist))
	})

	addr := os.Getenv("KIOSK_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("报刊亭系统已启动: http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}

// ---------- helpers ----------

func atoi(s string) uint {
	n, _ := strconv.ParseUint(s, 10, 64)
	return uint(n)
}

func fail(c *gin.Context, code int, err error) {
	c.JSON(code, gin.H{"error": err.Error()})
}

// ---------- handlers ----------

func (s *Server) listReaders(c *gin.Context) {
	rs, err := s.store.ListReaders()
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, rs)
}

func (s *Server) createReader(c *gin.Context) {
	var in struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	r, err := s.store.CreateReader(in.Name, in.Phone)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, r)
}

func (s *Server) listMagazines(c *gin.Context) {
	ms, err := s.store.ListMagazines()
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, ms)
}

func (s *Server) createMagazine(c *gin.Context) {
	var in struct {
		Title     string      `json:"title"`
		Code      string      `json:"code"`
		UnitPrice int64       `json:"unit_price"`
		Rules     doubleRules `json:"double_issue_rules"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	m, err := s.store.CreateMagazine(in.Title, in.Code, in.UnitPrice, in.Rules)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, m)
}

func (s *Server) createSubscription(c *gin.Context) {
	var in CreateSubInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	sub, err := s.store.CreateSubscription(in)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, sub)
}

func (s *Server) listSubscriptions(c *gin.Context) {
	var readerID uint
	if v := c.Query("reader_id"); v != "" {
		readerID = atoi(v)
	}
	subs, err := s.store.ListSubscriptions(readerID)
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, subs)
}

func (s *Server) getSubscription(c *gin.Context) {
	d, err := s.store.GetSubscription(atoi(c.Param("id")))
	if err != nil {
		fail(c, 404, err)
		return
	}
	c.JSON(200, d)
}

func (s *Server) changePickup(c *gin.Context) {
	var in struct {
		Mode PickupMode `json:"mode"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	sub, err := s.store.ChangePickupMode(atoi(c.Param("id")), in.Mode)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, sub)
}

func (s *Server) issueSummary(c *gin.Context) {
	rows, err := s.store.IssueSummaryForReader(atoi(c.Param("id")))
	if err != nil {
		fail(c, 404, err)
		return
	}
	c.JSON(200, rows)
}

func (s *Server) stopSub(c *gin.Context) {
	res, dup, err := s.store.StopAndRefund(atoi(c.Param("id")))
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, gin.H{"duplicated": dup, "result": res})
}

func (s *Server) registerArrival(c *gin.Context) {
	var in ArrivalInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	a, dup, err := s.store.RegisterArrival(in)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, gin.H{"duplicated": dup, "arrival": a})
}

func (s *Server) listArrivals(c *gin.Context) {
	var magID uint
	if v := c.Query("magazine_id"); v != "" {
		magID = atoi(v)
	}
	as, err := s.store.ListArrivals(magID)
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, as)
}

func (s *Server) stock(c *gin.Context) {
	var magID uint
	if v := c.Query("magazine_id"); v != "" {
		magID = atoi(v)
	}
	rows, err := s.store.StockList(magID)
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, rows)
}

func (s *Server) reportMissing(c *gin.Context) {
	var in struct {
		SubscriptionID uint   `json:"subscription_id"`
		IssueNo        string `json:"issue_no"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	req, dup, err := s.store.ReportMissing(in.SubscriptionID, in.IssueNo)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, gin.H{"duplicated": dup, "request": req})
}

func (s *Server) listReissues(c *gin.Context) {
	rs, err := s.store.ListReissues()
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, rs)
}

func (s *Server) publisherRespond(c *gin.Context) {
	var in struct {
		MagazineID uint   `json:"magazine_id"`
		IssueNo    string `json:"issue_no"`
		Accept     bool   `json:"accept"`
		Reason     string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	req, arr, err := s.store.PublisherRespond(in.MagazineID, in.IssueNo, in.Accept, in.Reason)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, gin.H{"request": req, "arrival": arr})
}

func (s *Server) receive(c *gin.Context) {
	var in ReceiveInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	item, dup, err := s.store.Receive(in)
	if err != nil {
		fail(c, 400, err)
		return
	}
	c.JSON(200, gin.H{"duplicated": dup, "item": item})
}

func (s *Server) dashboard(c *gin.Context) {
	rows, err := s.store.Dashboard()
	if err != nil {
		fail(c, 500, err)
		return
	}
	c.JSON(200, rows)
}
