package main

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerRoutes(r *gin.Engine, svc *Service) {
	api := r.Group("/api")

	api.GET("/publications", func(c *gin.Context) {
		var pubs []Publication
		svc.db.Order("id").Find(&pubs)
		c.JSON(http.StatusOK, pubs)
	})

	api.GET("/publications/:id", func(c *gin.Context) {
		id := parseID(c)
		var pub Publication
		if err := svc.db.First(&pub, id).Error; err != nil {
			fail(c, http.StatusNotFound, "刊物不存在")
			return
		}
		var sched []IssueSchedule
		svc.db.Where("publication_id = ?", id).Order("seq").Find(&sched)
		c.JSON(http.StatusOK, gin.H{"publication": pub, "schedule": sched})
	})

	api.POST("/publications/:id/suspend", func(c *gin.Context) {
		id := parseID(c)
		var body struct {
			Reason string `json:"reason"`
		}
		c.ShouldBindJSON(&body)
		if body.Reason == "" {
			body.Reason = "出版社停刊"
		}
		refunds, err := svc.SuspendPublication(id, body.Reason)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"refunds": refunds})
	})

	api.GET("/subscriptions", func(c *gin.Context) {
		var subs []Subscription
		q := svc.db.Preload("Publication").Order("id")
		if reader := c.Query("reader"); reader != "" {
			q = q.Where("reader_name LIKE ?", "%"+reader+"%")
		}
		q.Find(&subs)
		c.JSON(http.StatusOK, subs)
	})

	api.POST("/subscriptions", func(c *gin.Context) {
		var body struct {
			ReaderName    string `json:"reader_name"`
			Phone         string `json:"phone"`
			PublicationID uint   `json:"publication_id"`
			StartIssueNo  string `json:"start_issue_no"`
			TotalPeriods  int    `json:"total_periods"`
			Copies        int    `json:"copies"`
			PickupMethod  string `json:"pickup_method"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "参数错误")
			return
		}
		if body.ReaderName == "" {
			fail(c, http.StatusBadRequest, "读者姓名不能为空")
			return
		}
		if body.Copies <= 0 {
			body.Copies = 1
		}
		if body.PickupMethod == "" {
			body.PickupMethod = PickupSelf
		}
		sub, err := svc.CreateSubscription(body.ReaderName, body.Phone, body.PublicationID, body.StartIssueNo, body.TotalPeriods, body.Copies, body.PickupMethod)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, sub)
	})

	// 读者视图：逐期履约记录 + 汇总 + 退款明细
	api.GET("/subscriptions/:id/view", func(c *gin.Context) {
		view, err := svc.GetReaderView(parseID(c))
		if err != nil {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		c.JSON(http.StatusOK, view)
	})

	api.PUT("/subscriptions/:id/pickup-method", func(c *gin.Context) {
		var body struct {
			PickupMethod string `json:"pickup_method"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || (body.PickupMethod != PickupSelf && body.PickupMethod != PickupDelivery) {
			fail(c, http.StatusBadRequest, "取刊方式须为 self 或 delivery")
			return
		}
		affected, err := svc.ChangePickupMethod(parseID(c), body.PickupMethod)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"updated_future_issues": affected})
	})

	api.POST("/arrivals", func(c *gin.Context) {
		var body struct {
			PublicationID  uint   `json:"publication_id"`
			IssueNo        string `json:"issue_no"`
			Quantity       int    `json:"quantity"`
			IdempotencyKey string `json:"idempotency_key"`
			Note           string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "参数错误")
			return
		}
		batch, dup, err := svc.RegisterArrival(body.PublicationID, body.IssueNo, body.Quantity, body.IdempotencyKey, body.Note)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"batch": batch, "duplicate": dup})
	})

	api.GET("/arrivals", func(c *gin.Context) {
		var batches []ArrivalBatch
		svc.db.Order("id desc").Limit(100).Find(&batches)
		c.JSON(http.StatusOK, batches)
	})

	api.GET("/fulfillments", func(c *gin.Context) {
		var fuls []FulfillmentView
		q := svc.db.Table("fulfillments").
			Select("fulfillments.*, subscriptions.reader_name").
			Joins("JOIN subscriptions ON subscriptions.id = fulfillments.subscription_id").
			Order("fulfillments.publication_id, fulfillments.issue_no, fulfillments.subscription_id")
		if pubID := c.Query("publication_id"); pubID != "" {
			q = q.Where("fulfillments.publication_id = ?", pubID)
		}
		if status := c.Query("status"); status != "" {
			q = q.Where("fulfillments.status = ?", status)
		}
		q.Scan(&fuls)
		c.JSON(http.StatusOK, fuls)
	})

	api.POST("/fulfillments/:id/pickup", func(c *gin.Context) {
		var body struct {
			PickedBy string `json:"picked_by"`
		}
		c.ShouldBindJSON(&body)
		f, dup, err := svc.Pickup(parseID(c), body.PickedBy)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"fulfillment": f, "duplicate": dup})
	})

	api.POST("/fulfillments/:id/mark-missing", func(c *gin.Context) {
		f, err := svc.MarkMissing(parseID(c))
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, f)
	})

	api.POST("/fulfillments/:id/reissue-requests", func(c *gin.Context) {
		req, dup, err := svc.RequestReissue(parseID(c))
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"request": req, "duplicate": dup})
	})

	api.GET("/reissue-requests", func(c *gin.Context) {
		var reqs []ReissueRequest
		svc.db.Order("id desc").Find(&reqs)
		c.JSON(http.StatusOK, reqs)
	})

	// 本地模拟出版社回执
	api.POST("/publisher/receipts", func(c *gin.Context) {
		var body struct {
			ReceiptNo        string `json:"receipt_no"`
			ReissueRequestID uint   `json:"reissue_request_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "参数错误")
			return
		}
		req, dup, err := svc.ReceivePublisherReceipt(body.ReceiptNo, body.ReissueRequestID)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"request": req, "duplicate": dup})
	})

	api.GET("/inventories", func(c *gin.Context) {
		var invs []IssueInventory
		q := svc.db.Order("publication_id, issue_no")
		if pubID := c.Query("publication_id"); pubID != "" {
			q = q.Where("publication_id = ?", pubID)
		}
		q.Find(&invs)
		type invView struct {
			IssueInventory
			Available int `json:"available"`
		}
		views := make([]invView, 0, len(invs))
		for _, inv := range invs {
			views = append(views, invView{inv, inv.Available()})
		}
		c.JSON(http.StatusOK, views)
	})

	api.GET("/refunds", func(c *gin.Context) {
		var refunds []Refund
		q := svc.db.Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("issue_no") }).Order("id")
		if subID := c.Query("subscription_id"); subID != "" {
			q = q.Where("subscription_id = ?", subID)
		}
		q.Find(&refunds)
		c.JSON(http.StatusOK, refunds)
	})
}

func parseID(c *gin.Context) uint {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id)
}

func fail(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}
