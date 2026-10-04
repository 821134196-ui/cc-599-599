package main

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := autoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewStore(db)
}

// 构造：万象月刊，单期 15.00 元（1500 分），2026 年第 7/8 期合刊按 2 期折算。
func fixtureYearSub(t *testing.T, st *Store) (*Magazine, *Reader, *Subscription) {
	t.Helper()
	mag, err := st.CreateMagazine("万象月刊", "WXYK", 1500, doubleRules{
		"2026-07-08": 2,
	})
	if err != nil {
		t.Fatalf("magazine: %v", err)
	}
	rd, err := st.CreateReader("张晓明", "13800000001")
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	sub, err := st.CreateSubscription(CreateSubInput{
		ReaderID:     rd.ID,
		MagazineID:   mag.ID,
		StartIssueNo: "2026-01",
		TotalIssues:  12,
		PickupMode:   PickupToStore,
	})
	if err != nil {
		t.Fatalf("subscription: %v", err)
	}
	return mag, rd, sub
}

// ---------- 场景 1：合刊折算期数 ----------

func TestMergedIssueCredits(t *testing.T) {
	st := newTestStore(t)
	_, _, sub := fixtureYearSub(t, st)

	// 全年 12 个折算期数；7/8 合刊只有一条期号记录 → 11 条期号。
	var items []FulfillmentItem
	st.db.Where("subscription_id = ?", sub.ID).Order("sequence").Find(&items)
	if len(items) != 11 {
		t.Fatalf("期望 11 条期号记录（合刊合并为一条），实际 %d", len(items))
	}
	totalCredit := 0
	var merged *FulfillmentItem
	for i := range items {
		totalCredit += items[i].Credit
		if items[i].IssueNo == "2026-07-08" {
			merged = &items[i]
		}
	}
	if totalCredit != 12 {
		t.Fatalf("折算总期数应为 12，实际 %d", totalCredit)
	}
	if merged == nil || merged.Credit != 2 {
		t.Fatalf("合刊期号应存在且 credit=2，得到 %+v", merged)
	}

	// 逐期正常到货（含合刊批次），数量都只登记 1 份。
	issues := []string{"2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07-08",
		"2026-09", "2026-10", "2026-11", "2026-12"}
	for i, no := range issues {
		_, dup, err := st.RegisterArrival(ArrivalInput{
			MagazineID:       sub.MagazineID,
			IssueNo:          no,
			Quantity:         1,
			PublisherReceipt: "R-" + no,
		})
		if err != nil || dup {
			t.Fatalf("到货 %s 失败: dup=%v err=%v", no, dup, err)
		}
		_ = i
	}
	// 全部签收
	for _, no := range issues {
		_, dup, err := st.Receive(ReceiveInput{SubscriptionID: sub.ID, IssueNo: no, Receipt: "RC-" + no})
		if err != nil || dup {
			t.Fatalf("签收 %s 失败: dup=%v err=%v", no, dup, err)
		}
	}
	d, _ := st.GetSubscription(sub.ID)
	if d.CreditsGot != 12 {
		t.Fatalf("合刊全部签收后已履约折算期数应为 12，实际 %d", d.CreditsGot)
	}
	// 合刊签收一次只产生一条已签收记录，不能算成交付两次
	var mergedReceived int64
	st.db.Model(&FulfillmentItem{}).
		Where("subscription_id = ? AND issue_no = ? AND status = ?", sub.ID, "2026-07-08", ItemReceived).
		Count(&mergedReceived)
	if mergedReceived != 1 {
		t.Fatalf("合刊签收记录应为 1 条，实际 %d", mergedReceived)
	}
}

// ---------- 场景 2：重复登记到货 / 重复签收 / 重复出版社回执，均只生效一次 ----------

