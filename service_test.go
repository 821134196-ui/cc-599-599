package main

import (
	"testing"
)

// 每个测试用独立的内存库 + 种子数据
func setup(t *testing.T) (*Service, Publication, Subscription, Subscription) {
	t.Helper()
	db := openDB("file::memory:")
	svc := NewService(db)
	if err := Seed(db, svc); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	var city Publication
	db.Where("code = ?", "CITY").First(&city)
	var zhang, li Subscription
	db.Where("reader_name = ?", "张三").First(&zhang)
	db.Where("reader_name = ?", "李四").First(&li)
	return svc, city, zhang, li
}

func invOf(t *testing.T, svc *Service, pubID uint, issueNo string) IssueInventory {
	t.Helper()
	var inv IssueInventory
	if err := svc.db.Where("publication_id = ? AND issue_no = ?", pubID, issueNo).First(&inv).Error; err != nil {
		t.Fatalf("库存不存在 %d/%s: %v", pubID, issueNo, err)
	}
	return inv
}

func fulOf(t *testing.T, svc *Service, subID uint, issueNo string) Fulfillment {
	t.Helper()
	var f Fulfillment
	if err := svc.db.Where("subscription_id = ? AND issue_no = ?", subID, issueNo).First(&f).Error; err != nil {
		t.Fatalf("履约记录不存在 %d/%s: %v", subID, issueNo, err)
	}
	return f
}

// 合刊：12 期签约 = 11 条履约记录，2026-07/08 计 2 期，折算合计 = 12
func TestCombinedIssueCounts(t *testing.T) {
	svc, city, zhang, _ := setup(t)

	view, err := svc.GetReaderView(zhang.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Fulfillments) != 11 {
		t.Fatalf("12 期签约应有 11 条履约记录（含 1 个合刊），实际 %d", len(view.Fulfillments))
	}
	total := 0
	var combined *Fulfillment
	for i, f := range view.Fulfillments {
		total += f.CountsAs
		if f.IssueNo == "2026-07/08" {
			combined = &view.Fulfillments[i]
		}
	}
	if total != 12 {
		t.Fatalf("折算期数合计应为 12，实际 %d", total)
	}
	if combined == nil || combined.CountsAs != 2 {
		t.Fatalf("合刊 2026-07/08 应计 2 期: %+v", combined)
	}

	// 合刊到货 2 份 → 两名订户各到刊，履约期数一次 +2
	if _, _, err := svc.RegisterArrival(city.ID, "2026-07/08", 2, "T-ARR-0708", ""); err != nil {
		t.Fatal(err)
	}
	view, _ = svc.GetReaderView(zhang.ID)
	f := fulOf(t, svc, zhang.ID, "2026-07/08")
	if f.Status != FulArrived || f.CountsAs != 2 {
		t.Fatalf("合刊应已到刊且计 2 期: %+v", f)
	}
	// 张三已履约：2026-01(领取) + 2026-02(到刊) + 2026-03(到刊) + 07/08(到刊,2期) = 5 期
	if view.Summary.FulfilledPeriods != 5 {
		t.Fatalf("已履约应为 5 期，实际 %d", view.Summary.FulfilledPeriods)
	}
	inv := invOf(t, svc, city.ID, "2026-07/08")
	if inv.ArrivedQty != 2 || inv.AllocatedQty != 2 || inv.Available() != 0 {
		t.Fatalf("合刊库存对不上: %+v", inv)
	}
}

// 重复登记到货：库存与履约只变动一次
func TestDuplicateArrivalIdempotent(t *testing.T) {
	svc, city, _, _ := setup(t)

	before := invOf(t, svc, city.ID, "2026-01")
	_, dup, err := svc.RegisterArrival(city.ID, "2026-01", 2, "SEED-ARR-CITY-2026-01", "重复")
	if err != nil {
		t.Fatal(err)
	}
	if !dup {
		t.Fatal("应识别为重复到货")
	}
	after := invOf(t, svc, city.ID, "2026-01")
	if before != after {
		t.Fatalf("重复到货导致库存变动: %+v → %+v", before, after)
	}

	// 新键 = 真正的新批次，库存正常增加
	if _, dup, _ := svc.RegisterArrival(city.ID, "2026-01", 3, "T-ARR-01-EXTRA", ""); dup {
		t.Fatal("新幂等键不应判重")
	}
	if inv := invOf(t, svc, city.ID, "2026-01"); inv.ArrivedQty != 5 {
		t.Fatalf("新批次后到货应为 5，实际 %d", inv.ArrivedQty)
	}
}

