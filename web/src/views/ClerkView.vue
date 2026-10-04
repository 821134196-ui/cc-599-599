<script setup>
import { onMounted, reactive, ref } from 'vue'
import { api, STATUS_TEXT, MODE_TEXT, yuan } from '../api.js'

const emit = defineEmits(['toast'])
const toast = (m, k) => emit('toast', m, k)

const magazines = ref([])
const readers = ref([])
const subs = ref([])
const stock = ref([])
const reissues = ref([])
const selected = ref(null)

const newSub = reactive({ reader_id: null, magazine_id: null, start_issue_no: '2026-01', total_issues: 12, pickup_mode: 'store' })
const arrival = reactive({ magazine_id: null, issue_no: '', quantity: 1, publisher_receipt: '', kind: 'normal' })
const missing = reactive({ subscription_id: null, issue_no: '' })
const pub = reactive({ magazine_id: null, issue_no: '', accept: true, reason: '' })
const sign = reactive({ subscription_id: null, issue_no: '', receipt: '' })

async function loadBase() {
  magazines.value = await api.magazines()
  readers.value = await api.readers()
  if (magazines.value.length) {
    arrival.magazine_id = arrival.magazine_id || magazines.value[0].id
    pub.magazine_id = pub.magazine_id || magazines.value[0].id
  }
  if (readers.value.length) newSub.reader_id = newSub.reader_id || readers.value[0].id
  if (magazines.value.length) newSub.magazine_id = newSub.magazine_id || magazines.value[0].id
}
async function loadAll() {
  subs.value = await api.subscriptions()
  const details = await Promise.all(subs.value.map(s => api.subscription(s.id)))
  subs.value = details
  const magId = arrival.magazine_id || magazines.value[0]?.id
  stock.value = magId ? await api.stock(magId) : []
  reissues.value = await api.reissues()
}
onMounted(async () => { await loadBase(); await loadAll() })

function pick(s) { selected.value = s }

async function createSub() {
  try {
    const s = await api.createSubscription({ ...newSub })
    toast('订阅已创建：#' + s.id + '，已按合刊折算规则生成逐期号计划')
    await loadAll()
  } catch (e) { toast(e.message, 'err') }
}

async function registerArrival() {
  try {
    const r = await api.registerArrival({ ...arrival })
    if (r.duplicated) toast('回执 ' + arrival.publisher_receipt + ' 已登记过，库存与分配未重复变动', 'dup')
    else toast('到货登记成功，已自动分配给等待中的订户（批次 #' + r.arrival.id + '）')
    await loadAll()
  } catch (e) { toast(e.message, 'err') }
}

async function reportMissing() {
  if (!missing.subscription_id) return toast('请选择订阅', 'warn')
  try {
    const r = await api.missing({ ...missing })
    toast(r.duplicated ? '该期缺刊已申请过补发，不重复受理' : '已登记缺刊并向出版社申请补发',
          r.duplicated ? 'dup' : 'ok')
    await loadAll()
  } catch (e) { toast(e.message, 'err') }
}

async function publisherRespond() {
  try {
    const r = await api.publisherRespond({ ...pub })
    if (r.arrival) {
      toast('出版社已受理并发货：补发件 ' + r.arrival.quantity + ' 份入库并分配给缺刊订户（归入原期号）')
    } else {
      toast('出版社驳回：' + (r.request?.reject_reason || '未说明'), 'warn')
    }
    await loadAll()
  } catch (e) { toast(e.message, 'err') }
}

async function clerkReceive() {
  try {
    const r = await api.receive({ ...sign })
    toast(r.duplicated ? '重复签收回执：该期已签收，履约次数不重复计算' : '签收成功',
          r.duplicated ? 'dup' : 'ok')
    await loadAll()
  } catch (e) { toast(e.message, 'err') }
}

