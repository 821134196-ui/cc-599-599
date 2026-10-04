package main

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// ---------- 取刊方式 ----------

type PickupMode string

const (
	PickupToStore PickupMode = "store"    // 到店自取（默认）
	PickupMail    PickupMode = "mail"     // 邮寄
	PickupHome    PickupMode = "delivery" // 送刊上门
)

// pickupChange 记录一次取刊方式调整，仅对 changeIssueNo 之后（不含该期）
// 尚未到货的未来刊期生效；已完成的履约记录保持原样。
type pickupChange struct {
	FromMode      PickupMode `json:"from_mode" gorm:"type:text"`
	ToMode        PickupMode `json:"to_mode" gorm:"type:text"`
	ChangeIssueNo string     `json:"change_issue_no"` // 当时最新一期已到货期号
	ChangedAt     time.Time  `json:"changed_at"`
}

type pickupHistory []pickupChange

func (h pickupHistory) Value() (driver.Value, error) { return json.Marshal(h) }
func (h *pickupHistory) Scan(v any) error {
	if v == nil {
		return nil
	}
	b, ok := v.([]byte)
	if !ok {
		s, ok2 := v.(string)
		if !ok2 {
			return errors.New("pickupHistory: unsupported scan type")
		}
		b = []byte(s)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, h)
}

// ---------- 读者 / 期刊 ----------

type Reader struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	CreatedAt time.Time `json:"created_at"`
}

// Magazine 期刊（含签约时确定的合刊折算规则）。
type Magazine struct {
	ID         uint   `json:"id" gorm:"primaryKey"`
	Title      string `json:"title"`
	Code       string `json:"code" gorm:"uniqueIndex"`
	UnitPrice  int64  `json:"unit_price"` // 单期定价（分）
	// DoubleIssueRules 合刊折算规则，签约时固定，例如
	// {"2026-07-08": 2} 表示 2026 年第 7/8 期合刊，按 2 期折算。
	Rules doubleRules `json:"double_issue_rules" gorm:"type:text"`
}

type doubleRules map[string]int

func (r doubleRules) Value() (driver.Value, error) { return json.Marshal(r) }
func (r *doubleRules) Scan(v any) error {
	if v == nil {
		return nil
	}
	b, ok := v.([]byte)
	if !ok {
		s, ok2 := v.(string)
		if !ok2 {
			return errors.New("doubleRules: unsupported scan type")
		}
		b = []byte(s)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, &r)
}

// ---------- 订阅 ----------

// Subscription 一份连续刊期订阅。期数与期号分离：
// totalIssues 为折算后应付期数（合刊 credit=2），
// FulfillmentItems 为逐期号的履约明细（合刊只有一条期号记录）。
type Subscription struct {
	ID           uint          `json:"id" gorm:"primaryKey"`
	ReaderID     uint          `json:"reader_id"`
	Reader       Reader        `json:"-" gorm:"foreignKey:ReaderID"`
	MagazineID   uint          `json:"magazine_id"`
	Magazine     Magazine      `json:"-" gorm:"foreignKey:MagazineID"`
	StartIssueNo string        `json:"start_issue_no"` // 起始期号，如 2026-01
	TotalIssues  int           `json:"total_issues"`   // 折算后总期数
	UnitPrice    int64         `json:"unit_price"`     // 签约时单期价（分/期）
	PickupMode   PickupMode    `json:"pickup_mode" gorm:"type:text"`
	PickupHist   pickupHistory `json:"pickup_history" gorm:"type:text"`
	Status       string        `json:"status"` // active / stopped / finished
	CreatedAt    time.Time     `json:"created_at"`

	Items []FulfillmentItem `json:"-" gorm:"foreignKey:SubscriptionID"`
}

// 履约状态（按单个期号）
const (
	ItemPending   = "pending"   // 尚未到货
	ItemArrived   = "arrived"   // 已到货分配，待领取
	ItemReceived  = "received"  // 读者已签收
	ItemMissing   = "missing"   // 确认缺刊，已申请/待补发
	ItemReissuing = "reissuing" // 出版社补发中
)