// 重复出版社回执：库存与履约只变动一次；补发件归原期号，不增加期数
func TestDuplicatePublisherReceipt(t *testing.T) {
	svc, city, _, li := setup(t)

	// 种子中李四 2026-02 已补发到刊；记录当前库存
	before := invOf(t, svc, city.ID, "2026-02")
	var req ReissueRequest
	svc.db.Where("issue_no = ? AND subscription_id = ?", "2026-02", li.ID).First(&req)
	if req.Status != ReissueReceived {
		t.Fatalf("种子数据应已收到补发: %+v", req)
	}

	// 同一回执号再来一次
	if _, dup, err := svc.ReceivePublisherReceipt("SEED-RCPT-2026-02-LI", req.ID); err != nil || !dup {
		t.Fatalf("同回执号应判重: dup=%v err=%v", dup, err)
	}
	// 同一申请换个回执号再来一次
	if _, dup, err := svc.ReceivePublisherReceipt("T-RCPT-NEW", req.ID); err != nil || !dup {
		t.Fatalf("同申请应判重: dup=%v err=%v", dup, err)
	}
	after := invOf(t, svc, city.ID, "2026-02")
	if before != after {
		t.Fatalf("重复回执导致库存变动: %+v → %+v", before, after)
	}

	// 补发件归原期号：履约记录仍 11 条，折算合计仍 12 期
	view, _ := svc.GetReaderView(li.ID)
	if len(view.Fulfillments) != 11 {
		t.Fatalf("补发不应新增履约记录，实际 %d 条", len(view.Fulfillments))
	}
	total := 0
	for _, f := range view.Fulfillments {
		total += f.CountsAs
	}
	if total != 12 {
		t.Fatalf("补发不应增加订阅期数，合计应为 12，实际 %d", total)
	}
	f := fulOf(t, svc, li.ID, "2026-02")
	if f.Status != FulArrived || !f.Reissued {
		t.Fatalf("补发件应归原期号并标记补发: %+v", f)
	}
}

// 重复签收回执：领取计数只变一次
func TestDuplicatePickup(t *testing.T) {
	svc, city, _, li := setup(t)

	f := fulOf(t, svc, li.ID, "2026-02") // 补发已到刊
	if _, dup, err := svc.Pickup(f.ID, "李四"); err != nil || dup {
		t.Fatalf("首次签收失败: dup=%v err=%v", dup, err)
	}
	picked := invOf(t, svc, city.ID, "2026-02").PickedQty
	if picked != 1 {
		t.Fatalf("签收后已领取应为 1，实际 %d", picked)
	}
	// 重复签收
	if _, dup, err := svc.Pickup(f.ID, "李四"); err != nil || !dup {
		t.Fatalf("应识别为重复签收: dup=%v err=%v", dup, err)
	}
	if got := invOf(t, svc, city.ID, "2026-02").PickedQty; got != 1 {
		t.Fatalf("重复签收后已领取应仍为 1，实际 %d", got)
	}
	// 未到刊不能签收
	pending := fulOf(t, svc, li.ID, "2026-04")
	if _, _, err := svc.Pickup(pending.ID, "李四"); err == nil {
		t.Fatal("未到刊不应允许签收")
	}
}

// 停刊退款：按未履约期数计算，明细合计 = 总额，已付款 = 已履约值 + 退款；重复停刊幂等
func TestSuspensionRefund(t *testing.T) {
	svc, city, zhang, li := setup(t)

	// 让李四签收 2026-02（补发件），张三 2026-01 已签收（种子）
	liFeb := fulOf(t, svc, li.ID, "2026-02")
	if _, _, err := svc.Pickup(liFeb.ID, "李四"); err != nil {
		t.Fatal(err)
	}
	// 合刊到货，张三李四均到刊（未领取）
	if _, _, err := svc.RegisterArrival(city.ID, "2026-07/08", 2, "T-ARR-0708", ""); err != nil {
		t.Fatal(err)
	}

	refunds, err := svc.SuspendPublication(city.ID, "测试停刊")
	if err != nil {
		t.Fatal(err)
	}
	if len(refunds) != 2 {
		t.Fatalf("两名订户应各有一笔退款，实际 %d", len(refunds))
	}
	for _, rf := range refunds {
		// 张三：已履约 = 01领 + 02到 + 03到 + 07/08到(2期) = 5 期 → 退 7 期
		// 李四：已履约 = 01到 + 02领 + 03到 + 07/08到(2期) = 5 期 → 退 7 期
		if rf.UnfulfilledPeriods != 7 {
			t.Fatalf("未履约应为 7 期，实际 %d", rf.UnfulfilledPeriods)
		}
		if rf.AmountCents != 7*600 {
			t.Fatalf("退款应为 4200 分，实际 %d", rf.AmountCents)
		}
		var itemSum int64
		itemPeriods := 0
		for _, it := range rf.Items {
			itemSum += it.AmountCents
			itemPeriods += it.CountsAs
		}
		if itemSum != rf.AmountCents {
			t.Fatalf("明细合计 %d ≠ 退款总额 %d", itemSum, rf.AmountCents)
		}
		if itemPeriods != rf.UnfulfilledPeriods {
			t.Fatalf("明细期数 %d ≠ 未履约期数 %d", itemPeriods, rf.UnfulfilledPeriods)
		}
		// 退款明细里不应出现已到刊/已领取的期
		for _, it := range rf.Items {
			if it.IssueNo == "2026-01" || it.IssueNo == "2026-02" || it.IssueNo == "2026-03" || it.IssueNo == "2026-07/08" {
				t.Fatalf("已履约的 %s 不应出现在退款明细", it.IssueNo)
			}
		}
	}

	// 金额守恒：已付款 = 已履约价值 + 退款
	view, _ := svc.GetReaderView(zhang.ID)
	fulfilledValue := int64(view.Summary.FulfilledPeriods) * 600
	if view.Summary.PaidCents != fulfilledValue+view.Summary.RefundCents {
		t.Fatalf("金额不守恒: 已付 %d, 已履约 %d, 退款 %d",
			view.Summary.PaidCents, fulfilledValue, view.Summary.RefundCents)
	}
	// 未履约记录状态应为已退款
	f := fulOf(t, svc, zhang.ID, "2026-04")
	if f.Status != FulRefunded {
		t.Fatalf("未履约记录应转为已退款: %+v", f)
	}

	// 重复停刊：退款不重复生成
	again, err := svc.SuspendPublication(city.ID, "重复停刊")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("重复停刊不应新增退款单，实际 %d 笔", len(again))
	}
	var refundCount int64
	svc.db.Model(&Refund{}).Where("publication_id = ?", city.ID).Count(&refundCount)
	if refundCount != 2 {
		t.Fatalf("退款单总数应为 2，实际 %d", refundCount)
	}
}

