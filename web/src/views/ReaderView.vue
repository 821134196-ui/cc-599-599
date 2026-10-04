<script setup>
import { inject, onMounted, ref, watch } from 'vue'
import { api, STATUS_TEXT, MODE_TEXT, yuan } from '../api.js'

const toast = inject('toast')
const readers = ref([])
const readerId = ref(null)
const subs = ref([])
const subId = ref(null)
const detail = ref(null)
const newMode = ref('')

async function loadReaders() {
  readers.value = await api.readers()
  if (readers.value.length && !readerId.value) {
    readerId.value = readers.value[0].id
  }
}
async function loadSubs() {
  if (!readerId.value) { subs.value = []; return }
  subs.value = await api.subscriptions(readerId.value)
  if (subs.value.length) {
    subId.value = subs.value.some(s => s.id === subId.value) ? subId.value : subs.value[0].id
  } else {
    subId.value = null
    detail.value = null
  }
  await loadDetail()
}
async function loadDetail() {
  detail.value = subId.value ? await api.subscription(subId.value) : null
  newMode.value = detail.value?.pickup_mode || ''
}
watch(readerId, loadSubs)
onMounted(async () => { await loadReaders(); await loadSubs() })

async function receive(item) {
  try {
    const r = await api.receive({
      subscription_id: subId.value,
      issue_no: item.issue_no,
      receipt: 'RC-' + item.issue_no + '-' + Date.now()
    })
    toast(r.duplicated ? '该期已签收，重复回执只计一次' : '签收成功：' + item.display_label,
          r.duplicated ? 'dup' : 'ok')
    await loadDetail()
  } catch (e) { toast(e.message, 'err') }
}

async function changeMode() {
  try {
    await api.changePickup(subId.value, newMode.value)
    toast('取刊方式已调整，仅对未来未到货刊期生效；已完成记录保持原样')
    await loadDetail()
  } catch (e) { toast(e.message, 'err') }
}

function modeFor(item) { return MODE_TEXT[item.effective_mode] || item.effective_mode }
const pct = (d) => d.credits_total ? Math.round(d.credits_received / d.credits_total * 100) : 0
</script>

<template>
  <div class="grid two">
    <div>
      <div class="card">
        <h2>选择读者</h2>
        <select v-model="readerId">
          <option v-for="r in readers" :key="r.id" :value="r.id">{{ r.name }}（{{ r.phone }}）</option>
        </select>
        <h3>该读者的订阅</h3>
        <div v-if="!subs.length" class="muted">暂无订阅</div>
        <div
          v-for="s in subs" :key="s.id"
          class="list-item" :class="{ active: s.id === subId }"
          @click="subId = s.id; loadDetail()"
        >
          <div>订阅 #{{ s.id }} · {{ s.total_issues }} 期</div>
          <div class="sub">
            起始 {{ s.start_issue_no }} · {{ MODE_TEXT[s.pickup_mode] }}
            · <span :style="{color: s.status === 'active' ? 'var(--green)' : 'var(--red)'}">
              {{ s.status === 'active' ? '履约中' : '已停刊' }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <div v-if="detail">
      <div class="card">
        <h2>
          订阅 #{{ detail.id }} · {{ detail.magazine_title }}
          <span class="muted">｜{{ detail.reader_name }}</span>
        </h2>
        <div class="muted">
          起始期号 {{ detail.start_issue_no }} ｜ 折算总期数 {{ detail.credits_total }} ｜
          已签收折算期数 {{ detail.credits_received }}
        </div>
        <div class="progress"><i :style="{ width: pct(detail) + '%' }"></i></div>
        <div class="muted">履约进度 {{ pct(detail) }}%（合刊按折算期数计）</div>

        <h3>调整取刊方式（仅未来刊期生效）</h3>
        <div class="row">
          <label class="field" style="max-width:200px">
            <select v-model="newMode">
              <option value="store">到店自取</option>
              <option value="mail">邮寄</option>
              <option value="delivery">送刊上门</option>
            </select>
          </label>
          <button class="btn" :disabled="newMode === detail.pickup_mode || detail.status !== 'active'"
                  @click="changeMode">保存调整</button>
          <span class="muted">已到刊/已签收的期号保持原方式</span>
        </div>
        <div v-if="detail.pickup_history?.length" class="muted" style="margin-top:6px">
          变更记录：
          <span v-for="(h, i) in detail.pickup_history" :key="i">
            「{{ MODE_TEXT[h.from_mode] }} → {{ MODE_TEXT[h.to_mode] }}」自 {{ h.change_issue_no || '签约起' }} 之后生效；
          </span>
        </div>
      </div>

      <div class="card section-gap">
        <h2>逐期号履约记录</h2>
        <table>
          <thead>
            <tr><th>#</th><th>期号</th><th>折算期数</th><th>取刊方式</th><th>状态</th><th>凭证</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="it in detail.items" :key="it.id">
              <td>{{ it.sequence }}</td>
              <td>
                {{ it.display_label }}
                <span v-if="it.credit > 1" class="badge merge">合刊</span>
                <span v-if="it.is_reissue" class="badge reissuing">补发件</span>
              </td>
              <td>{{ it.credit }}</td>
              <td><span class="tag-mode">{{ modeFor(it) }}</span></td>
              <td><span class="badge" :class="it.status">{{ STATUS_TEXT[it.status] }}</span></td>
              <td class="muted">
                <span v-if="it.receive_receipt">签收 {{ it.receive_receipt }}</span>
                <span v-else-if="it.arrival_id">批次 #{{ it.arrival_id }}</span>
                <span v-else>—</span>
              </td>
              <td>
                <button v-if="it.status === 'arrived'" class="btn small" @click="receive(it)">领取签收</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="detail.refund" class="card refund-box section-gap">
        <h2>停刊退款单 #{{ detail.refund.id }}</h2>
        <div>
          未履约折算期数 <b>{{ detail.refund.unfulfilled_count }}</b> 期 ｜
          单期价 {{ yuan(detail.refund.per_issue_price) }} ｜
          应退合计 <span class="money" style="font-size:16px">{{ yuan(detail.refund.amount) }}</span>
        </div>
        <table style="margin-top:10px">
          <thead><tr><th>#</th><th>期号</th><th>折算期数</th><th>停刊时状态</th><th>退款额</th></tr></thead>
          <tbody>
            <tr v-for="l in detail.refund.details" :key="l.sequence">
              <td>{{ l.sequence }}</td>
              <td>{{ l.label }}<span v-if="l.credit > 1" class="badge merge">合刊</span></td>
              <td>{{ l.credit }}</td>
              <td><span class="badge" :class="l.status">{{ STATUS_TEXT[l.status] }}</span></td>
              <td class="money">{{ yuan(l.amount) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <div v-else class="card muted">请选择左侧订阅查看逐期号履约情况</div>
  </div>
</template>