async function stop(s) {
  if (!confirm('确定为订阅 #' + s.id + ' 办理停刊退款？将按未履约期数计算。')) return
  try {
    const r = await api.stop(s.id)
    if (r.duplicated) { toast('该订阅已停刊，退款单不重复生成', 'dup'); return }
    const x = r.result
    toast(`停刊完成：未履约 ${x.refund.unfulfilled_count} 期，应退 ${yuan(x.refund.amount)}，释放库存 ${x.released} 份`)
    selected.value = subs.value.find(z => z.id === s.id) || null
    await loadAll()
    selected.value = subs.value.find(z => z.id === s.id) || null
  } catch (e) { toast(e.message, 'err') }
}

function useIssue(s, no, target) {
  if (target === 'missing') { missing.subscription_id = s.id; missing.issue_no = no }
  if (target === 'sign') { sign.subscription_id = s.id; sign.issue_no = no; sign.receipt = 'RC-' + no + '-' + Date.now() }
}
const statusCount = (s, st) => s.items.filter(i => i.status === st).length
const pct = (s) => s.credits_total ? Math.round(s.credits_received / s.credits_total * 100) : 0
const magCode = (id) => magazines.value.find(m => m.id === id)?.code || ''
</script>

<template>
  <div class="grid" style="gap:20px">
    <!-- 订阅总览 -->
    <div class="card">
      <h2>订阅与逐期履约总览</h2>
      <table>
        <thead>
          <tr>
            <th>订阅</th><th>读者</th><th>期刊</th><th>期数(折算)</th>
            <th>未到货</th><th>已到刊待取</th><th>已签收</th><th>缺刊</th><th>待补发</th>
            <th>取刊方式</th><th>状态</th><th>进度</th><th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in subs" :key="s.id" :style="selected?.id === s.id ? 'background:var(--brand-soft)' : ''">
            <td>#{{ s.id }}</td>
            <td>{{ s.reader_name }}</td>
            <td>{{ s.magazine_title }}</td>
            <td>{{ s.credits_received }}/{{ s.credits_total }}</td>
            <td>{{ statusCount(s, 'pending') }}</td>
            <td>{{ statusCount(s, 'arrived') }}</td>
            <td>{{ statusCount(s, 'received') }}</td>
            <td style="color:var(--red)">{{ statusCount(s, 'missing') }}</td>
            <td style="color:var(--violet)">{{ statusCount(s, 'reissuing') }}</td>
            <td>{{ MODE_TEXT[s.pickup_mode] }}</td>
            <td>
              <span class="badge" :class="s.status === 'active' ? 'received' : 'refunded'">
                {{ s.status === 'active' ? '履约中' : '已停刊' }}
              </span>
            </td>
            <td style="min-width:90px">
              <div class="progress"><i :style="{width: pct(s)+'%'}"></i></div>
              <span class="muted">{{ pct(s) }}%</span>
            </td>
            <td>
              <button class="btn small ghost" @click="pick(s)">明细</button>
              <button v-if="s.status === 'active'" class="btn small danger" @click="stop(s)">停刊退款</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="grid two">
      <!-- 操作区 -->
      <div style="display:flex; flex-direction:column; gap:16px">
        <div class="card">
          <h2>① 新建订阅</h2>
          <label class="field"><span>读者</span>
            <select v-model="newSub.reader_id">
              <option v-for="r in readers" :key="r.id" :value="r.id">{{ r.name }}</option>
            </select>
          </label>
          <label class="field"><span>期刊</span>
            <select v-model="newSub.magazine_id">
              <option v-for="m in magazines" :key="m.id" :value="m.id">
                {{ m.title }}（{{ yuan(m.unit_price) }}/期，合刊规则按签约固定）
              </option>
            </select>
          </label>
          <div class="row">
            <label class="field"><span>起始期号</span><input v-model="newSub.start_issue_no" placeholder="2026-01"></label>
            <label class="field"><span>折算期数</span><input type="number" v-model.number="newSub.total_issues"></label>
            <label class="field"><span>取刊方式</span>
              <select v-model="newSub.pickup_mode">
                <option value="store">到店自取</option><option value="mail">邮寄</option><option value="delivery">送刊上门</option>
              </select>
            </label>
          </div>
          <button class="btn" @click="createSub">生成逐期号计划</button>
        </div>

        <div class="card">
          <h2>② 到货登记（店员登记后自动分配订户）</h2>
          <div class="row">
            <label class="field"><span>期刊</span>
              <select v-model="arrival.magazine_id" @change="loadAll">
                <option v-for="m in magazines" :key="m.id" :value="m.id">{{ m.title }}</option>
              </select>
            </label>
            <label class="field"><span>期号（合刊填 2026-07-08）</span><input v-model="arrival.issue_no"></label>
          </div>
          <div class="row">
            <label class="field"><span>到货份数</span><input type="number" v-model.number="arrival.quantity"></label>
            <label class="field"><span>类型</span>
              <select v-model="arrival.kind">
                <option value="normal">正常到货</option>
                <option value="reissue">补发件</option>
              </select>
            </label>
            <label class="field" style="flex:2"><span>出版社发货回执号（重复登记只入账一次）</span>
              <input v-model="arrival.publisher_receipt" placeholder="如 R-2026-03"></label>
          </div>
          <button class="btn" @click="registerArrival">登记到货并分配</button>
        </div>

        <div class="card">
          <h2>③ 缺刊登记 → 向出版社申请补发</h2>
          <div class="row">
            <label class="field" style="flex:2"><span>订阅</span>
              <select v-model="missing.subscription_id">
                <option :value="null" disabled>选择订阅</option>
                <option v-for="s in subs" :key="s.id" :value="s.id">#{{ s.id }} {{ s.reader_name }} · {{ s.magazine_title }}</option>
              </select>
            </label>
            <label class="field"><span>缺刊期号</span><input v-model="missing.issue_no" placeholder="2026-04"></label>
          </div>
          <button class="btn" @click="reportMissing">确认缺刊并申请补发</button>
        </div>

        <div class="card">
          <h2>④ 出版社回执（本地模拟）</h2>
          <div class="row">
            <label class="field"><span>期刊</span>
              <select v-model="pub.magazine_id">
                <option v-for="m in magazines" :key="m.id" :value="m.id">{{ m.title }}</option>
              </select>
            </label>
            <label class="field"><span>期号</span><input v-model="pub.issue_no" placeholder="2026-04"></label>
            <label class="field"><span>受理结果</span>
              <select v-model="pub.accept">
                <option :value="true">受理并发货（补发件入库）</option>
                <option :value="false">驳回</option>
              </select>
            </label>
          </div>
          <label v-if="!pub.accept" class="field"><span>驳回原因</span><input v-model="pub.reason"></label>
          <button class="btn" @click="publisherRespond">发送出版社回执</button>
          <p class="muted" style="margin-bottom:0">补发件自动生成确定性回执号 RE-期刊代码-期号；重复回执不会二次入库。</p>
        </div>

        <div class="card">
          <h2>⑤ 读者领取签收</h2>
          <div class="row">
            <label class="field" style="flex:2"><span>订阅</span>
              <select v-model="sign.subscription_id">
                <option :value="null" disabled>选择订阅</option>
                <option v-for="s in subs" :key="s.id" :value="s.id">#{{ s.id }} {{ s.reader_name }}</option>
              </select>
            </label>
            <label class="field"><span>期号</span><input v-model="sign.issue_no"></label>
            <label class="field" style="flex:2"><span>签收回执号</span><input v-model="sign.receipt"></label>
          </div>
          <button class="btn" @click="clerkReceive">登记签收</button>
        </div>
      </div>

      <!-- 右侧：库存 + 补发单 + 明细 -->
      <div style="display:flex; flex-direction:column; gap:16px">
        <div class="card">
          <h2>库存（按期号批次）</h2>
          <table>
            <thead><tr><th>期号</th><th>类型</th><th>到货</th><th>已分配</th><th>可发</th><th>出版社回执</th></tr></thead>
            <tbody>
              <tr v-for="r in stock" :key="r.id">
                <td>{{ r.issue_no }}</td>
                <td>
                  <span class="badge" :class="r.kind === 'reissue' ? 'reissuing' : 'arrived'">
                    {{ r.kind === 'reissue' ? '补发件' : '正常' }}
                  </span>
                </td>
                <td>{{ r.quantity }}</td>
                <td>{{ r.allocated }}</td>
                <td><b>{{ r.available }}</b></td>
                <td class="muted">{{ r.publisher_receipt }}</td>
              </tr>
              <tr v-if="!stock.length"><td colspan="6" class="muted">暂无到货</td></tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <h2>补发申请单</h2>
          <table>
            <thead><tr><th>期号</th><th>状态</th><th>出版社单号</th><th>备注</th></tr></thead>
            <tbody>
              <tr v-for="r in reissues" :key="r.id">
                <td>{{ r.issue_no }}</td>
                <td>
                  <span class="badge" :class="{
                    requested: 'missing', accepted: 'arrived', shipped: 'received', rejected: 'missing'
                  }[r.status]">
                    {{ {requested:'已申请', accepted:'已受理', shipped:'已发货入库', rejected:'已驳回'}[r.status] }}
                  </span>
                </td>
                <td class="muted">{{ r.publisher_no || '—' }}</td>
                <td class="muted">{{ r.reject_reason || (r.status === 'shipped' ? '补发件归入原期号' : '') }}</td>
              </tr>
              <tr v-if="!reissues.length"><td colspan="4" class="muted">暂无补发申请</td></tr>
            </tbody>
          </table>
        </div>

        <div v-if="selected" class="card">
          <h2>订阅 #{{ selected.id }} 逐期号明细（{{ selected.magazine_title }}）</h2>
          <div class="muted" style="margin-bottom:8px">
            已签收折算 {{ selected.credits_received }}/{{ selected.credits_total }} 期
          </div>
          <table>
            <thead><tr><th>#</th><th>期号</th><th>折算</th><th>方式</th><th>状态</th><th>批次/回执</th><th>快捷操作</th></tr></thead>
            <tbody>
              <tr v-for="it in selected.items" :key="it.id">
                <td>{{ it.sequence }}</td>
                <td>
                  {{ it.display_label }}
                  <span v-if="it.is_reissue" class="badge reissuing">补发</span>
                </td>
                <td>{{ it.credit }}</td>
                <td>{{ MODE_TEXT[it.effective_mode] }}</td>
                <td><span class="badge" :class="it.status">{{ STATUS_TEXT[it.status] }}</span></td>
                <td class="muted" style="font-size:11px">
                  <div v-if="it.arrival_id">批次 #{{ it.arrival_id }}</div>
                  <div v-if="it.receive_receipt">签收 {{ it.receive_receipt }}</div>
                </td>
                <td>
                  <button v-if="it.status === 'pending'" class="btn small ghost"
                          @click="useIssue(selected, it.issue_no, 'missing')">缺刊</button>
                  <button v-if="it.status === 'arrived'" class="btn small"
                          @click="useIssue(selected, it.issue_no, 'sign')">签收</button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="selected.refund" class="refund-box">
            <b>停刊退款：</b>未履约 {{ selected.refund.unfulfilled_count }} 期，
            应退 <span class="money">{{ yuan(selected.refund.amount) }}</span>
            <table style="margin-top:8px">
              <tbody>
                <tr v-for="l in selected.refund.details" :key="l.sequence">
                  <td>{{ l.label }}<span v-if="l.credit>1" class="badge merge">合刊×{{ l.credit }}</span></td>
                  <td class="money">{{ yuan(l.amount) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
