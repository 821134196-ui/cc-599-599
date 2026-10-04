package main

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
)

// Service 业务逻辑层。所有写操作都在事务内完成，重复请求靠唯一约束 + 状态检查保证只生效一次。
type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

// ---------- 订阅 ----------

// CreateSubscription 创建订阅：按刊期表从起始期累加折算期数，生成逐期履约记录。
func (s *Service) CreateSubscription(readerName, phone string, pubID uint, startIssueNo string, totalPeriods, copies int, pickupMethod string) (*Subscription, error) {
	if totalPeriods <= 0 || copies <= 0 {
		return nil, errors.New("期数和份数必须为正数")
	}
	var pub Publication
	if err := s.db.First(&pub, pubID).Error; err != nil {
		return nil, fmt.Errorf("刊物不存在: %w", err)
	}
	if pub.Status != PubActive {
		return nil, errors.New("刊物已停刊，无法新订")
	}
	var sched []IssueSchedule
	if err := s.db.Where("publication_id = ?", pubID).Order("seq").Find(&sched).Error; err != nil {
		return nil, err
	}
	// 从起始期开始走刊期表，累计折算期数直到覆盖签约期数
	started := false
	acc := 0
	var covered []IssueSchedule
	for _, it := range sched {
		if !started {
			if it.IssueNo != startIssueNo {
				continue
			}
			started = true
		}
		covered = append(covered, it)
		acc += it.CountsAs
		if acc >= totalPeriods {
			break
		}
	}
	if !started {
		return nil, fmt.Errorf("起始期号 %s 不在刊期表中", startIssueNo)
	}
	if acc < totalPeriods {
		return nil, fmt.Errorf("刊期表从 %s 起只有 %d 期，不足签约的 %d 期", startIssueNo, acc, totalPeriods)
	}

	sub := &Subscription{
		ReaderName: readerName, Phone: phone, PublicationID: pubID,
		StartIssueNo: startIssueNo, TotalPeriods: totalPeriods, Copies: copies,
		UnitPriceCents: pub.UnitPriceCents,
		PaidCents:      int64(totalPeriods) * pub.UnitPriceCents * int64(copies),
		PickupMethod:   pickupMethod, Status: SubActive, CreatedAt: time.Now(),
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(sub).Error; err != nil {
			return err
		}
		for _, it := range covered {
			f := Fulfillment{
				SubscriptionID: sub.ID, PublicationID: pubID, IssueNo: it.IssueNo,
				CountsAs: it.CountsAs, Copies: copies, PickupMethod: pickupMethod,
				Status: FulPending,
			}
			if err := tx.Create(&f).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sub.Publication = pub
	return sub, nil
}

// ChangePickupMethod 调整取刊方式：只对未来刊期（待到刊）生效，已到刊/已领取等记录保持原样。
func (s *Service) ChangePickupMethod(subID uint, method string) (int64, error) {
	var sub Subscription
	if err := s.db.First(&sub, subID).Error; err != nil {
		return 0, fmt.Errorf("订阅不存在: %w", err)
	}
	var affected int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Subscription{}).Where("id = ?", subID).Update("pickup_method", method).Error; err != nil {
			return err
		}
		// 仅更新未来刊期（pending）；arrived/picked/missing/reissue_requested/refunded 均保持原快照
		res := tx.Model(&Fulfillment{}).
			Where("subscription_id = ? AND status = ?", subID, FulPending).
			Update("pickup_method", method)
		affected = res.RowsAffected
		return res.Error
	})
	return affected, err
}

// ---------- 到货与分配 ----------

