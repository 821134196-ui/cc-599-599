const BASE = '/api'

async function req(method, path, body) {
  const res = await fetch(BASE + path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `请求失败 ${res.status}`)
  return data
}

export const api = {
  readers:   () => req('GET', '/readers'),
  magazines: () => req('GET', '/magazines'),
  subscriptions: (readerId) => req('GET', '/subscriptions' + (readerId ? `?reader_id=${readerId}` : '')),
  subscription:  (id) => req('GET', `/subscriptions/${id}`),
  createSubscription: (b) => req('POST', '/subscriptions', b),
  changePickup: (id, mode) => req('POST', `/subscriptions/${id}/pickup-mode`, { mode }),
  stop: (id) => req('POST', `/subscriptions/${id}/stop`),

  arrivals: (magId) => req('GET', '/arrivals' + (magId ? `?magazine_id=${magId}` : '')),
  registerArrival: (b) => req('POST', '/arrivals', b),
  stock: (magId) => req('GET', '/stock' + (magId ? `?magazine_id=${magId}` : '')),

  missing: (b) => req('POST', '/missing', b),
  reissues: () => req('GET', '/reissues'),
  publisherRespond: (b) => req('POST', '/publisher/respond', b),

  receive: (b) => req('POST', '/receive', b)
}

export const STATUS_TEXT = {
  pending: '未到货',
  arrived: '已到刊·待领取',
  received: '已签收',
  missing: '缺刊',
  reissuing: '待补发',
  refunded: '已退款'
}

export const MODE_TEXT = {
  store: '到店自取',
  mail: '邮寄',
  delivery: '送刊上门'
}

export function yuan(fen) {
  return '¥' + (Number(fen || 0) / 100).toFixed(2)
}