func TestDuplicateReceiptsIdempotent(t *testing.T) {
	st := newTestStore(t)
	mag, _, sub := fixtureYearSub(t, st)

	// 第一次到货：1 份，自动分配给唯一订户 → allocated=1
	a1, dup, err := st.RegisterArrival(ArrivalInput{
		MagazineID: mag.ID, IssueNo: "2026-01", Quantity: 1, PublisherReceipt: "R-DUP-01",
	})
	if err != nil || dup {
		t.Fatalf("首次到货失败: %v dup=%v", err, dup)
	}
	if a1.Allocated != 1 {
		t.Fatalf("首次到货应自动分配 1 份，实际 allocated=%d", a1.Allocated)
	}

	// 重复登记同一出版社回执：库存、分配均不得二次变化
	a2, dup, err := st.RegisterArrival(ArrivalInput{
		MagazineID: mag.ID, IssueNo: "2026-01", Quantity: 1, PublisherReceipt: "R-DUP-01",
	})
	if err != nil || !dup || a2.ID != a1.ID {
		t.Fatalf("重复到货应返回 duplicated=true 与原批次，got id=%d dup=%v err=%v", a2.ID, dup, err)
	}
	st.db.First(a1, a1.ID)
	if a1.Quantity != 1 || a1.Allocated != 1 {
		t.Fatalf("重复到货后库存被二次改动: qty=%d allocated=%d", a1.Quantity, a1.Allocated)
	}
	var arrivalCount int64
	st.db.Model(&StockArrival{}).Where("publisher_receipt = ?", "R-DUP-01").Count(&arrivalCount)
	if arrivalCount != 1 {
		t.Fatalf("同回执到货批次应只有 1 条，实际 %d", arrivalCount)
	}

	// 首次签收
	it1, dup, err := st.Receive(ReceiveInput{SubscriptionID: sub.ID, IssueNo: "2026-01", Receipt: "RC-DUP-01"})
	if err != nil || dup || it1.Status != ItemReceived {
		t.Fatalf("首次签收失败: %v dup=%v", err, dup)
	}
	// 重复签收（无论用什么回执号）：履约次数只计一次
	it2, dup, err := st.Receive(ReceiveInput{SubscriptionID: sub.ID, IssueNo: "2026-01", Receipt: "RC-DUP-01-NEW"})
	if err != nil || !dup {
		t.Fatalf("重复签收应幂等返回，got err=%v dup=%v", err, dup)
	}
	_ = it2
	d, _ := st.GetSubscription(sub.ID)
	if d.CreditsGot != 1 {
		t.Fatalf("重复签收后已履约期数应仍为 1，实际 %d", d.CreditsGot)
	}

	// 缺刊补发 + 重复出版社回执
	if _, _, err := st.ReportMissing(sub.ID, "2026-02"); err != nil {
		t.Fatalf("缺刊登记失败: %v", err)
	}
	// 重复申请同一期缺刊 → 幂等
	_, dup2, err := st.ReportMissing(sub.ID, "2026-02")
	if err != nil || !dup2 {
		t.Fatalf("重复缺刊申请应幂等，got err=%v dup=%v", err, dup2)
	}
	req, arr1, err := st.PublisherRespond(mag.ID, "2026-02", true, "")
	if err != nil || arr1 == nil || req.Status != ReissueShipped {
		t.Fatalf("出版社受理失败: %v arr=%v", err, arr1)
	}
	if arr1.Quantity != 1 || arr1.Allocated != 1 || arr1.Kind != ArrivalReissue {
		t.Fatalf("补发批次异常: %+v", arr1)
	}
	// 出版社回执重复送达：不得二次入库
	req2, arr2, err := st.PublisherRespond(mag.ID, "2026-02", true, "")
	if err != nil || arr2 == nil || arr2.ID != arr1.ID {
		t.Fatalf("重复出版社回执应返回原补发批次，got err=%v", err)
	}
	_ = req2
	var reissueBatches int64
	st.db.Model(&StockArrival{}).Where("kind = ? AND issue_no = ?", ArrivalReissue, "2026-02").Count(&reissueBatches)
	if reissueBatches != 1 {
		t.Fatalf("补发批次应只有 1 条，实际 %d", reissueBatches)
	}
	// 补发件归入原期号，签收 credit 只加 1；订阅期数不增加
	it, _, err := st.Receive(ReceiveInput{SubscriptionID: sub.ID, IssueNo: "2026-02", Receipt: "RC-02"})
	if err != nil || !it.IsReissue {
		t.Fatalf("补发件签收异常: %v is_reissue=%v", err, it.IsReissue)
	}
	d, _ = st.GetSubscription(sub.ID)
	if d.CreditsTotal != 12 || d.CreditsGot != 2 {
		t.Fatalf("补发后总期数仍为 12、已履约为 2，得到 total=%d got=%d", d.CreditsTotal, d.CreditsGot)
	}
}

