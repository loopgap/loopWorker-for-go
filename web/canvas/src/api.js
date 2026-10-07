// Every call to the LoopWorker API goes through this module.
//
// The API authenticates each /api/v1 route with a header credential: the Go
// authenticator reads X-API-Key and Authorization, and nothing else. The
// browser's EventSource cannot set headers, which is why the live stream below
// uses fetch plus a streaming reader instead - same credential path, no token in
// the URL and therefore nothing in the access log or browser history.

const TOKEN_KEY = 'loopworker.apiKey';
const API_KEY_HEADER = 'X-API-Key';

export function getToken() {
  try {
    return window.localStorage.getItem(TOKEN_KEY) || '';
  } catch {
    // Private browsing can refuse localStorage; the session simply will not
    // persist across reloads, which is better than failing to load.
    return '';
  }
}

export function setToken(value) {
  try {
    if (value) {
      window.localStorage.setItem(TOKEN_KEY, value);
    } else {
      window.localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    /* see getToken */
  }
}

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// The server always answers with {success, error:{code,message}}; show the
// repair hint it wrote for the customer rather than "[object Object]".
function messageFrom(body, fallback) {
  if (body && body.error && body.error.message) return body.error.message;
  return fallback;
}

// Every ordinary call gets a ceiling. A request that never receives an answer
// must fail loudly: without this, one stalled connection leaves the canvas
// spinning forever with no way for the operator to tell it apart from a slow
// server, and no way out but reloading the page.
const REQUEST_TIMEOUT_MS = 30000;

export async function apiFetch(path, options = {}) {
  const token = getToken();
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  if (token) headers[API_KEY_HEADER] = token;

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  let res;
  try {
    res = await fetch(path, { ...options, headers, signal: controller.signal });
  } catch (e) {
    if (controller.signal.aborted) {
      throw new ApiError(
        0,
        `the server did not answer ${path} within ${REQUEST_TIMEOUT_MS / 1000}s`
      );
    }
    throw e;
  } finally {
    clearTimeout(timer);
  }

  const text = await res.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, messageFrom(body, `${res.status} ${res.statusText}`));
  }
  return body;
}

// sseFetch subscribes to the live event stream.
//
// handlers is keyed by event name; a frame with no name goes to handlers.message,
// matching EventSource's own default. The returned promise rejects on a refused
// stream; pass an AbortSignal and cancel it on unmount, or the connection leaks.
export function sseFetch(path, handlers = {}, signal) {
  const token = getToken();
  const headers = { Accept: 'text/event-stream' };
  if (token) headers[API_KEY_HEADER] = token;

  return (async () => {
    const res = await fetch(path, { headers, signal });
    if (!res.ok || !res.body) {
      throw new ApiError(res.status, `event stream refused: ${res.status}`);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let eventName = 'message';
    let dataLines = [];

    const flush = () => {
      if (dataLines.length > 0) {
        const handler = handlers[eventName] || handlers.message;
        if (handler) handler(dataLines.join('\n'), eventName);
      }
      dataLines = [];
      eventName = 'message';
    };

    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let idx;
      while ((idx = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, idx).replace(/\r$/, '');
        buffer = buffer.slice(idx + 1);
        if (line === '') {
          flush();
        } else if (line.startsWith(':')) {
          // keep-alive comment; nothing to dispatch
        } else if (line.startsWith('event:')) {
          eventName = line.slice(6).trim();
        } else if (line.startsWith('data:')) {
          dataLines.push(line.slice(5).trimStart());
        }
      }
    }
  })();
}