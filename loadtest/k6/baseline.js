import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate } from 'k6/metrics';

import { BASE_URL, authHeaders, firstTicketNumber, login } from './lib.js';

const listLatency = new Trend('ticket_list_duration', true);
const detailLatency = new Trend('ticket_detail_duration', true);
const createLatency = new Trend('ticket_create_duration', true);
const searchLatency = new Trend('ticket_search_duration', true);
const failures = new Rate('scenario_failures');

export const options = {
  scenarios: {
    ticket_list: {
      exec: 'listTickets',
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 10 },
        { duration: '1m', target: 50 },
        { duration: '30s', target: 0 },
      ],
      gracefulRampDown: '10s',
    },
    ticket_detail: {
      exec: 'ticketDetail',
      executor: 'constant-vus',
      vus: 20,
      duration: '2m',
      startTime: '10s',
    },
    ticket_create: {
      exec: 'createTicket',
      executor: 'constant-arrival-rate',
      rate: 5,
      timeUnit: '1s',
      duration: '2m',
      preAllocatedVUs: 10,
      maxVUs: 40,
      startTime: '10s',
    },
    ticket_search: {
      exec: 'searchTickets',
      executor: 'constant-vus',
      vus: 10,
      duration: '2m',
      startTime: '10s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{scenario:ticket_list}': ['p(95)<500'],
    'http_req_duration{scenario:ticket_detail}': ['p(95)<300'],
    'http_req_duration{scenario:ticket_create}': ['p(95)<500'],
    'http_req_duration{scenario:ticket_search}': ['p(95)<500'],
  },
};

export function setup() {
  const token = login();
  return { token, number: firstTicketNumber(token) };
}

export function listTickets(data) {
  const res = http.get(`${BASE_URL}/v1/tickets?limit=25`, authHeaders(data.token));
  const ok = check(res, {
    'list 200': (r) => r.status === 200,
    'list has tickets': (r) => (r.json('tickets') || []).length > 0,
  });
  failures.add(!ok);
  listLatency.add(res.timings.duration);
}

export function ticketDetail(data) {
  const res = http.get(`${BASE_URL}/v1/tickets/${data.number}`, authHeaders(data.token));
  const ok = check(res, { 'detail 200': (r) => r.status === 200 });
  failures.add(!ok);
  detailLatency.add(res.timings.duration);
}

export function createTicket(data) {
  const body = JSON.stringify({
    requester_email: `load-${__VU}-${__ITER}@seed.test`,
    requester_name: 'Load Test',
    subject: `k6 load ticket ${__VU}-${__ITER}`,
    body: 'Created by the k6 baseline scenario.',
    priority: 'normal',
  });
  const res = http.post(`${BASE_URL}/v1/tickets`, body, authHeaders(data.token));
  const ok = check(res, { 'create 201': (r) => r.status === 201 });
  failures.add(!ok);
  createLatency.add(res.timings.duration);
}

export function searchTickets(data) {
  const res = http.get(`${BASE_URL}/v1/search?q=printer&limit=20`, authHeaders(data.token));
  const ok = check(res, { 'search 200': (r) => r.status === 200 });
  failures.add(!ok);
  searchLatency.add(res.timings.duration);
}