// FulfillmentItem 某份订阅下某一个期号的履约记录。
type FulfillmentItem struct {
	ID             uint       `json:"id" gorm:"primaryKey"`
	SubscriptionID uint       `json:"subscription_id" gorm:"uniqueIndex:idx_sub_issue"`
	IssueNo        string     `json:"issue_no" gorm:"uniqueIndex:idx_sub_issue"`
	DisplayLabel   string     `json:"display_label"` // 例如 2026年第7-8期（合刊）
	Credit         int        `json:"credit"`        // 该期号折算期数（合刊=2）
	Sequence       int        `json:"sequence"`      // 订阅内顺序（从1开始）
	Status         string     `json:"status" gorm:"index"`
	EffectiveMode  PickupMode `json:"effective_mode" gorm:"type:text"` // 按取刊方式调整规则解析出的生效方式

	// 关联的到货批次（正常到货或补发件）
	ArrivalID *uint          `json:"arrival_id,omitempty"`
	Arrival   *StockArrival  `json:"-" gorm:"foreignKey:ArrivalID"`
	IsReissue bool           `json:"is_reissue"` // 该期最终由补发件履约

	ReceivedAt *time.Time `json:"received_at,omitempty"`
	// ReceiveReceipt 签收回执号，唯一索引保证重复回执不会重复计数。
	ReceiveReceipt *string    `json:"receive_receipt,omitempty" gorm:"uniqueIndex"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ---------- 库存与到货 ----------

const (
	ArrivalNormal  = "normal"  // 正常到货
	ArrivalReissue = "reissue" // 出版社补发件
)

// StockArrival 一次到货批次（同一期号可有正常批次与补发批次）。
type StockArrival struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	MagazineID uint      `json:"magazine_id"`
	Magazine   Magazine `json:"-" gorm:"foreignKey:MagazineID"`
	IssueNo    string    `json:"issue_no" gorm:"index"`
	Kind       string    `json:"kind"` // normal / reissue
	Quantity   int       `json:"quantity"`
	Allocated  int       `json:"allocated"` // 已分配给订户的数量
	// PublisherReceipt 出版社发货回执号，唯一索引：重复回执重复登记只能生效一次。
	PublisherReceipt string    `json:"publisher_receipt" gorm:"uniqueIndex"`
	Note             string    `json:"note"`
	ArrivedAt        time.Time `json:"arrived_at"`
}

// ---------- 缺刊补发 ----------

const (
	ReissueRequested = "requested" // 已向出版社申请
	ReissueAccepted  = "accepted"  // 出版社受理（模拟回执）
	ReissueShipped   = "shipped"   // 补发件已到店（自动入库）
	ReissueRejected  = "rejected"
)

// ReissueRequest 缺刊补发申请。出版社回执在本地模拟。
type ReissueRequest struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	MagazineID     uint      `json:"magazine_id"`
	IssueNo        string    `json:"issue_no"`
	SubscriptionID uint      `json:"subscription_id"` // 触发申请的订户（仅作溯源）
	ItemID         uint      `json:"item_id"`
	Status         string    `json:"status"`
	// RequestKey 幂等键：同一期号的重复申请/重复出版社回执只处理一次。
	RequestKey  string    `json:"request_key" gorm:"uniqueIndex"`
	PublisherNo string    `json:"publisher_no"` // 模拟出版社受理单号
	RejectReason string   `json:"reject_reason"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ---------- 停刊退款 ----------

const RefundStatus = "refunded"

// Refund 停刊退款，金额依据尚未履约（未签收）期数计算，明细逐期号列出。
type Refund struct {
	ID             uint           `json:"id" gorm:"primaryKey"`
	SubscriptionID uint           `json:"subscription_id" gorm:"uniqueIndex"`
	UnfulfilledN   int            `json:"unfulfilled_count"` // 未履约期数（折算）
	PerIssuePrice  int64          `json:"per_issue_price"`
	Amount         int64          `json:"amount"` // 分
	Details        refundDetails  `json:"details" gorm:"type:text"`
	CreatedAt      time.Time      `json:"created_at"`
}

type refundLine struct {
	Sequence int    `json:"sequence"`
	IssueNo  string `json:"issue_no"`
	Label    string `json:"label"`
	Credit   int    `json:"credit"`
	Status   string `json:"status"`
	Amount   int64  `json:"amount"`
}

type refundDetails []refundLine

func (d refundDetails) Value() (driver.Value, error) { return json.Marshal(d) }
func (d *refundDetails) Scan(v any) error {
	if v == nil {
		return nil
	}
	b, ok := v.([]byte)
	if !ok {
		s, ok2 := v.(string)
		if !ok2 {
			return errors.New("refundDetails: unsupported scan type")
		}
		b = []byte(s)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, d)
}