// ---------- 场景 3：停刊退款按未履约期数计算，金额、明细、库存对得上 ----------

func TestStopRefundAccounting(t *testing.T) {
	st := newTestStore(t)
	mag, _, sub := fixtureYearSub(t, st)

	// 构造履约状态：
	// 01、02 已签收（已履约，不退）
	// 03 已到店未取（退款并释放库存占用）
	// 04 缺刊，补发件已到店未取（退款，同样释放补发批次占用）
	// 05..12（含 7/8 合刊 credit=2）未到货（退款）
	for _, no := range []string{"2026-01", "2026-02", "2026-03"} {
		if _, _, err := st.RegisterArrival(ArrivalInput{
			MagazineID: mag.ID, IssueNo: no, Quantity: 1, PublisherReceipt: "R-" + no,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, no := range []string{"2026-01", "2026-02"} {
		if _, _, err := st.Receive(ReceiveInput{SubscriptionID: sub.ID, IssueNo: no, Receipt: "RC-" + no}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.ReportMissing(sub.ID, "2026-04"); err != nil {
		t.Fatal(err)
	}
	if _, arr, err := st.PublisherRespond(mag.ID, "2026-04", true, ""); err != nil || arr == nil {
		t.Fatalf("补发失败: %v", err)
	}

	// 停刊前：03 正常批次与 04 补发批次各占用 1 份
	var a03, a04 StockArrival
	st.db.First(&a03, "publisher_receipt = ?", "R-2026-03")
	st.db.First(&a04, "publisher_receipt = ?", reissueReceipt(mag.Code, "2026-04"))
	if a03.Allocated != 1 || a04.Allocated != 1 {
		t.Fatalf("前置库存状态不对: a03=%d a04=%d", a03.Allocated, a04.Allocated)
	}

	res, dup, err := st.StopAndRefund(sub.ID)
	if err != nil || dup {
		t.Fatalf("停刊退款失败: %v dup=%v", err, dup)
	}
	// 未履约：03,04,05,06 各 1 + 07-08 合刊 2 + 09,10,11,12 各 1 = 10 期
	if res.Refund.UnfulfilledN != 10 {
		t.Fatalf("未履约期数应为 10，实际 %d", res.Refund.UnfulfilledN)
	}
	want := int64(10 * 1500) // 150.00 元
	if res.Refund.Amount != want {
		t.Fatalf("退款金额应为 %d 分（150.00 元），实际 %d", want, res.Refund.Amount)
	}
	// 明细：9 个未签收期号（合刊一行、credit=2）
	if len(res.Lines) != 9 {
		t.Fatalf("退款明细应为 9 行，实际 %d", len(res.Lines))
	}
	var lineCredits int
	var lineAmount int64
	for _, l := range res.Lines {
		lineCredits += l.Credit
		lineAmount += l.Amount
		if l.IssueNo == "2026-07-08" && (l.Credit != 2 || l.Amount != 3000) {
			t.Fatalf("合刊退款行应 credit=2 金额=3000，得到 %+v", l)
		}
	}
	if lineCredits != 10 || lineAmount != want {
		t.Fatalf("明细汇总对不上: credits=%d amount=%d", lineCredits, lineAmount)
	}
	// 2 份已到店未取的库存占用被释放（正常批次 + 补发批次各 1）
	if res.Released != 2 {
		t.Fatalf("应释放 2 份库存占用，实际 %d", res.Released)
	}
	st.db.First(&a03, a03.ID)
	st.db.First(&a04, a04.ID)
	if a03.Allocated != 0 || a03.Quantity-a03.Allocated != 1 {
		t.Fatalf("03 期正常批次释放后应 allocated=0 available=1，得到 %+v", a03)
	}
	if a04.Allocated != 0 || a04.Quantity-a04.Allocated != 1 {
		t.Fatalf("04 期补发批次释放后应 allocated=0 available=1，得到 %+v", a04)
	}

	// 重复停刊：返回同一退款单，金额不重复计算
	res2, dup, err := st.StopAndRefund(sub.ID)
	if err != nil || !dup {
		t.Fatalf("重复停刊应幂等，got err=%v dup=%v", err, dup)
	}
	if res2.Refund.Amount != want || res2.Refund.ID != res.Refund.ID {
		t.Fatalf("重复停刊返回了不同退款单: %+v vs %+v", res2.Refund, res.Refund)
	}
	var refundCount int64
	st.db.Model(&Refund{}).Where("subscription_id = ?", sub.ID).Count(&refundCount)
	if refundCount != 1 {
		t.Fatalf("退款单应只有 1 条，实际 %d", refundCount)
	}
	// 库存总账：3 个正常批次（01/02/03 各 qty1）+ 1 个补发批次（qty1）= 4，
	// 已签收占用 2（01/02），释放后可发 2（03 + 补发04）。
	var totalQty, totalAlloc int64
	st.db.Model(&StockArrival{}).Select("COALESCE(SUM(quantity),0)").Scan(&totalQty)
	st.db.Model(&StockArrival{}).Select("COALESCE(SUM(allocated),0)").Scan(&totalAlloc)
	if totalQty != 4 || totalAlloc != 2 {
		t.Fatalf("库存总账不平: qty=%d allocated=%d（期望 4/2）", totalQty, totalAlloc)
	}
}

// ---------- 附加：取刊方式调整只对未来刊期生效 ----------

func TestPickupModeFutureOnly(t *testing.T) {
	st := newTestStore(t)
	mag, _, sub := fixtureYearSub(t, st)

	if _, _, err := st.RegisterArrival(ArrivalInput{
		MagazineID: mag.ID, IssueNo: "2026-01", Quantity: 1, PublisherReceipt: "R-01",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Receive(ReceiveInput{SubscriptionID: sub.ID, IssueNo: "2026-01", Receipt: "RC-01"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RegisterArrival(ArrivalInput{
		MagazineID: mag.ID, IssueNo: "2026-02", Quantity: 1, PublisherReceipt: "R-02",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ChangePickupMode(sub.ID, PickupMail); err != nil {
		t.Fatalf("调整取刊方式失败: %v", err)
	}
	d, _ := st.GetSubscription(sub.ID)
	modeOf := func(no string) PickupMode {
		for _, it := range d.Items {
			if it.IssueNo == no {
				return it.EffectiveMode
			}
		}
		return ""
	}
	if modeOf("2026-01") != PickupToStore {
		t.Fatalf("已签收的 01 期应保持到店自取，实际 %s", modeOf("2026-01"))
	}
	if modeOf("2026-02") != PickupToStore {
		t.Fatalf("已到货的 02 期应保持到店自取，实际 %s", modeOf("2026-02"))
	}
	if modeOf("2026-03") != PickupMail || modeOf("2026-07-08") != PickupMail {
		t.Fatalf("未来刊期应改为邮寄，03=%s 合刊=%s", modeOf("2026-03"), modeOf("2026-07-08"))
	}
}
