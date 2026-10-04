package main

import (
	"log"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}

func autoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&Reader{},
		&Magazine{},
		&Subscription{},
		&FulfillmentItem{},
		&StockArrival{},
		&ReissueRequest{},
		&Refund{},
	)
}

// seed 首次启动写入演示数据：含 7/8 合刊折算规则的月刊。
func seed(st *Store) {
	var n int64
	st.db.Model(&Magazine{}).Count(&n)
	if n > 0 {
		return
	}
	mag, err := st.CreateMagazine("万象月刊", "WXYK", 1500, doubleRules{
		"2026-07-08": 2, // 2026 年第 7/8 期合刊，按 2 期折算
	})
	if err != nil {
		log.Printf("seed 期刊失败: %v", err)
		return
	}
	r1, _ := st.CreateReader("张晓明", "13800000001")
	r2, _ := st.CreateReader("李雅", "13800000002")

	// 全年订阅：12 个折算期数，遇到 7/8 合刊 → 11 个期号记录
	_, err = st.CreateSubscription(CreateSubInput{
		ReaderID:     r1.ID,
		MagazineID:   mag.ID,
		StartIssueNo: "2026-01",
		TotalIssues:  12,
		PickupMode:   PickupToStore,
	})
	if err != nil {
		log.Printf("seed 订阅1失败: %v", err)
	}
	// 半年订阅：6 期
	_, err = st.CreateSubscription(CreateSubInput{
		ReaderID:     r2.ID,
		MagazineID:   mag.ID,
		StartIssueNo: "2026-01",
		TotalIssues:  6,
		PickupMode:   PickupMail,
	})
	if err != nil {
		log.Printf("seed 订阅2失败: %v", err)
	}
	log.Println("已写入演示数据：万象月刊（7/8合刊）、2 位读者、全年/半年订阅各一份")
}
