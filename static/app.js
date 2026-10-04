const { createApp } = Vue;

createApp({
  data() {
    return {
      tab: "reader",
      tabs: [
        { key: "reader", label: "读者页" },
        { key: "staff", label: "店员台" },
        { key: "inventory", label: "库存与刊期" },
      ],
      publications: [],
      subscriptions: [],
      schedule: [],
      schedules: {}, // pubId -> schedule
      inventories: [],
      fulfillments: [],
      reissueRequests: [],
      readerView: null,
      selectedSubId: null,
      boardPubId: "",
      arrival: { publication_id: null, issue_no: "", quantity: 2, idempotency_key: "", note: "" },
      newSub: { reader_name: "", publication_id: null, start_issue_no: "", total_periods: 12, copies: 1, pickup_method: "self" },
      pmSubId: null,
      pmMethod: "delivery",
      suspendPubId: null,
      suspendReason: "出版社停刊",
      suspendResult: [],
      toast: "",
      toastType: "ok",
      receiptSeq: 1,
    };
  },
  async mounted() {
    await this.reloadAll();
  },
  methods: {
    async api(path, opts = {}) {
      const res = await fetch("/api" + path, {
        headers: { "Content-Type": "application/json" },
        ...opts,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || ("HTTP " + res.status));
      return data;
    },
    async reloadAll() {
      this.publications = await this.api("/publications");
      this.subscriptions = await this.api("/subscriptions");
      for (const p of this.publications) {
        const d = await this.api("/publications/" + p.id);
        this.schedules[p.id] = d.schedule;
      }
      if (this.publications.length && !this.arrival.publication_id) {
        this.arrival.publication_id = this.publications[0].id;
        this.newSub.publication_id = this.publications[0].id;
        this.suspendPubId = this.publications[0].id;
        this.loadSchedule();
      }
      if (this.subscriptions.length && !this.selectedSubId) {
        this.selectedSubId = this.subscriptions[0].id;
        this.pmSubId = this.subscriptions[0].id;
      }
      await Promise.all([
        this.loadReaderView(),
        this.loadFulfillments(),
        this.loadInventories(),
        this.loadReissues(),
      ]);
    },
    async loadSchedule() {
      if (!this.arrival.publication_id) return;
      const d = await this.api("/publications/" + this.arrival.publication_id);
      this.schedule = d.schedule;
      if (this.schedule.length && !this.schedule.some((s) => s.issue_no === this.arrival.issue_no)) {
        this.arrival.issue_no = this.schedule[0].issue_no;
      }
      if (!this.newSub.start_issue_no && this.schedule.length) {
        this.newSub.start_issue_no = this.schedule[0].issue_no;
      }
      this.suggestKey();
    },
    suggestKey() {
      // 建议键：同一期同一天重复提交会判重；新批次请改键
      const d = new Date();
      const ymd = `${d.getFullYear()}${String(d.getMonth() + 1).padStart(2, "0")}${String(d.getDate()).padStart(2, "0")}`;
      this.arrival.idempotency_key = `ARR-${this.arrival.publication_id}-${this.arrival.issue_no}-${ymd}`;
    },
    async loadReaderView() {
      if (!this.selectedSubId) return;
      this.readerView = await this.api(`/subscriptions/${this.selectedSubId}/view`);
    },
    async loadFulfillments() {
      const q = this.boardPubId ? `?publication_id=${this.boardPubId}` : "";
      this.fulfillments = await this.api("/fulfillments" + q);
    },
    async loadInventories() {
      this.inventories = await this.api("/inventories");
    },
    async loadReissues() {
      this.reissueRequests = await this.api("/reissue-requests");
    },
    async run(fn, okMsg) {
      try {
        const r = await fn();
        this.notify(okMsg || "操作成功", "ok");
        await this.reloadAll();
        return r;
      } catch (e) {
        this.notify(e.message, "err");
      }
    },
    registerArrival() {
      return this.run(async () => {
        const r = await this.api("/arrivals", { method: "POST", body: JSON.stringify(this.arrival) });
        if (r.duplicate) this.notify("重复登记：该批次已存在，库存与履约未变动", "warn");
        else this.notify(`到货登记成功：${r.batch.issue_no} × ${r.batch.quantity}`, "ok");
      });
    },
    createSub() {
      return this.run(async () => {
        await this.api("/subscriptions", { method: "POST", body: JSON.stringify(this.newSub) });
        this.notify("订阅创建成功", "ok");
        this.newSub.reader_name = "";
      });
    },
    changePickupMethod() {
      return this.run(async () => {
        const r = await this.api(`/subscriptions/${this.pmSubId}/pickup-method`, {
          method: "PUT",
          body: JSON.stringify({ pickup_method: this.pmMethod }),
        });
        this.notify(`已调整，${r.updated_future_issues} 条未来刊期生效（已完成记录保持原样）`, "ok");
      });
    },
    suspendPub() {
      return this.run(async () => {
        const r = await this.api(`/publications/${this.suspendPubId}/suspend`, {
          method: "POST",
          body: JSON.stringify({ reason: this.suspendReason }),
        });
        this.suspendResult = r.refunds;
        this.notify(`停刊完成，共 ${r.refunds.length} 笔退款`, "ok");
      });
    },
    pickup(f) {
      const name = prompt(`签收人（${f.reader_name} · ${f.issue_no}）：`, f.reader_name);
      if (!name) return;
      return this.run(async () => {
        const r = await this.api(`/fulfillments/${f.id}/pickup`, {
          method: "POST",
          body: JSON.stringify({ picked_by: name }),
        });
        if (r.duplicate) this.notify("重复签收回执：计数未变动", "warn");
        else this.notify("签收成功", "ok");
      });
    },
    markMissing(f) {
      return this.run(async () => {
        await this.api(`/fulfillments/${f.id}/mark-missing`, { method: "POST" });
        this.notify("已登记缺刊", "ok");
      });
    },
    requestReissue(f) {
      return this.run(async () => {
        const r = await this.api(`/fulfillments/${f.id}/reissue-requests`, { method: "POST" });
        if (r.duplicate) this.notify("该期已申请过补发，返回原申请", "warn");
        else this.notify("已向出版社申请补发", "ok");
      });
    },
    simulateReceipt(r) {
      const receiptNo = r.receipt_no || `RCPT-${r.issue_no}-${r.id}`;
      return this.run(async () => {
        const res = await this.api("/publisher/receipts", {
          method: "POST",
          body: JSON.stringify({ receipt_no: receiptNo, reissue_request_id: r.id }),
        });
        if (res.duplicate) this.notify("重复回执：库存与履约未变动", "warn");
        else this.notify(`收到出版社补发件（回执 ${receiptNo}），已归入原期号`, "ok");
      });
    },
    schedulesOf(pubId) {
      return this.schedules[pubId] || [];
    },
    pubName(id) {
      const p = this.publications.find((x) => x.id === id);
      return p ? p.name : id;
    },
    statusLabel(s) {
      return {
        pending: "待到刊",
        arrived: "已到刊",
        picked: "已领取",
        missing: "缺刊",
        reissue_requested: "待补发",
        refunded: "已退款",
      }[s] || s;
    },
    subStatusLabel(s) {
      return { active: "在订", completed: "已完成", refunded: "已退款" }[s] || s;
    },
    pickupLabel(m) {
      return { self: "到店自取", delivery: "配送上门" }[m] || m;
    },
    yuan(cents) {
      return "¥" + (cents / 100).toFixed(2);
    },
    fmtTime(t) {
      if (!t) return "—";
      return new Date(t).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
    },
    notify(msg, type) {
      this.toast = msg;
      this.toastType = type || "ok";
      clearTimeout(this._tt);
      this._tt = setTimeout(() => (this.toast = ""), 3500);
    },
  },
}).mount("#app");