// RegisterArrival 登记到货。幂等：同一 IdempotencyKey 重复登记，库存与履约只变动一次。
// 返回 (批次, 是否重复, 错误)。
func (s *Service) RegisterArrival(pubID uint, issueNo string, qty int, idemKey, note string) (*ArrivalBatch, bool, error) {
	if qty <= 0 {
		return nil, false, errors.New("到货数量必须为正数")
	}
	if idemKey == "" {
		return nil, false, errors.New("缺少幂等键")
	}
	// 先查重：已存在同键批次则直接返回，不产生任何变动
	var existing ArrivalBatch
	if err := s.db.Where("idempotency_key = ?", idemKey).First(&existing).Error; err == nil {
		return &existing, true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	batch := &ArrivalBatch{PublicationID: pubID, IssueNo: issueNo, Quantity: qty, IdempotencyKey: idemKey, Note: note, CreatedAt: time.Now()}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(batch).Error; err != nil {
			return err // 唯一索引兜底：并发重复键会失败回滚
		}
		inv, err := upsertInventory(tx, pubID, issueNo)
		if err != nil {
			return err
		}
		if err := tx.Model(inv).Update("arrived_qty", gorm.Expr("arrived_qty + ?", qty)).Error; err != nil {
			return err
		}
		// 分配给等待中的履约记录，并把仍分不到的标记为缺刊
		return allocateAndMarkMissing(tx, pubID, issueNo)
	})
	if err != nil {
		return nil, false, err
	}
	return batch, false, nil
}

func upsertInventory(tx *gorm.DB, pubID uint, issueNo string) (*IssueInventory, error) {
	var inv IssueInventory
	err := tx.Where("publication_id = ? AND issue_no = ?", pubID, issueNo).First(&inv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		inv = IssueInventory{PublicationID: pubID, IssueNo: issueNo}
		if err := tx.Create(&inv).Error; err != nil {
			return nil, err
		}
		return &inv, nil
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// allocateAndMarkMissing 把可分配库存按订阅先后分给 pending/missing 的履约记录；
// 本轮到货后仍分不到的 pending 记录标记为缺刊（后续批次或补发件仍可补上）。
func allocateAndMarkMissing(tx *gorm.DB, pubID uint, issueNo string) error {
	var inv IssueInventory
	if err := tx.Where("publication_id = ? AND issue_no = ?", pubID, issueNo).First(&inv).Error; err != nil {
		return err
	}
	available := inv.Available()
	var pendings []Fulfillment
	if err := tx.Where("publication_id = ? AND issue_no = ? AND status IN ?", pubID, issueNo, []string{FulPending, FulMissing}).
		Order("subscription_id").Find(&pendings).Error; err != nil {
		return err
	}
	now := time.Now()
	allocated := 0
	for _, f := range pendings {
		if available >= f.Copies {
			if err := tx.Model(&Fulfillment{}).Where("id = ?", f.ID).
				Updates(map[string]any{"status": FulArrived, "arrived_at": &now}).Error; err != nil {
				return err
			}
			available -= f.Copies
			allocated += f.Copies
		} else if f.Status == FulPending {
			// 本轮到货不足，标记缺刊（missing 的保持 missing）
			if err := tx.Model(&Fulfillment{}).Where("id = ?", f.ID).
				Update("status", FulMissing).Error; err != nil {
				return err
			}
		}
	}
	if allocated > 0 {
		if err := tx.Model(&IssueInventory{}).Where("id = ?", inv.ID).
			Update("allocated_qty", gorm.Expr("allocated_qty + ?", allocated)).Error; err != nil {
			return err
		}
	}
	return nil
}

// ---------- 签收 ----------

// Pickup 领取签收。幂等：重复签收回执只生效一次，返回 duplicate=true。
func (s *Service) Pickup(fulfillmentID uint, pickedBy string) (*Fulfillment, bool, error) {
	if pickedBy == "" {
		return nil, false, errors.New("签收人不能为空")
	}
	var result Fulfillment
	duplicate := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var f Fulfillment
		if err := tx.First(&f, fulfillmentID).Error; err != nil {
			return fmt.Errorf("履约记录不存在: %w", err)
		}
		if f.Status == FulPicked {
			result = f
			duplicate = true // 重复回执：不再变动库存与计数
			return nil
		}
		if f.Status != FulArrived {
			return fmt.Errorf("当前状态(%s)不可签收，需先到刊", f.Status)
		}
		now := time.Now()
		if err := tx.Model(&Fulfillment{}).Where("id = ? AND status = ?", f.ID, FulArrived).
			Updates(map[string]any{"status": FulPicked, "picked_at": &now, "picked_by": pickedBy}).Error; err != nil {
			return err
		}
		if err := tx.Model(&IssueInventory{}).Where("publication_id = ? AND issue_no = ?", f.PublicationID, f.IssueNo).
			Update("picked_qty", gorm.Expr("picked_qty + ?", f.Copies)).Error; err != nil {
			return err
		}
		return tx.First(&result, f.ID).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &result, duplicate, nil
}

// ---------- 缺刊与补发 ----------

// MarkMissing 手工登记缺刊（仅 pending 可标记）。
func (s *Service) MarkMissing(fulfillmentID uint) (*Fulfillment, error) {
	var f Fulfillment
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&f, fulfillmentID).Error; err != nil {
			return fmt.Errorf("履约记录不存在: %w", err)
		}
		if f.Status == FulMissing {
			return nil // 幂等
		}
		if f.Status != FulPending {
			return fmt.Errorf("当前状态(%s)不可登记缺刊", f.Status)
		}
		return tx.Model(&Fulfillment{}).Where("id = ?", f.ID).Update("status", FulMissing).Error
	})
	if err != nil {
		return nil, err
	}
	return s.reloadFulfillment(f.ID)
}

