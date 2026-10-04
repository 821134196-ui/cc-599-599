package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ---------- 错误 ----------

var (
	ErrNotFound      = errors.New("记录不存在")
	ErrAlreadyDone   = errors.New("该操作已处理过，重复提交不生效")
	ErrWrongState    = errors.New("当前状态不允许该操作")
	ErrReceiptDup    = errors.New("回执号已存在，重复回执只能入账一次")
	ErrSubStopped    = errors.New("订阅已停刊")
)

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// ---------- 期号工具 ----------

// parseIssue 解析 "2026-01" → 年、月
func parseIssue(no string) (int, int, error) {
	p := strings.Split(no, "-")
	if len(p) != 2 {
		return 0, 0, fmt.Errorf("期号格式应为 YYYY-MM: %s", no)
	}
	y, err1 := strconv.Atoi(p[0])
	m, err2 := strconv.Atoi(p[1])
	if err1 != nil || err2 != nil || m < 1 || m > 12 {
		return 0, 0, fmt.Errorf("非法期号: %s", no)
	}
	return y, m, nil
}

func fmtIssue(y, m int) string { return fmt.Sprintf("%04d-%02d", y, m) }

// mergedRule 合刊规则中“起始月”视角的索引
type mergedRule struct {
	issueNo string // 合刊期号，如 2026-07-08
	parts   int    // 合并的月数
	credit  int    // 折算期数
	label   string
}

