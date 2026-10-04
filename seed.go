package main

import (
	"fmt"

	"gorm.io/gorm"
)

// Seed 首次启动时写入演示数据（幂等：已有数据则跳过）。
// 《都市生活月刊》：月刊，签约折算规则为 7-8 月合刊计 2 期，
// 全年 11 个物理刊期 = 12 个签约期数。
func Seed(db *gorm.DB, svc *Service) error {
	var count int64
	db.Model(&Publication{}).Count(&count)
	if count > 0 {
		return nil
	}

	city := Publication{Name: "都市生活月刊", Code: "CITY", UnitPriceCents: 600, Status: PubActive}
	sci := Publication{Name: "少年科学", Code: "SCI", UnitPriceCents: 800, Status: PubActive}
	if err := db.Create(&city).Error; err != nil {
		return err
	}
	if err := db.Create(&sci).Error; err != nil {
		return err
	}

	// 都市生活月刊刊期表：2026-07/08 为合刊，计 2 期
	cityIssues := []IssueSchedule{
		{PublicationID: city.ID, IssueNo: "2026-01", Seq: 1, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-02", Seq: 2, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-03", Seq: 3, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-04", Seq: 4, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-05", Seq: 5, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-06", Seq: 6, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-07/08", Seq: 7, CountsAs: 2}, // 合刊计 2 期
		{PublicationID: city.ID, IssueNo: "2026-09", Seq: 8, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-10", Seq: 9, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-11", Seq: 10, CountsAs: 1},
		{PublicationID: city.ID, IssueNo: "2026-12", Seq: 11, CountsAs: 1},
	}
	if err := db.Create(&cityIssues).Error; err != nil {
		return err
	}
	// 少年科学刊期表：普通月刊
	for i := 1; i <= 12; i++ {
		if err := db.Create(&IssueSchedule{
			PublicationID: sci.ID, IssueNo: fmt.Sprintf("2026-%02d", i), Seq: i, CountsAs: 1,
		}).Error; err != nil {
			return err
		}
	}

	// 读者订阅
	zhang, err := svc.CreateSubscription("张三", "13800000001", city.ID, "2026-01", 12, 1, PickupSelf)
	if err != nil {
		return err
	}
	li, err := svc.CreateSubscription("李四", "13800000002", city.ID, "2026-01", 12, 1, PickupDelivery)
	if err != nil {
		return err
	}
	if _, err := svc.CreateSubscription("王五", "13800000003", sci.ID, "2026-03", 6, 1, PickupSelf); err != nil {
		return err
	}

	// 初始业务流水，让页面打开即有数据：
	// 1) 2026-01 到货 2 份 → 张三、李四均到刊；张三签收
	if _, _, err := svc.RegisterArrival(city.ID, "2026-01", 2, "SEED-ARR-CITY-2026-01", "首期到货"); err != nil {
		return err
	}
	if _, _, err := svc.Pickup(fulID(db, zhang.ID, "2026-01"), "张三"); err != nil {
		return err
	}
	// 2) 2026-02 只到 1 份 → 张三到刊，李四缺刊 → 申请补发 → 出版社回执 → 李四补发到刊
	if _, _, err := svc.RegisterArrival(city.ID, "2026-02", 1, "SEED-ARR-CITY-2026-02", "到货不足"); err != nil {
		return err
	}
	req, _, err := svc.RequestReissue(fulID(db, li.ID, "2026-02"))
	if err != nil {
		return err
	}
	if _, _, err := svc.ReceivePublisherReceipt("SEED-RCPT-2026-02-LI", req.ID); err != nil {
		return err
	}
	// 3) 2026-03 到货 2 份
	if _, _, err := svc.RegisterArrival(city.ID, "2026-03", 2, "SEED-ARR-CITY-2026-03", ""); err != nil {
		return err
	}
	return nil
}

func fulID(db *gorm.DB, subID uint, issueNo string) uint {
	var f Fulfillment
	db.Where("subscription_id = ? AND issue_no = ?", subID, issueNo).First(&f)
	return f.ID
}
