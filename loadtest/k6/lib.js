import http from 'k6/http';
import { check } from 'k6';

export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
export const EMAIL = __ENV.LOAD_EMAIL || 'loadtest@seed.test';
export const PASSWORD = __ENV.LOAD_PASSWORD || 'loadtest-password';
export const ORG_SLUG = __ENV.ORG_SLUG || '';

export function login() {
  const body = { email: EMAIL, password: PASSWORD };
  if (ORG_SLUG) {
    body.org_slug = ORG_SLUG;
  }
  const res = http.post(`${BASE_URL}/v1/auth/login`, JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    tags: { name: 'auth.login' },
  });
  if (!check(res, { 'login succeeded': (r) => r.status === 200 })) {
    throw new Error(`login failed: ${res.status} ${res.body}`);
  }
  return res.json('access_token');
}

export function authHeaders(token) {
  return {
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
  };
}

export function firstTicketNumber(token) {
  const res = http.get(`${BASE_URL}/v1/tickets?limit=1`, authHeaders(token));
  if (!check(res, { 'ticket list for setup': (r) => r.status === 200 })) {
    throw new Error(`ticket lookup failed: ${res.status} ${res.body}`);
  }
  const tickets = res.json('tickets') || [];
  if (tickets.length === 0) {
    throw new Error('seed produced no tickets for the load-test org');
  }
  return tickets[0].number;
}