// RequestReissue 向出版社申请补发。一个履约记录只能申请一次（重复申请返回原申请）。
func (s *Service) RequestReissue(fulfillmentID uint) (*ReissueRequest, bool, error) {
	var existing ReissueRequest
	if err := s.db.Where("fulfillment_id = ?", fulfillmentID).First(&existing).Error; err == nil {
		return &existing, true, nil
	}
	var req ReissueRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var f Fulfillment
		if err := tx.First(&f, fulfillmentID).Error; err != nil {
			return fmt.Errorf("履约记录不存在: %w", err)
		}
		if f.Status != FulMissing {
			return fmt.Errorf("当前状态(%s)不可申请补发，需先登记缺刊", f.Status)
		}
		req = ReissueRequest{
			FulfillmentID: f.ID, SubscriptionID: f.SubscriptionID, PublicationID: f.PublicationID,
			IssueNo: f.IssueNo, Quantity: f.Copies, Status: ReissueRequested, RequestedAt: time.Now(),
		}
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		return tx.Model(&Fulfillment{}).Where("id = ?", f.ID).Update("status", FulReissueRequested).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &req, false, nil
}

// ReceivePublisherReceipt 模拟收到出版社补发回执。
// 幂等：同一回执号或同一申请重复接收，库存与履约只变动一次。
// 补发件归入原期号：不新增履约记录、不增加订阅期数、不重复计交付。
func (s *Service) ReceivePublisherReceipt(receiptNo string, reissueRequestID uint) (*ReissueRequest, bool, error) {
	if receiptNo == "" {
		return nil, false, errors.New("回执号不能为空")
	}
	// 同一回执号已处理过 → 直接返回原申请，不再变动
	var byReceipt ReissueRequest
	if err := s.db.Where("receipt_no = ?", receiptNo).First(&byReceipt).Error; err == nil {
		return &byReceipt, true, nil
	}
	var req ReissueRequest
	duplicate := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&req, reissueRequestID).Error; err != nil {
			return fmt.Errorf("补发申请不存在: %w", err)
		}
		if req.Status == ReissueReceived {
			duplicate = true // 该申请已收过补发件
			return nil
		}
		now := time.Now()
		if err := tx.Model(&ReissueRequest{}).Where("id = ? AND status = ?", req.ID, ReissueRequested).
			Updates(map[string]any{"status": ReissueReceived, "receipt_no": &receiptNo, "received_at": &now}).Error; err != nil {
			return err
		}
		// 补发件入原期号库存
		inv, err := upsertInventory(tx, req.PublicationID, req.IssueNo)
		if err != nil {
			return err
		}
		if err := tx.Model(&IssueInventory{}).Where("id = ?", inv.ID).Updates(map[string]any{
			"arrived_qty":   gorm.Expr("arrived_qty + ?", req.Quantity),
			"reissue_qty":   gorm.Expr("reissue_qty + ?", req.Quantity),
			"allocated_qty": gorm.Expr("allocated_qty + ?", req.Quantity),
		}).Error; err != nil {
			return err
		}
		// 原履约记录直接转为已到刊（补发），不新增记录、不增加期数
		return tx.Model(&Fulfillment{}).Where("id = ?", req.FulfillmentID).
			Updates(map[string]any{"status": FulArrived, "reissued": true, "arrived_at": &now}).Error
	})
	if err != nil {
		return nil, false, err
	}
	if duplicate {
		if err := s.db.First(&req, reissueRequestID).Error; err != nil {
			return nil, false, err
		}
	}
	return &req, duplicate, nil
}

