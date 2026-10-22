// Control-plane load test (L5-003). Thresholds are the targets: k6 exits non-zero when one is missed.
// Run through tests/load/run.sh, which starts `enclave server --driver fake` and passes BASE_URL / SETUP_CODE.
import http from 'k6/http';
import { check, fail } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:7171';
const JSONH = { 'Content-Type': 'application/json' };

export const options = {
  scenarios: {
    reads: { executor: 'constant-vus', vus: Number(__ENV.READ_VUS || 50), duration: __ENV.DURATION || '60s', exec: 'reads' },
    writes: { executor: 'constant-vus', vus: Number(__ENV.WRITE_VUS || 10), duration: __ENV.DURATION || '60s', exec: 'writes' },
  },
  thresholds: {
    'http_req_duration{scenario:reads}': ['p(95)<100'],
    'http_req_duration{scenario:writes}': ['p(95)<300'],
    'http_req_failed{scenario:reads}': ['rate<0.001'],
    'http_req_failed{scenario:writes}': ['rate<0.001'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  const setupBody = JSON.stringify({ setupCode: __ENV.SETUP_CODE, machineName: 'load', email: 'load@example.com', password: 'load-test-password' });
  let res = http.post(`${BASE}/api/v1/setup`, setupBody, { headers: JSONH });
  if (res.status !== 201) fail(`setup: ${res.status} ${res.body}`);
  const cookie = res.cookies.es_session[0].value;
  res = http.post(`${BASE}/api/v1/tokens`, JSON.stringify({ name: 'k6', scope: 'write' }),
    { headers: Object.assign({ 'X-Enclave-CSRF': '1', Cookie: `es_session=${cookie}` }, JSONH) });
  if (res.status !== 201) fail(`token: ${res.status} ${res.body}`);
  const token = res.json('secret');
  const auth = { Authorization: `Bearer ${token}` };
  const ids = [];
  for (let i = 0; i < 50; i++) {
    const r = http.post(`${BASE}/api/v1/resources`, JSON.stringify({ kind: 'cache', name: `seed-${i}`, spec: {} }), { headers: Object.assign({}, auth, JSONH) });
    if (r.status !== 202) fail(`seed: ${r.status} ${r.body}`);
    ids.push(r.json('id'));
  }
  return { auth, ids };
}

export function reads(data) {
  const id = data.ids[Math.floor(Math.random() * data.ids.length)];
  const roll = Math.random();
  let res;
  if (roll < 0.4) res = http.get(`${BASE}/api/v1/resources?kind=cache&limit=50`, { headers: data.auth, tags: { op: 'list' } });
  else if (roll < 0.9) res = http.get(`${BASE}/api/v1/resources/${id}`, { headers: data.auth, tags: { op: 'get' } });
  else res = http.get(`${BASE}/api/v1/machine`, { headers: data.auth, tags: { op: 'machine' } });
  check(res, { 'read 200': (r) => r.status === 200 });
}

export function writes(data) {
  const name = `load-${__VU}-${__ITER}`;
  const headers = Object.assign({ 'Idempotency-Key': `${name}-${Date.now()}` }, data.auth, JSONH);
  const created = http.post(`${BASE}/api/v1/resources`, JSON.stringify({ kind: 'secret', name, spec: { value: 'x' } }), { headers, tags: { op: 'create' } });
  check(created, { 'create 202': (r) => r.status === 202 });
  if (created.status === 202) {
    const del = http.del(`${BASE}/api/v1/resources/${created.json('id')}`, null, { headers: data.auth, tags: { op: 'delete' } });
    check(del, { 'delete 202': (r) => r.status === 202 });
  }
}
