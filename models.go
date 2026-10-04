package main

import "time"

// ---------- 状态常量 ----------

// 履约记录状态
const (
	FulPending          = "pending"           // 待到刊
	FulArrived          = "arrived"           // 已到刊（已分配，待领取）
	FulPicked           = "picked"            // 已领取
	FulMissing          = "missing"           // 缺刊
	FulReissueRequested = "reissue_requested" // 待补发（已向出版社申请）
	FulRefunded         = "refunded"          // 已退款（停刊）
)

// 订阅状态
const (
	SubActive    = "active"
	SubCompleted = "completed"
	SubRefunded  = "refunded"
)

// 刊物状态
const (
	PubActive    = "active"
	PubSuspended = "suspended"
)

// 补发申请状态
const (
	ReissueRequested = "requested"
	ReissueReceived  = "received"
)

// 取刊方式
const (
	PickupSelf     = "self"     // 到店自取
	PickupDelivery = "delivery" // 配送上门
)

// ---------- 模型 ----------

// Publication 刊物
type Publication struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	Name           string     `json:"name"`
	Code           string     `gorm:"uniqueIndex" json:"code"`
	UnitPriceCents int64      `json:"unit_price_cents"` // 每期单价（分）
	Status         string     `json:"status"`           // active / suspended
	SuspendReason  string     `json:"suspend_reason"`
	SuspendedAt    *time.Time `json:"suspended_at"`
}

// IssueSchedule 刊期表：签约时确定的刊期顺序与合刊折算规则
type IssueSchedule struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	PublicationID uint   `gorm:"uniqueIndex:idx_sched_pub_issue" json:"publication_id"`
	IssueNo       string `gorm:"uniqueIndex:idx_sched_pub_issue" json:"issue_no"`
	Seq           int    `json:"seq"`       // 刊期顺序
	CountsAs      int    `json:"counts_as"` // 折算期数：普通刊=1，合刊=2
}

// Subscription 订阅：读者订连续刊期
type Subscription struct {
	ID             uint        `gorm:"primaryKey" json:"id"`
	ReaderName     string      `json:"reader_name"`
	Phone          string      `json:"phone"`
	PublicationID  uint        `json:"publication_id"`
	Publication    Publication `gorm:"foreignKey:PublicationID" json:"publication"`
	StartIssueNo   string      `json:"start_issue_no"`
	TotalPeriods   int         `json:"total_periods"`    // 签约总期数（按折算规则计）
	Copies         int         `json:"copies"`           // 每期份数
	UnitPriceCents int64       `json:"unit_price_cents"` // 签约单价快照（分/期/份）
	PaidCents      int64       `json:"paid_cents"`       // 已收订款 = 期数×单价×份数
	PickupMethod   string      `json:"pickup_method"`    // 当前取刊方式（仅对未来刊期生效）
	Status         string      `json:"status"`
	CreatedAt      time.Time   `json:"created_at"`
}

// Fulfillment 履约记录：一份订阅 × 一个刊期，逐期跟踪
type Fulfillment struct {
	ID             uint         `gorm:"primaryKey" json:"id"`
	SubscriptionID uint         `gorm:"uniqueIndex:idx_ful_sub_issue" json:"subscription_id"`
	Subscription   Subscription `gorm:"foreignKey:SubscriptionID" json:"subscription"`
	PublicationID  uint         `json:"publication_id"`
	IssueNo        string       `gorm:"uniqueIndex:idx_ful_sub_issue" json:"issue_no"`
	CountsAs       int          `json:"counts_as"` // 折算期数快照
	Copies         int          `json:"copies"`
	PickupMethod   string       `json:"pickup_method"` // 取刊方式快照：已完成记录保持原样
	Status         string       `json:"status"`
	Reissued       bool         `json:"reissued"` // 本次到刊是否为补发件
	ArrivedAt      *time.Time   `json:"arrived_at"`
	PickedAt       *time.Time   `json:"picked_at"`
	PickedBy       string       `json:"picked_by"` // 签收人
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// IssueInventory 刊期库存：某刊物某一期的到刊/分配/领取计数
type IssueInventory struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	PublicationID uint   `gorm:"uniqueIndex:idx_inv_pub_issue" json:"publication_id"`
	IssueNo       string `gorm:"uniqueIndex:idx_inv_pub_issue" json:"issue_no"`
	ArrivedQty    int    `json:"arrived_qty"`   // 累计到货（含补发件）
	ReissueQty    int    `json:"reissue_qty"`   // 其中补发件数量
	AllocatedQty  int    `json:"allocated_qty"` // 已分配给订户
	PickedQty     int    `json:"picked_qty"`    // 已被领取
}

// Available 可分配库存
func (inv IssueInventory) Available() int { return inv.ArrivedQty - inv.AllocatedQty }

// ArrivalBatch 到货批次：幂等键保证重复登记只生效一次
type ArrivalBatch struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	PublicationID  uint      `json:"publication_id"`
	IssueNo        string    `json:"issue_no"`
	Quantity       int       `json:"quantity"`
	IdempotencyKey string    `gorm:"uniqueIndex" json:"idempotency_key"`
	Note           string    `json:"note"`
	CreatedAt      time.Time `json:"created_at"`
}

// ReissueRequest 补发申请：一个履约记录最多一条
type ReissueRequest struct {
	ID             uint        `gorm:"primaryKey" json:"id"`
	FulfillmentID  uint        `gorm:"uniqueIndex" json:"fulfillment_id"` // 同一刊期同一订户只补发一次
	Fulfillment    Fulfillment `gorm:"foreignKey:FulfillmentID" json:"fulfillment"`
	SubscriptionID uint        `json:"subscription_id"`
	PublicationID  uint        `json:"publication_id"`
	IssueNo        string      `json:"issue_no"`
	Quantity       int         `json:"quantity"`
	Status         string      `json:"status"`                        // requested / received
	ReceiptNo      *string     `gorm:"uniqueIndex" json:"receipt_no"` // 出版社回执号，唯一
	RequestedAt    time.Time   `json:"requested_at"`
	ReceivedAt     *time.Time  `json:"received_at"`
}

// Refund 退款：一份订阅最多一笔
type Refund struct {
	ID                 uint         `gorm:"primaryKey" json:"id"`
	SubscriptionID     uint         `gorm:"uniqueIndex" json:"subscription_id"`
	PublicationID      uint         `json:"publication_id"`
	Reason             string       `json:"reason"`
	UnfulfilledPeriods int          `json:"unfulfilled_periods"`
	Copies             int          `json:"copies"`
	UnitPriceCents     int64        `json:"unit_price_cents"`
	AmountCents        int64        `json:"amount_cents"`
	CreatedAt          time.Time    `json:"created_at"`
	Items              []RefundItem `gorm:"foreignKey:RefundID" json:"items"`
}

// RefundItem 退款明细：按未履约刊期逐期列出
type RefundItem struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	RefundID    uint   `gorm:"index" json:"refund_id"`
	IssueNo     string `json:"issue_no"`
	CountsAs    int    `json:"counts_as"`
	AmountCents int64  `json:"amount_cents"` // = 折算期数 × 单价 × 份数
}