// ---------- 停刊退款 ----------

// SuspendPublication 停刊：为每份有效订阅按未履约期数生成退款（含明细）。
// 幂等：重复停刊返回已有退款，不重复生成。已到刊/已领取视为已履约，不退。
func (s *Service) SuspendPublication(pubID uint, reason string) ([]Refund, error) {
	var pub Publication
	if err := s.db.First(&pub, pubID).Error; err != nil {
		return nil, fmt.Errorf("刊物不存在: %w", err)
	}
	var refunds []Refund
	err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if pub.Status != PubSuspended {
			if err := tx.Model(&Publication{}).Where("id = ?", pubID).
				Updates(map[string]any{"status": PubSuspended, "suspend_reason": reason, "suspended_at": &now}).Error; err != nil {
				return err
			}
		}
		var subs []Subscription
		if err := tx.Where("publication_id = ? AND status = ?", pubID, SubActive).Find(&subs).Error; err != nil {
			return err
		}
		for _, sub := range subs {
			var existing Refund
			if err := tx.Where("subscription_id = ?", sub.ID).First(&existing).Error; err == nil {
				continue // 已退过，幂等跳过
			}
			var fuls []Fulfillment
			if err := tx.Where("subscription_id = ?", sub.ID).Find(&fuls).Error; err != nil {
				return err
			}
			unfulfilledPeriods := 0
			var items []RefundItem
			for _, f := range fuls {
				switch f.Status {
				case FulArrived, FulPicked:
					// 已到刊/已领取 = 已履约，不退
				default:
					// pending/missing/reissue_requested = 未履约
					unfulfilledPeriods += f.CountsAs
					items = append(items, RefundItem{
						IssueNo: f.IssueNo, CountsAs: f.CountsAs,
						AmountCents: int64(f.CountsAs) * sub.UnitPriceCents * int64(sub.Copies),
					})
				}
			}
			var amount int64
			for _, it := range items {
				amount += it.AmountCents
			}
			if unfulfilledPeriods > 0 {
				refund := Refund{
					SubscriptionID: sub.ID, PublicationID: pubID, Reason: reason,
					UnfulfilledPeriods: unfulfilledPeriods, Copies: sub.Copies,
					UnitPriceCents: sub.UnitPriceCents, AmountCents: amount, CreatedAt: now,
				}
				if err := tx.Create(&refund).Error; err != nil {
					return err
				}
				for i := range items {
					items[i].RefundID = refund.ID
				}
				if err := tx.Create(&items).Error; err != nil {
					return err
				}
				refund.Items = items
				refunds = append(refunds, refund)
			}
			// 未履约的履约记录转为已退款；全部履约完的订阅转为已完成
			newSubStatus := SubCompleted
			if unfulfilledPeriods > 0 {
				newSubStatus = SubRefunded
				if err := tx.Model(&Fulfillment{}).
					Where("subscription_id = ? AND status IN ?", sub.ID, []string{FulPending, FulMissing, FulReissueRequested}).
					Update("status", FulRefunded).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&Subscription{}).Where("id = ?", sub.ID).Update("status", newSubStatus).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 返回该刊物全部退款（含此前已生成的），便于重复调用时展示一致结果
	var all []Refund
	if err := s.db.Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("issue_no") }).
		Where("publication_id = ?", pubID).Order("subscription_id").Find(&all).Error; err != nil {
		return nil, err
	}
	return all, nil
}

