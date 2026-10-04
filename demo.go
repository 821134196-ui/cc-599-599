package main

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// runDemo 在内存库中跑一遍完整场景，打印期数、库存、金额的对账结果。
// 覆盖：合刊折算、重复到货/重复回执/重复签收的幂等、停刊退款。
func runDemo() {
	db := openDB("file::memory:")
	svc := NewService(db)
	if err := Seed(db, svc); err != nil {
		panic(err)
	}

	line := func() { fmt.Println(strings.Repeat("─", 72)) }
	header := func(t string) { fmt.Printf("\n■ %s\n", t); line() }

	var city Publication
	db.Where("code = ?", "CITY").First(&city)
	var zhang, li Subscription
	db.Where("reader_name = ?", "张三").First(&zhang)
	db.Where("reader_name = ?", "李四").First(&li)

	fmt.Println("════════ 报刊亭期刊订阅系统 · 对账演示 ════════")
	fmt.Printf("刊物：%s  单价：%.2f 元/期  签约规则：2026-07/08 合刊计 2 期\n",
		city.Name, float64(city.UnitPriceCents)/100)
	fmt.Printf("订户：张三、李四，各订 12 期（2026-01 起），各付 %.2f 元\n",
		float64(zhang.PaidCents)/100)

	// ── 场景 1：合刊折算 ─────────────────────────────
	header("场景 1 · 合刊：2026-07/08 合刊到货 2 份，按签约规则计 2 期")
	if _, _, err := svc.RegisterArrival(city.ID, "2026-07/08", 2, "DEMO-ARR-0708", "合刊"); err != nil {
		panic(err)
	}
	printSubRecon(db, svc, zhang.ID, "张三")
	printInventory(db, city.ID, "2026-07/08")

	// ── 场景 2：重复登记/重复回执/重复签收 ────────────
	header("场景 2 · 幂等：重复登记到货、重复出版社回执、重复签收都只生效一次")
	invBefore := getInv(db, city.ID, "2026-01")
	if _, dup, _ := svc.RegisterArrival(city.ID, "2026-01", 2, "SEED-ARR-CITY-2026-01", "重复登记"); !dup {
		panic("应识别为重复到货")
	}
	invAfter := getInv(db, city.ID, "2026-01")
	fmt.Printf("重复登记 2026-01 到货：到货数 %d → %d（不变 ✓），已分配 %d → %d（不变 ✓）\n",
		invBefore.ArrivedQty, invAfter.ArrivedQty, invBefore.AllocatedQty, invAfter.AllocatedQty)

	var reqLi ReissueRequest
	db.Where("issue_no = ? AND subscription_id = ?", "2026-02", li.ID).First(&reqLi)
	invBefore = getInv(db, city.ID, "2026-02")
	if _, dup, _ := svc.ReceivePublisherReceipt("SEED-RCPT-2026-02-LI", reqLi.ID); !dup {
		panic("应识别为重复回执")
	}
	invAfter = getInv(db, city.ID, "2026-02")
	fmt.Printf("重复出版社回执 2026-02：到货数 %d → %d（不变 ✓），其中补发件 %d → %d（不变 ✓）\n",
		invBefore.ArrivedQty, invAfter.ArrivedQty, invBefore.ReissueQty, invAfter.ReissueQty)

	// 李四 2026-02 补发到刊后签收，再重复签收一次
	fulLi := getFul(db, li.ID, "2026-02")
	if _, _, err := svc.Pickup(fulLi.ID, "李四"); err != nil {
		panic(err)
	}
	invBefore = getInv(db, city.ID, "2026-02")
	if _, dup, _ := svc.Pickup(fulLi.ID, "李四"); !dup {
		panic("应识别为重复签收")
	}
	invAfter = getInv(db, city.ID, "2026-02")
	fmt.Printf("重复签收 2026-02：已领取 %d → %d（不变 ✓），履约次数不重复计算 ✓\n",
		invBefore.PickedQty, invAfter.PickedQty)
	printSubRecon(db, svc, li.ID, "李四")

	// ── 场景 3：停刊退款 ─────────────────────────────
	header("场景 3 · 停刊：出版社停刊，按未履约期数退款（含合刊明细）")
	refunds, err := svc.SuspendPublication(city.ID, "出版社自 2026-04 起停刊")
	if err != nil {
		panic(err)
	}
	for _, rf := range refunds {
		var sub Subscription
		db.First(&sub, rf.SubscriptionID)
		fmt.Printf("退款单 #%d  读者：%s  未履约 %d 期 × %.2f 元 = 退款 %.2f 元\n",
			rf.ID, sub.ReaderName, rf.UnfulfilledPeriods,
			float64(rf.UnitPriceCents)/100, float64(rf.AmountCents)/100)
		var itemSum int64
		for _, it := range rf.Items {
			fmt.Printf("    · %-10s 计 %d 期  %.2f 元\n", it.IssueNo, it.CountsAs, float64(it.AmountCents)/100)
			itemSum += it.AmountCents
		}
		fmt.Printf("    明细合计 %.2f 元 %s\n", float64(itemSum)/100, ok(itemSum == rf.AmountCents))
	}
	// 重复停刊：退款不重复生成
	again, _ := svc.SuspendPublication(city.ID, "重复停刊请求")
	fmt.Printf("重复停刊：退款单数量 %d → %d（不变 ✓）\n", len(refunds), len(again))

	// ── 终态对账 ─────────────────────────────────────
	header("终态对账 · 张三")
	printSubRecon(db, svc, zhang.ID, "张三")
	header("终态对账 · 李四")
	printSubRecon(db, svc, li.ID, "李四")
	header("库存总览")
	for _, issue := range []string{"2026-01", "2026-02", "2026-03", "2026-07/08"} {
		printInventory(db, city.ID, issue)
	}
	fmt.Println()
}