// firstMergedMonth 返回某年某月是否为某合刊的起始月。
func firstMergedMonth(rules doubleRules, y, m int) (mergedRule, bool) {
	for issueNo, credit := range rules {
		p := strings.Split(issueNo, "-")
		if len(p) < 3 {
			continue
		}
		ry, _ := strconv.Atoi(p[0])
		rm, _ := strconv.Atoi(p[1])
		if ry == y && rm == m {
			ms := []string{}
			for _, s := range p[1:] {
				ms = append(ms, strconv.Itoa(mustAtoi(s)))
			}
			return mergedRule{
				issueNo: issueNo,
				parts:   len(p) - 1,
				credit:  credit,
				label:   fmt.Sprintf("%d年第%s期（合刊）", y, strings.Join(ms, "-")),
			}, true
		}
	}
	return mergedRule{}, false
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ---------- 基础资料 ----------

func (s *Store) CreateReader(name, phone string) (*Reader, error) {
	r := &Reader{Name: name, Phone: phone}
	return r, s.db.Create(r).Error
}

func (s *Store) ListReaders() ([]Reader, error) {
	var rs []Reader
	return rs, s.db.Order("id").Find(&rs).Error
}

func (s *Store) CreateMagazine(title, code string, unitPrice int64, rules doubleRules) (*Magazine, error) {
	if rules == nil {
		rules = doubleRules{}
	}
	m := &Magazine{Title: title, Code: code, UnitPrice: unitPrice, Rules: rules}
	return m, s.db.Create(m).Error
}

func (s *Store) ListMagazines() ([]Magazine, error) {
	var ms []Magazine
	return ms, s.db.Order("id").Find(&ms).Error
}

// ---------- 订阅与逐期号生成 ----------

// planItems 按签约时的合刊折算规则生成逐期号履约计划。
// 总折算期数 = totalIssues；合刊只有一条期号记录但 credit=2。
func planItems(startIssueNo string, totalIssues int, rules doubleRules) ([]FulfillmentItem, error) {
	y, m, err := parseIssue(startIssueNo)
	if err != nil {
		return nil, err
	}
	if totalIssues <= 0 {
		return nil, errors.New("订阅期数必须大于 0")
	}
	items := []FulfillmentItem{}
	credits := 0
	seq := 0
	for credits < totalIssues {
		seq++
		if rg, ok := firstMergedMonth(rules, y, m); ok {
			// 若合刊会使折算期数超出订阅总期数，则订阅在合刊前结束。
			if credits+rg.credit > totalIssues {
				break
			}
			items = append(items, FulfillmentItem{
				IssueNo:      rg.issueNo,
				DisplayLabel: rg.label,
				Credit:       rg.credit,
				Sequence:     seq,
			})
			credits += rg.credit
			m += rg.parts
		} else {
			no := fmtIssue(y, m)
			items = append(items, FulfillmentItem{
				IssueNo:      no,
				DisplayLabel: fmt.Sprintf("%d年第%d期", y, m),
				Credit:       1,
				Sequence:     seq,
			})
			credits++
			m++
		}
		if m > 12 {
			m -= 12
			y++
		}
	}
	return items, nil
}

type CreateSubInput struct {
	ReaderID     uint       `json:"reader_id"`
	MagazineID   uint       `json:"magazine_id"`
	StartIssueNo string     `json:"start_issue_no"`
	TotalIssues  int        `json:"total_issues"`
	PickupMode   PickupMode `json:"pickup_mode"`
}

func (s *Store) CreateSubscription(in CreateSubInput) (*Subscription, error) {
	var mag Magazine
	if err := s.db.First(&mag, in.MagazineID).Error; err != nil {
		return nil, ErrNotFound
	}
	var rd Reader
	if err := s.db.First(&rd, in.ReaderID).Error; err != nil {
		return nil, ErrNotFound
	}
	mode := in.PickupMode
	if mode == "" {
		mode = PickupToStore
	}
	planned, err := planItems(in.StartIssueNo, in.TotalIssues, mag.Rules)
	if err != nil {
		return nil, err
	}
	sub := &Subscription{
		ReaderID:     in.ReaderID,
		MagazineID:   in.MagazineID,
		StartIssueNo: in.StartIssueNo,
		TotalIssues:  in.TotalIssues,
		UnitPrice:    mag.UnitPrice,
		PickupMode:   mode,
		Status:       "active",
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(sub).Error; err != nil {
			return err
		}
		for i := range planned {
			planned[i].SubscriptionID = sub.ID
			planned[i].Status = ItemPending
			planned[i].EffectiveMode = mode
			if err := tx.Create(&planned[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// SubscriptionDetail 读者/店员看到的一份订阅的逐期号视图
type SubscriptionDetail struct {
	Subscription
	MagazineTitle string           `json:"magazine_title"`
	ReaderName    string           `json:"reader_name"`
	Items         []FulfillmentItem `json:"items"`
	CreditsTotal  int              `json:"credits_total"`
	CreditsGot    int              `json:"credits_received"` // 已签收折算期数
	Refund        *Refund          `json:"refund,omitempty"`
}

func (s *Store) GetSubscription(id uint) (*SubscriptionDetail, error) {
	var sub Subscription
	if err := s.db.First(&sub, id).Error; err != nil {
		return nil, ErrNotFound
	}
	var mag Magazine
	s.db.First(&mag, sub.MagazineID)
	var rd Reader
	s.db.First(&rd, sub.ReaderID)
	var items []FulfillmentItem
	if err := s.db.Order("sequence").Find(&items, "subscription_id = ?", id).Error; err != nil {
		return nil, err
	}
	d := &SubscriptionDetail{
		Subscription:  sub,
		MagazineTitle: mag.Title,
		ReaderName:    rd.Name,
		Items:         items,
		CreditsTotal:  sub.TotalIssues,
	}
	for _, it := range items {
		if it.Status == ItemReceived {
			d.CreditsGot += it.Credit
		}
	}
	var rf Refund
	if err := s.db.First(&rf, "subscription_id = ?", id).Error; err == nil {
		d.Refund = &rf
	}
	return d, nil
}

func (s *Store) ListSubscriptions(readerID uint) ([]Subscription, error) {
	q := s.db.Order("id")
	if readerID > 0 {
		q = q.Where("reader_id = ?", readerID)
	}
	var subs []Subscription
	return subs, q.Find(&subs).Error
}

// ---------- 取刊方式调整：只对未来刊期生效 ----------

// ChangePickupMode 更新尚未到货的未来刊期；已到货/已签收/缺刊中的记录保持原样。
func (s *Store) ChangePickupMode(subID uint, newMode PickupMode) (*Subscription, error) {
	var sub Subscription
	if err := s.db.First(&sub, subID).Error; err != nil {
		return nil, ErrNotFound
	}
	if sub.Status != "active" {
		return nil, ErrSubStopped
	}
	switch newMode {
	case PickupToStore, PickupMail, PickupHome:
	default:
		return nil, fmt.Errorf("未知取刊方式: %s", newMode)
	}
	if sub.PickupMode == newMode {
		return &sub, nil
	}
	// 变更生效点：当前最新一期“已到货或已签收”的期号，其后才算未来刊期。
	var latest FulfillmentItem
	s.db.Where("subscription_id = ? AND status IN ?", subID, []string{ItemArrived, ItemReceived}).
		Order("sequence DESC").First(&latest)

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&FulfillmentItem{}).
			Where("subscription_id = ? AND status = ?", subID, ItemPending).
			Update("effective_mode", newMode).Error; err != nil {
			return err
		}
		hist := append(sub.PickupHist, pickupChange{
			FromMode:      sub.PickupMode,
			ToMode:        newMode,
			ChangeIssueNo: latest.IssueNo,
			ChangedAt:     time.Now(),
		})
		return tx.Model(&sub).Updates(map[string]any{
			"pickup_mode":    newMode,
			"pickup_hist":    hist,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	s.db.First(&sub, subID)
	return &sub, nil
}

// ---------- 到货登记（幂等）+ 自动分配 ----------

type ArrivalInput struct {
	MagazineID       uint   `json:"magazine_id"`
	IssueNo          string `json:"issue_no"`
	Quantity         int    `json:"quantity"`
	PublisherReceipt string `json:"publisher_receipt"`
	Kind             string `json:"kind"` // normal / reissue
	Note             string `json:"note"`
}

// RegisterArrival 登记到货。出版社回执号唯一：重复登记同一回执直接返回原批次，
// 库存与已分配数量均不发生第二次变化。
func (s *Store) RegisterArrival(in ArrivalInput) (*StockArrival, bool, error) {
	if in.Quantity <= 0 {
		return nil, false, errors.New("到货数量必须大于 0")
	}
	if in.PublisherReceipt == "" {
		return nil, false, errors.New("出版社回执号不能为空")
	}
	kind := in.Kind
	if kind == "" {
		kind = ArrivalNormal
	}
	// 幂等检查：回执号已存在 → 原样返回，不做任何变动。
	var existing StockArrival
	if err := s.db.Where("publisher_receipt = ?", in.PublisherReceipt).First(&existing).Error; err == nil {
		return &existing, true, nil // duplicated=true
	}
	if _, _, err := parseIssue(prefixIssue(in.IssueNo)); err != nil {
		return nil, false, err
	}
	a := &StockArrival{
		MagazineID:       in.MagazineID,
		IssueNo:          in.IssueNo,
		Kind:             kind,
		Quantity:         in.Quantity,
		PublisherReceipt: in.PublisherReceipt,
		Note:             in.Note,
		ArrivedAt:        time.Now(),
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(a).Error; err != nil {
			if isUniqueErr(err) {
				return ErrReceiptDup
			}
			return err
		}
		return s.allocate(tx, a)
	})
	if err != nil {
		if errors.Is(err, ErrReceiptDup) {
			s.db.Where("publisher_receipt = ?", in.PublisherReceipt).First(&existing)
			return &existing, true, nil
		}
		return nil, false, err
	}
	s.db.First(a, a.ID)
	return a, false, nil
}

// prefixIssue 兼容合刊期号 "2026-07-08"，取前两段校验。
func prefixIssue(no string) string {
	p := strings.Split(no, "-")
	if len(p) >= 2 {
		return p[0] + "-" + p[1]
	}
	return no
}

// allocate 将到货批次分配给等待中的订户。
// normal → pending；reissue → missing/reissuing（缺刊订户）。
// 每个履约记录只可能被分配一次（状态守卫），故履约次数不会重复增加。
func (s *Store) allocate(tx *gorm.DB, a *StockArrival) error {
	var targetStatus []string
	if a.Kind == ArrivalReissue {
		targetStatus = []string{ItemMissing, ItemReissuing}
	} else {
		targetStatus = []string{ItemPending}
	}
	var items []FulfillmentItem
	if err := tx.
		Joins("JOIN subscriptions ON subscriptions.id = fulfillment_items.subscription_id").
		Where("fulfillment_items.issue_no = ? AND fulfillment_items.status IN ?",
			a.IssueNo, targetStatus).
		Where("subscriptions.magazine_id = ? AND subscriptions.status = ?", a.MagazineID, "active").
		Order("fulfillment_items.subscription_id, fulfillment_items.sequence").
		Find(&items).Error; err != nil {
		return err
	}
	allocated := 0
	for _, it := range items {
		if allocated >= a.Quantity {
			break
		}
		// 条件更新作为最后一道状态守卫：并发/重复分配下只有一处会成功。
		res := tx.Model(&FulfillmentItem{}).
			Where("id = ? AND status IN ?", it.ID, targetStatus).
			Updates(map[string]any{
				"status":     ItemArrived,
				"arrival_id": a.ID,
				"is_reissue": a.Kind == ArrivalReissue,
				"updated_at": time.Now(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			allocated++
		}
	}
	return tx.Model(a).Update("allocated", a.Allocated+allocated).Error
}

func (s *Store) ListArrivals(magazineID uint) ([]StockArrival, error) {
	q := s.db.Order("arrived_at DESC, id DESC")
	if magazineID > 0 {
		q = q.Where("magazine_id = ?", magazineID)
	}
	var as []StockArrival
	return as, q.Find(&as).Error
}

// StockRow 库存视图：按 期号+批次 给出可发数。
type StockRow struct {
	StockArrival
	Available int `json:"available"`
}

func (s *Store) StockList(magazineID uint) ([]StockRow, error) {
	arrs, err := s.ListArrivals(magazineID)
	if err != nil {
		return nil, err
	}
	rows := make([]StockRow, 0, len(arrs))
	for _, a := range arrs {
		rows = append(rows, StockRow{StockArrival: a, Available: a.Quantity - a.Allocated})
	}
	return rows, nil
}

// ---------- 缺刊与出版社补发（本地模拟回执） ----------

func reissueKey(magID uint, issueNo string) string {
	return fmt.Sprintf("M%d:%s", magID, issueNo)
}

// ReportMissing 店员确认某订阅某期缺刊，并向出版社申请补发（同一期号幂等）。
func (s *Store) ReportMissing(subID uint, issueNo string) (*ReissueRequest, bool, error) {
	var sub Subscription
	if err := s.db.First(&sub, subID).Error; err != nil {
		return nil, false, ErrNotFound
	}
	var item FulfillmentItem
	if err := s.db.Where("subscription_id = ? AND issue_no = ?", subID, issueNo).First(&item).Error; err != nil {
		return nil, false, ErrNotFound
	}
	key := reissueKey(sub.MagazineID, issueNo)

	// 已有申请单 → 幂等返回；同时保证该订户的该期进入缺刊状态。
	var req ReissueRequest
	if err := s.db.Where("request_key = ?", key).First(&req).Error; err == nil {
		if item.Status == ItemPending {
			s.db.Model(&item).Where("status = ?", ItemPending).
				Update("status", ItemMissing)
		}
		return &req, true, nil
	}
	switch item.Status {
	case ItemReceived, ItemArrived:
		return nil, false, ErrWrongState
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&FulfillmentItem{}).Where("id = ? AND status = ?", item.ID, ItemPending).
			Update("status", ItemMissing).Error; err != nil {
			return err
		}
		req = ReissueRequest{
			MagazineID:     sub.MagazineID,
			IssueNo:        issueNo,
			SubscriptionID: subID,
			ItemID:         item.ID,
			Status:         ReissueRequested,
			RequestKey:     key,
		}
		if err := tx.Create(&req).Error; err != nil {
			if isUniqueErr(err) {
				return ErrAlreadyDone
			}
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyDone) {
			s.db.Where("request_key = ?", key).First(&req)
			return &req, true, nil
		}
		return nil, false, err
	}
	return &req, false, nil
}

// PublisherRespond 模拟出版社回执：受理并发货。
// 补发件以独立批次入库（回执号确定性生成），收到重复回执不会二次入库。
func (s *Store) PublisherRespond(magID uint, issueNo string, accept bool, reason string) (*ReissueRequest, *StockArrival, error) {
	key := reissueKey(magID, issueNo)
	var req ReissueRequest
	if err := s.db.Where("request_key = ?", key).First(&req).Error; err != nil {
		return nil, nil, ErrNotFound
	}
	var mag Magazine
	if err := s.db.First(&mag, magID).Error; err != nil {
		return nil, nil, ErrNotFound
	}
	if req.Status == ReissueShipped {
		// 重复回执：返回既有补发批次，库存不再变动。
		var arr StockArrival
		s.db.Where("publisher_receipt = ?", reissueReceipt(mag.Code, issueNo)).First(&arr)
		return &req, &arr, nil
	}
	if !accept {
		req.Status = ReissueRejected
		req.RejectReason = reason
		s.db.Save(&req)
		return &req, nil, nil
	}

	// 发货数量 = 该期当前缺刊中的订户份数（含 reissuing/missing）。
	var n int64
	s.db.Model(&FulfillmentItem{}).
		Joins("JOIN subscriptions ON subscriptions.id = fulfillment_items.subscription_id").
		Where("fulfillment_items.issue_no = ? AND fulfillment_items.status IN ? AND subscriptions.magazine_id = ?",
			issueNo, []string{ItemMissing, ItemReissuing}, magID).Count(&n)
	if n == 0 {
		return nil, nil, ErrWrongState
	}

	receipt := reissueReceipt(mag.Code, issueNo)
	var existing StockArrival
	if err := s.db.Where("publisher_receipt = ?", receipt).First(&existing).Error; err == nil {
		// 出版社回执重复送达：幂等。
		req.Status = ReissueShipped
		s.db.Save(&req)
		return &req, &existing, nil
	}

	arr := &StockArrival{
		MagazineID:       magID,
		IssueNo:          issueNo,
		Kind:             ArrivalReissue,
		Quantity:         int(n),
		PublisherReceipt: receipt,
		Note:             "出版社补发件",
		ArrivedAt:        time.Now(),
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(arr).Error; err != nil {
			if isUniqueErr(err) {
				return ErrReceiptDup
			}
			return err
		}
		// 缺刊订户进入“补发中”，再由补发批次统一分配为到刊。
		if err := tx.Model(&FulfillmentItem{}).
			Where("issue_no = ? AND status = ?", issueNo, ItemMissing).
			Update("status", ItemReissuing).Error; err != nil {
			return err
		}
		if err := s.allocate(tx, arr); err != nil {
			return err
		}
		req.Status = ReissueShipped
		req.PublisherNo = "PUB-" + strings.ToUpper(receipt)
		return tx.Save(&req).Error
	})
	if err != nil {
		return nil, nil, err
	}
	s.db.First(arr, arr.ID)
	return &req, arr, nil
}

func reissueReceipt(magCode, issueNo string) string {
	return fmt.Sprintf("RE-%s-%s", strings.ToUpper(magCode), issueNo)
}

func (s *Store) ListReissues() ([]ReissueRequest, error) {
	var rs []ReissueRequest
	return rs, s.db.Order("id DESC").Find(&rs).Error
}

// ---------- 签收（幂等） ----------

type ReceiveInput struct {
	SubscriptionID uint   `json:"subscription_id"`
	IssueNo        string `json:"issue_no"`
	Receipt        string `json:"receipt"` // 签收回执号
}

// Receive 读者领取签收。已签收的期重复提交、或相同回执号重复送达，
// 履约次数只计一次。
func (s *Store) Receive(in ReceiveInput) (*FulfillmentItem, bool, error) {
	if in.Receipt == "" {
		return nil, false, errors.New("签收回执号不能为空")
	}
	var item FulfillmentItem
	if err := s.db.Where("subscription_id = ? AND issue_no = ?", in.SubscriptionID, in.IssueNo).
		First(&item).Error; err != nil {
		return nil, false, ErrNotFound
	}
	if item.Status == ItemReceived {
		return &item, true, nil // 重复签收：原样返回，不重复计数
	}
	if item.Status != ItemArrived {
		return nil, false, ErrWrongState
	}
	now := time.Now()
	receipt := in.Receipt
	res := s.db.Model(&FulfillmentItem{}).
		Where("id = ? AND status = ?", item.ID, ItemArrived).
		Updates(map[string]any{
			"status":          ItemReceived,
			"received_at":     now,
			"receive_receipt": &receipt,
			"updated_at":      now,
		})
	if res.Error != nil {
		if isUniqueErr(res.Error) {
			return nil, false, ErrReceiptDup
		}
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		// 已被其它请求抢先签收
		s.db.First(&item, item.ID)
		return &item, true, nil
	}
	s.db.First(&item, item.ID)
	return &item, false, nil
}

// ---------- 停刊退款 ----------

// RefundResult 退款及明细
type RefundResult struct {
	Refund      Refund        `json:"refund"`
	Lines       []refundLine  `json:"lines"`
	Released    int           `json:"released_allocations"` // 已到店未取、退回可售库存的份数
}

// StopAndRefund 停刊退款：尚未签收的期号一律视为未履约，按折算期数退款；
// 已到店但未取的份释放回库存可发数；缺刊/补发中同样退款。
func (s *Store) StopAndRefund(subID uint) (*RefundResult, bool, error) {
	var sub Subscription
	if err := s.db.First(&sub, subID).Error; err != nil {
		return nil, false, ErrNotFound
	}
	if sub.Status != "active" {
		// 重复停刊：返回既有退款单，金额不重复计算。
		var existing Refund
		if err := s.db.First(&existing, "subscription_id = ?", subID).Error; err == nil {
			return &RefundResult{Refund: existing, Lines: existing.Details}, true, nil
		}
		return nil, false, ErrSubStopped
	}
	var items []FulfillmentItem
	if err := s.db.Where("subscription_id = ?", subID).Order("sequence").Find(&items).Error; err != nil {
		return nil, false, err
	}
	lines := []refundLine{}
	unfulfilled := 0
	var amount int64
	released := 0
	for _, it := range items {
		if it.Status == ItemReceived {
			continue
		}
		amt := int64(it.Credit) * sub.UnitPrice
		lines = append(lines, refundLine{
			Sequence: it.Sequence,
			IssueNo:  it.IssueNo,
			Label:    it.DisplayLabel,
			Credit:   it.Credit,
			Status:   it.Status,
			Amount:   amt,
		})
		unfulfilled += it.Credit
		amount += amt
		if it.Status == ItemArrived && it.ArrivalID != nil {
			released++
		}
	}
	rf := Refund{
		SubscriptionID: subID,
		UnfulfilledN:   unfulfilled,
		PerIssuePrice:  sub.UnitPrice,
		Amount:         amount,
		Details:        lines,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 释放已到店未取份的库存占用
		for _, it := range items {
			if it.Status == ItemArrived && it.ArrivalID != nil {
				if err := tx.Model(&StockArrival{}).
					Where("id = ? AND allocated > 0", *it.ArrivalID).
					UpdateColumn("allocated", gorm.Expr("allocated - 1")).Error; err != nil {
					return err
				}
			}
			if it.Status != ItemReceived {
				if err := tx.Model(&FulfillmentItem{}).Where("id = ?", it.ID).
					Update("status", "refunded").Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&sub).Update("status", "stopped").Error; err != nil {
			return err
		}
		return tx.Create(&rf).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &RefundResult{Refund: rf, Lines: lines, Released: released}, false, nil
}

// ---------- 汇总工具 ----------

type IssueSummary struct {
	SubscriptionID uint   `json:"subscription_id"`
	MagazineID     uint   `json:"magazine_id"`
	IssueNo        string `json:"issue_no"`
	DisplayLabel  string `json:"display_label"`
	Credit        int    `json:"credit"`
	Pending       int    `json:"pending"`
	Arrived       int    `json:"arrived"`
	Received      int    `json:"received"`
	Missing       int    `json:"missing"`
	Reissuing     int    `json:"reissuing"`
}

// IssueSummaryForReader 读者页面：按期号列出已到刊/已领取/缺刊/待补发
func (s *Store) IssueSummaryForReader(subID uint) ([]IssueSummary, error) {
	d, err := s.GetSubscription(subID)
	if err != nil {
		return nil, err
	}
	out := make([]IssueSummary, 0, len(d.Items))
	for _, it := range d.Items {
		row := IssueSummary{
			IssueNo:      it.IssueNo,
			DisplayLabel: it.DisplayLabel,
			Credit:       it.Credit,
		}
		switch it.Status {
		case ItemPending:
			row.Pending = 1
		case ItemArrived:
			row.Arrived = 1
		case ItemReceived:
			row.Received = 1
		case ItemMissing:
			row.Missing = 1
		case ItemReissuing:
			row.Reissuing = 1
		}
		out = append(out, row)
	}
	return out, nil
}

// Dashboard 店员总览：按期刊+期号的状态计数
func (s *Store) Dashboard() ([]IssueSummary, error) {
	var items []FulfillmentItem
	if err := s.db.Find(&items).Error; err != nil {
		return nil, err
	}
	var subs []Subscription
	s.db.Find(&subs)
	subMag := map[uint]uint{}
	for _, sb := range subs {
		subMag[sb.ID] = sb.MagazineID
	}
	m := map[string]*IssueSummary{}
	order := []string{}
	for _, it := range items {
		k := fmt.Sprintf("%d|%s", it.SubscriptionID, it.IssueNo)
		row, ok := m[k]
		if !ok {
			row = &IssueSummary{
				SubscriptionID: it.SubscriptionID,
				MagazineID:     subMag[it.SubscriptionID],
				IssueNo:        it.IssueNo,
				DisplayLabel:   it.DisplayLabel,
				Credit:         it.Credit,
			}
			m[k] = row
			order = append(order, k)
		}
		switch it.Status {
		case ItemPending:
			row.Pending++
		case ItemArrived:
			row.Arrived++
		case ItemReceived:
			row.Received++
		case ItemMissing:
			row.Missing++
		case ItemReissuing:
			row.Reissuing++
		}
	}
	sort.Strings(order)
	out := make([]IssueSummary, 0, len(order))
	for _, k := range order {
		out = append(out, *m[k])
	}
	return out, nil
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