// 取刊方式调整：只对未来刊期生效，已到刊/已领取保持原样
func TestPickupMethodChangeOnlyFuture(t *testing.T) {
	svc, _, zhang, _ := setup(t)

	// 张三：2026-01 已领取(self)，2026-02/03 已到刊(self)，其余 pending
	affected, err := svc.ChangePickupMethod(zhang.ID, PickupDelivery)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 8 { // 11 条记录 - 1 领取 - 2 到刊 = 8 条 pending
		t.Fatalf("应更新 8 条未来刊期，实际 %d", affected)
	}
	if got := fulOf(t, svc, zhang.ID, "2026-01").PickupMethod; got != PickupSelf {
		t.Fatalf("已领取记录应保持原取刊方式，实际 %s", got)
	}
	if got := fulOf(t, svc, zhang.ID, "2026-02").PickupMethod; got != PickupSelf {
		t.Fatalf("已到刊记录应保持原取刊方式，实际 %s", got)
	}
	if got := fulOf(t, svc, zhang.ID, "2026-04").PickupMethod; got != PickupDelivery {
		t.Fatalf("未来刊期应改为新方式，实际 %s", got)
	}
}

// 缺刊→补发全流程：到货不足自动缺刊，补发归原期号，不重复计交付
func TestMissingAndReissueFlow(t *testing.T) {
	svc, city, _, _ := setup(t)

	// 种子中 2026-02 到货 1 份：张三到刊，李四缺刊
	inv := invOf(t, svc, city.ID, "2026-02")
	if inv.ArrivedQty != 2 || inv.ReissueQty != 1 { // 1 正常 + 1 补发
		t.Fatalf("2026-02 库存对不上: %+v", inv)
	}
	// 交付计数：该期只有 2 份交付（张三正常件 + 李四补发件），补发不重复计
	var delivered int64
	svc.db.Model(&Fulfillment{}).
		Where("publication_id = ? AND issue_no = ? AND status IN ?", city.ID, "2026-02", []string{FulArrived, FulPicked}).
		Select("COALESCE(SUM(copies),0)").Scan(&delivered)
	if delivered != 2 {
		t.Fatalf("2026-02 交付应为 2 份（补发不重复计），实际 %d", delivered)
	}
	// 重复申请补发：返回原申请，不新建
	liFeb := fulOf(t, svc, mustSubID(t, svc, "李四"), "2026-02")
	req1, dup1, err := svc.RequestReissue(liFeb.ID)
	if err != nil || !dup1 {
		t.Fatalf("重复申请补发应返回原申请: dup=%v err=%v", dup1, err)
	}
	var reqCount int64
	svc.db.Model(&ReissueRequest{}).Where("fulfillment_id = ?", liFeb.ID).Count(&reqCount)
	if reqCount != 1 || req1.FulfillmentID != liFeb.ID {
		t.Fatalf("同一履约记录应只有一条补发申请，实际 %d 条", reqCount)
	}
}

func mustSubID(t *testing.T, svc *Service, reader string) uint {
	t.Helper()
	var sub Subscription
	if err := svc.db.Where("reader_name = ?", reader).First(&sub).Error; err != nil {
		t.Fatalf("订户不存在 %s: %v", reader, err)
	}
	return sub.ID
}