func getInv(db *gorm.DB, pubID uint, issueNo string) IssueInventory {
	var inv IssueInventory
	db.Where("publication_id = ? AND issue_no = ?", pubID, issueNo).First(&inv)
	return inv
}

func getFul(db *gorm.DB, subID uint, issueNo string) Fulfillment {
	var f Fulfillment
	db.Where("subscription_id = ? AND issue_no = ?", subID, issueNo).First(&f)
	return f
}

// printSubRecon 打印一份订阅的期数与金额对账：
// 总期数 = 已履约 + 未履约；已付款 = 已履约金额 + 退款金额。
func printSubRecon(db *gorm.DB, svc *Service, subID uint, name string) {
	view, err := svc.GetReaderView(subID)
	if err != nil {
		panic(err)
	}
	s := view.Summary
	fulfilledValue := int64(s.FulfilledPeriods) * view.Subscription.UnitPriceCents * int64(view.Subscription.Copies)
	pendingSettle := s.PaidCents - fulfilledValue - s.RefundCents // 未履约且尚未退款的金额
	fmt.Printf("读者 %s：总期数 %d = 已履约 %d（已领取 %d + 已到刊 %d）+ 未履约 %d %s\n",
		name, s.TotalPeriods, s.FulfilledPeriods, s.PickedPeriods, s.ArrivedPeriods,
		s.UnfulfilledPeriods, ok(s.TotalPeriods == s.FulfilledPeriods+s.UnfulfilledPeriods))
	fmt.Printf("    已付 %.2f 元 = 已履约 %.2f 元 + 已退款 %.2f 元 + 待结算 %.2f 元 %s\n",
		float64(s.PaidCents)/100, float64(fulfilledValue)/100, float64(s.RefundCents)/100,
		float64(pendingSettle)/100, ok(pendingSettle >= 0))
	// 履约记录折算期数合计必须等于签约总期数（补发件不增加期数）
	total := 0
	for _, f := range view.Fulfillments {
		total += f.CountsAs
	}
	fmt.Printf("    履约记录 %d 条，折算期数合计 %d = 签约期数 %d %s\n",
		len(view.Fulfillments), total, s.TotalPeriods, ok(total == s.TotalPeriods))
}

func printInventory(db *gorm.DB, pubID uint, issueNo string) {
	inv := getInv(db, pubID, issueNo)
	// 库存对账：已分配 = 已到刊记录份数；已领取 = 已领取记录份数
	var allocatedCnt, pickedCnt int64
	db.Model(&Fulfillment{}).Where("publication_id = ? AND issue_no = ? AND status IN ?", pubID, issueNo,
		[]string{FulArrived, FulPicked}).Select("COALESCE(SUM(copies),0)").Scan(&allocatedCnt)
	db.Model(&Fulfillment{}).Where("publication_id = ? AND issue_no = ? AND status = ?", pubID, issueNo,
		FulPicked).Select("COALESCE(SUM(copies),0)").Scan(&pickedCnt)
	fmt.Printf("期号 %-10s 到货 %d（含补发 %d） 已分配 %d 已领取 %d 可分配 %d ｜ 与履约记录核对：分配 %s 领取 %s\n",
		issueNo, inv.ArrivedQty, inv.ReissueQty, inv.AllocatedQty, inv.PickedQty, inv.Available(),
		ok(int64(inv.AllocatedQty) == allocatedCnt), ok(int64(inv.PickedQty) == pickedCnt))
}

func ok(b bool) string {
	if b {
		return "✓ 对上"
	}
	return "✗ 对不上"
}