// ---------- 查询 ----------

// FulfillmentView 读者页逐期记录
type FulfillmentView struct {
	Fulfillment
	ReaderName string `json:"reader_name"`
}

type SubscriptionSummary struct {
	TotalPeriods       int   `json:"total_periods"`
	FulfilledPeriods   int   `json:"fulfilled_periods"`   // 已到刊+已领取（折算期数）
	PickedPeriods      int   `json:"picked_periods"`      // 已领取
	ArrivedPeriods     int   `json:"arrived_periods"`     // 已到刊待领取
	MissingPeriods     int   `json:"missing_periods"`     // 缺刊+待补发
	PendingPeriods     int   `json:"pending_periods"`     // 待到刊
	RefundedPeriods    int   `json:"refunded_periods"`    // 已退款
	UnfulfilledPeriods int   `json:"unfulfilled_periods"` // 未履约 = 总 - 已履约
	PaidCents          int64 `json:"paid_cents"`
	RefundCents        int64 `json:"refund_cents"`
}

type ReaderView struct {
	Subscription Subscription        `json:"subscription"`
	Summary      SubscriptionSummary `json:"summary"`
	Fulfillments []Fulfillment       `json:"fulfillments"`
	Refund       *Refund             `json:"refund"`
}

func (s *Service) GetReaderView(subID uint) (*ReaderView, error) {
	var sub Subscription
	if err := s.db.Preload("Publication").First(&sub, subID).Error; err != nil {
		return nil, fmt.Errorf("订阅不存在: %w", err)
	}
	var fuls []Fulfillment
	if err := s.db.Where("subscription_id = ?", subID).
		Order("created_at, id").Find(&fuls).Error; err != nil {
		return nil, err
	}
	// 按刊期表顺序排序
	order := s.issueOrder(sub.PublicationID)
	sort.SliceStable(fuls, func(i, j int) bool { return order[fuls[i].IssueNo] < order[fuls[j].IssueNo] })

	sum := SubscriptionSummary{TotalPeriods: sub.TotalPeriods, PaidCents: sub.PaidCents}
	for _, f := range fuls {
		switch f.Status {
		case FulPicked:
			sum.PickedPeriods += f.CountsAs
		case FulArrived:
			sum.ArrivedPeriods += f.CountsAs
		case FulMissing, FulReissueRequested:
			sum.MissingPeriods += f.CountsAs
		case FulPending:
			sum.PendingPeriods += f.CountsAs
		case FulRefunded:
			sum.RefundedPeriods += f.CountsAs
		}
	}
	sum.FulfilledPeriods = sum.PickedPeriods + sum.ArrivedPeriods
	sum.UnfulfilledPeriods = sub.TotalPeriods - sum.FulfilledPeriods

	var refund Refund
	var refundPtr *Refund
	if err := s.db.Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("issue_no") }).
		Where("subscription_id = ?", subID).First(&refund).Error; err == nil {
		refundPtr = &refund
		sum.RefundCents = refund.AmountCents
	}
	return &ReaderView{Subscription: sub, Summary: sum, Fulfillments: fuls, Refund: refundPtr}, nil
}

func (s *Service) issueOrder(pubID uint) map[string]int {
	var sched []IssueSchedule
	s.db.Where("publication_id = ?", pubID).Order("seq").Find(&sched)
	order := make(map[string]int, len(sched))
	for i, it := range sched {
		order[it.IssueNo] = i
	}
	return order
}

func (s *Service) reloadFulfillment(id uint) (*Fulfillment, error) {
	var f Fulfillment
	if err := s.db.First(&f, id).Error; err != nil {
		return nil, err
	}
	return &f, nil
}
