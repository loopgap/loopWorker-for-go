import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiFetch, sseFetch, ApiError, setToken } from './api.js'

// The API module is the only place the canvas speaks HTTP, and every defect it
// used to carry was invisible to the compiler: a request that never answers,
// an error object rendered as "[object Object]", an event stream that silently
// drops frames. All of it is behaviour, so all of it is tested here.

const jsonResponse = (body, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

beforeEach(() => {
  setToken('')
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('apiFetch', () => {
  it('surfaces the repair hint the server wrote, not "[object Object]"', async () => {
    // The server answers a bad request with {success:false, error:{code,message}}
    // and a message written for a human. Showing the raw object was a dead end
    // for exactly the person who needed the hint.
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse(
          {
            success: false,
            error: {
              code: 'UNKNOWN_FIELD',
              message: 'the field "skill" is not part of this endpoint contract',
            },
          },
          400
        )
      )
    )

    await expect(apiFetch('/api/v1/tasks')).rejects.toThrow(
      /is not part of this endpoint/
    )
    await expect(apiFetch('/api/v1/tasks')).rejects.toBeInstanceOf(ApiError)
  })

  it('keeps the status on the error so callers can tell 401 from 500', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ error: { message: 'nope' } }, 401)))

    const err = await apiFetch('/api/v1/x').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(401)
  })

  it('gives up on a server that never answers instead of spinning forever', async () => {
    // Without a ceiling one stalled connection left the canvas spinning with no
    // way for the operator to tell it apart from a slow server.
    vi.useFakeTimers()
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_url, opts) =>
          new Promise((_resolve, reject) => {
            opts.signal.addEventListener('abort', () =>
              reject(new DOMException('aborted', 'AbortError'))
            )
          })
      )
    )

    // Attach the rejection handler before advancing the clock: the abort fires
    // during advanceTimersByTimeAsync, and a promise that rejects with no
    // handler attached yet is an unhandled rejection even though we assert on
    // it a line later.
    const pending = expect(apiFetch('/api/v1/workflow/graph')).rejects.toThrow(
      /did not answer/
    )
    await vi.advanceTimersByTimeAsync(31000)
    await pending
  })

  it('sends the stored API key as a header, never in the URL', async () => {
    setToken('secret-key')
    const spy = vi.fn(async () => jsonResponse({ success: true, data: {} }))
    vi.stubGlobal('fetch', spy)

    await apiFetch('/api/v1/workflow/graph')

    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/v1/workflow/graph')
    expect(opts.headers['X-API-Key']).toBe('secret-key')
    expect(String(url)).not.toContain('secret-key')
  })
})

describe('sseFetch', () => {
  // The parser in api.js is hand-written because a browser EventSource cannot
  // send an Authorization header. That makes it the canvas's most fragile piece
  // of code, and a real socket delivers bytes at boundaries nobody chooses.
  // streamOf reproduces exactly those boundaries.
  const streamOf = (chunks) =>
    new Response(
      new ReadableStream({
        start(controller) {
          const encoder = new TextEncoder()
          for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
          controller.close()
        },
      }),
      { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
    )

  it('reassembles a frame split across network chunks', async () => {
    const whole = 'event: task.completed\ndata: {"id":"task-1","state":"completed"}\n\n'
    // Cut in the three worst places at once: inside the event name, across the
    // line boundary into the middle of the JSON, and with the terminating blank
    // line arriving alone. A parser without a buffer emits a truncated frame.
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => streamOf([
        whole.slice(0, 9),
        whole.slice(9, 30),
        whole.slice(30, whole.length - 1),
        whole.slice(whole.length - 1),
      ])),
    )

    const completed = vi.fn()
    await sseFetch('/api/v1/events/live', { 'task.completed': completed })

    expect(completed).toHaveBeenCalledTimes(1)
    expect(JSON.parse(completed.mock.calls[0][0]).state).toBe('completed')
  })

  it('accepts CRLF line endings, which the SSE specification allows', async () => {
    // Go's Fprintf writes LF, but a reverse proxy that rewrites the stream, or
    // any future server on any platform, may send CRLF. A stray \r left on the
    // end of a data line is enough to make the JSON unparseable.
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => streamOf(['event: task.created\r\ndata: {"id":"task-7"}\r\n\r\n'])),
    )

    const created = vi.fn()
    await sseFetch('/api/v1/events/live', { 'task.created': created })

    expect(created).toHaveBeenCalledTimes(1)
    expect(JSON.parse(created.mock.calls[0][0]).id).toBe('task-7')
  })

  it('joins a multi-line data field with newlines, as EventSource does', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => streamOf(['event: note\ndata: first\ndata: second\n\n'])),
    )

    const note = vi.fn()
    await sseFetch('/api/v1/events/live', { note })

    expect(note).toHaveBeenCalledTimes(1)
    expect(note.mock.calls[0][0]).toBe('first\nsecond')
  })

  it('handles the opening frame the server sends before any task event', async () => {
    // Byte-for-byte the shape pkg/api/handlers_events.go writes. If the canvas
    // chokes on this it shows up as "connecting to LoopWorker..." forever.
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => streamOf(['event: stream.opened\ndata: {"caller":"k","types":13}\n\n'])),
    )

    const opened = vi.fn()
    await sseFetch('/api/v1/events/live', { 'stream.opened': opened })

    expect(opened).toHaveBeenCalledTimes(1)
    expect(JSON.parse(opened.mock.calls[0][0]).types).toBe(13)
  })

  it('routes named events to their handler and ignores keep-alive comments', async () => {
    // The canvas paints live updates straight off this stream; a frame parsed
    // into the wrong bucket is a node that silently stops updating.
    const frames = [
      ': keep-alive\n\n',
      'event: task.created\ndata: {"id":"task-1","state":"queued"}\n\n',
      'event: task.completed\ndata: {"id":"task-1","state":"completed"}\n\n',
    ].join('')

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(frames, { status: 200 }))
    )

    const created = vi.fn()
    const completed = vi.fn()
    await sseFetch('/api/v1/events/live', {
      'task.created': created,
      'task.completed': completed,
    })

    expect(created).toHaveBeenCalledTimes(1)
    expect(completed).toHaveBeenCalledTimes(1)
    expect(JSON.parse(created.mock.calls[0][0]).id).toBe('task-1')
  })

  it('rejects a refused stream instead of resolving silently', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))

    await expect(sseFetch('/api/v1/events/live', {})).rejects.toThrow(
      /event stream refused: 401/
    )
  })
})