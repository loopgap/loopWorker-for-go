import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import App from './App.jsx'
import { setToken } from './api.js'

// Every defect this file guards was invisible to the compiler and to CI: the
// server speaks lower-case JSON keys while the node cards read Go field names,
// a plain-text payload was run through atob(), the live stream was ignored for
// tasks created outside the canvas, and an effect guard could wedge the splash
// screen forever. All of them were found by hand, one browser at a time.

const GRAPH_PATH = '/api/v1/workflow/graph'
const LIVE_PATH = '/api/v1/events/live'
const TASKS_PATH = '/api/v1/tasks'

// The shape the Go API actually returns. Built from a real response, because
// the casing is the whole point: node.data.task.state, not task.State.
const task = (overrides = {}) => ({
  id: 'task-1',
  type: 'hello',
  state: 'completed',
  priority: 1,
  priority_name: 'normal',
  owner: 'local-operator',
  created_at: '2026-10-06T09:52:33.4252041Z',
  input: '{"name":"probe-marker-12345"}',
  input_encoding: 'text',
  result: 'hello from wasm',
  result_encoding: 'text',
  retry: 0,
  max_retry: 3,
  dependencies: [],
  is_agent: false,
  ...overrides,
})

const graphResponse = (tasks = [task()]) => ({
  success: true,
  data: {
    edges: [],
    meta: { nodes: tasks.length, edges: 0, max_nodes: 5000, truncated: false, scope: 'all' },
    nodes: tasks.map((t) => ({
      id: t.id,
      type: 'wasmNode',
      position: { x: 100, y: 100 },
      data: { label: t.type, task: t, depth: 0, in_cycle: false },
    })),
  },
})

// A live stream the test can push frames into, so "a task appeared without a
// Sync click" can be asserted instead of assumed.
function liveStream() {
  let controller
  const stream = new ReadableStream({
    start(c) {
      controller = c
    },
  })
  const enc = new TextEncoder()
  return {
    stream,
    emit(event, data) {
      controller.enqueue(enc.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`))
    },
    close() {
      controller.close()
    },
  }
}

let live
let currentTasks

function stubServer({ graphStatus = 200, tasks, createStatus = 201, createMessage = '' } = {}) {
  if (tasks) currentTasks = tasks
  const spy = vi.fn(async (url, opts = {}) => {
    if (url === LIVE_PATH) return new Response(live.stream, { status: 200 })
    if (url === TASKS_PATH && opts.method === 'POST') {
      if (createStatus >= 400) {
        return new Response(JSON.stringify({ error: { message: createMessage } }), {
          status: createStatus,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      // The server allocates the id and really does accept the task, so the
      // refetch the canvas performs after a create has something to return.
      const sent = JSON.parse(opts.body)
      currentTasks = [...currentTasks, task({ id: 'task-new', type: sent.type })]
      return new Response(JSON.stringify({ success: true, data: { id: 'task-new' } }), {
        status: createStatus,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === GRAPH_PATH) {
      return new Response(JSON.stringify(graphResponse(currentTasks)), {
        status: graphStatus,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response(JSON.stringify({ error: { message: `unexpected ${url}` } }), {
      status: 404,
      headers: { 'Content-Type': 'application/json' },
    })
  })
  vi.stubGlobal('fetch', spy)
  return spy
}

// The server already has this task; the canvas does not, and only the event
// tells it. Without this the stub would keep answering with the same graph and
// the refetch would prove nothing.
function serverAcceptsAnotherTask(t) {
  currentTasks = [...currentTasks, t]
}

const graphFetches = (spy) => spy.mock.calls.filter(([url]) => url === GRAPH_PATH).length
const postCalls = (spy) => spy.mock.calls.filter(([url, opts]) => url === TASKS_PATH && opts?.method === 'POST')

beforeEach(() => {
  setToken('')
  window.localStorage.clear()
  live = liveStream()
  currentTasks = [task()]
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('boot', () => {
  it('settles past the splash screen and paints the graph', async () => {
    // The probe used to carry an "already started" guard that deadlocked the
    // splash under a double-invoked effect: nothing ever appeared and there was
    // no error to explain why.
    stubServer()
    render(<App />)

    await waitFor(() => expect(screen.queryByText(/Connecting to LoopWorker/i)).toBeNull(), {
      timeout: 3000,
    })
    await screen.findByText('hello')
  })

  it('asks for the graph a bounded number of times on mount', async () => {
    // fetchGraph used to depend on [setNodes, setEdges], which are new
    // references every render, so the effect re-ran and re-fetched forever.
    // Two calls are correct and expected: App's credential probe, then Canvas's
    // initial load. Anything beyond that is the loop coming back.
    const spy = stubServer()
    render(<App />)

    await screen.findByText('hello')
    await new Promise((r) => setTimeout(r, 150))
    expect(graphFetches(spy)).toBe(2)
  })

  it('shows a credential form on 401, and not on a dead network', async () => {
    stubServer({ graphStatus: 401 })
    render(<App />)
    await screen.findByText(/api key/i, undefined, { timeout: 3000 })

    cleanup()
    live = liveStream()
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url) =>
        url === GRAPH_PATH
          ? new Response(JSON.stringify({ error: { message: 'connection refused' } }), { status: 500 })
          : new Response(live.stream, { status: 200 })
      )
    )
    render(<App />)
    await screen.findByText(/connection refused/i, undefined, { timeout: 3000 })
    expect(screen.queryByText(/api key/i)).toBeNull()
  })

  it('gives up on a server that accepts the request and never answers', async () => {
    // The third way this can go, and the one neither test above covers: the
    // connection is established, the request goes out, and no response ever
    // arrives. Neither a status nor a rejection arrives, so without a ceiling
    // the splash sits on "Connecting to LoopWorker…" forever and the operator
    // has no way to tell a wedged server from a slow one, and no way out but
    // reloading. apiFetch's AbortController is the ceiling; this is what holds
    // it in place.
    vi.useFakeTimers()
    try {
      // Models a real fetch: it settles only when the signal aborts.
      vi.stubGlobal(
        'fetch',
        vi.fn(
          (url, opts) =>
            new Promise((resolve, reject) => {
              if (url !== GRAPH_PATH) {
                resolve(new Response(new ReadableStream(), { status: 200 }))
                return
              }
              opts.signal.addEventListener('abort', () =>
                reject(new DOMException('The operation was aborted.', 'AbortError'))
              )
            })
        )
      )
      render(<App />)

      // Still waiting, as it must be: a server that has not answered is not
      // an error yet, and claiming failure early would be its own lie.
      expect(screen.queryByText(/Connecting to LoopWorker/i)).toBeTruthy()
      expect(screen.queryByText(/Retry connection/i)).toBeNull()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(31_000)
      })

      // Synchronous on purpose: findByText polls on its own schedule, which
      // does not advance under fake timers, so the test would time out while
      // waiting for a state update that has already happened.
      expect(screen.getByText(/did not answer .* within 30s/i)).toBeTruthy()
      expect(screen.getByText(/Retry connection/i)).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('creating a node', () => {
  it('posts input_text verbatim, not base64 of it', async () => {
    // The one action this GUI exists for, and the one nothing tested. The
    // payload used to carry btoa(userInput) with no input_encoding, so the
    // server took the encoded text as the payload verbatim and the task ran
    // against "aHR0cDov..." instead of the URL the operator typed - and
    // nothing on screen said so. Asserting the exact request body is what
    // holds that fix in place.
    const spy = stubServer()
    render(<App />)
    await screen.findByText('hello')

    fireEvent.click(screen.getByText('New Node'))
    fireEvent.change(screen.getByDisplayValue('http://localhost:8080/'), {
      target: { value: 'https://example.test/hook' },
    })
    fireEvent.click(screen.getByText('Deploy Node'))

    await waitFor(() => expect(postCalls(spy).length).toBe(1))
    const body = JSON.parse(postCalls(spy)[0][1].body)
    expect(body.type).toBe('test-host-funcs')
    expect(body.input_text).toBe('https://example.test/hook')
    // The regression in one assertion: an `input` member here would be read
    // verbatim by the server, base64 or not, and the task would run against
    // the wrong string.
    expect(body.input).toBeUndefined()
  })

  it('closes the modal and repaints with the new node', async () => {
    // A create that reported success but left the canvas showing the old graph
    // would read as "the button does nothing". The refetch is the whole reason
    // the node appears before the next SSE frame arrives.
    const spy = stubServer()
    render(<App />)
    await screen.findByText('hello')
    const before = graphFetches(spy)

    fireEvent.click(screen.getByText('New Node'))
    fireEvent.click(screen.getByText('Deploy Node'))

    await waitFor(() => expect(graphFetches(spy)).toBeGreaterThan(before))
    expect(screen.queryByText('Create Workflow Node')).toBeNull()
    expect(screen.getByText('test-host-funcs')).toBeTruthy()
  })

  it('shows the server refusal instead of failing silently', async () => {
    // "I clicked Deploy and nothing happened" is the ticket this prevents. A
    // rejected create has to say the server's own words - the server writes a
    // repair hint into error.message for exactly this reader.
    stubServer({
      createStatus: 422,
      createMessage: 'the workflow definition is not executable. Fix: check the dependency edges.',
    })
    render(<App />)
    await screen.findByText('hello')

    fireEvent.click(screen.getByText('New Node'))
    fireEvent.click(screen.getByText('Deploy Node'))

    await screen.findByText(/not executable/i)
    // The form stays open so the operator can correct the input rather than
    // retyping everything into a dialog that closed on them.
    expect(screen.getByText('Create Workflow Node')).toBeTruthy()
  })
})

describe('node cards', () => {
  it('renders the state the server sent, read from the lower-case key', async () => {
    // task.State is undefined on a JSON payload, so this rendered PENDING for
    // every task forever.
    stubServer({ tasks: [task({ state: 'running' })] })
    render(<App />)

    await screen.findByText('hello')
    await waitFor(() => expect(screen.getByText('RUNNING')).toBeTruthy())
  })

  it('opens the detail panel and decodes a plain-text payload', async () => {
    // input_encoding "text" was run through atob(), which throws
    // InvalidCharacterError on anything that is not base64 - and took the whole
    // panel down with it.
    stubServer()
    render(<App />)

    await screen.findByText('hello')
    fireEvent.click(screen.getByText('hello'))

    await screen.findByText('Task Details')
    expect(screen.getByText(/probe-marker-12345/)).toBeTruthy()
  })

  it('decodes a base64 payload when the server says so', async () => {
    stubServer({
      tasks: [
        task({
          input: btoa('the input side'),
          input_encoding: 'base64',
          result: btoa('the result side'),
          result_encoding: 'base64',
        }),
      ],
    })
    render(<App />)

    await screen.findByText('hello')
    fireEvent.click(screen.getByText('hello'))
    await screen.findByText('Task Details')
    await waitFor(() => {
      expect(screen.getByText('the input side')).toBeTruthy()
      expect(screen.getByText('the result side')).toBeTruthy()
    })
  })

  it('closes the detail panel on Escape, not only on the close button', async () => {
    stubServer()
    render(<App />)

    await screen.findByText('hello')
    fireEvent.click(screen.getByText('hello'))
    await screen.findByText('Task Details')

    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByText('Task Details')).toBeNull())
  })
})

describe('live updates', () => {
  it('paints a task created outside the canvas without a Sync click', async () => {
    // handleUpdate used to map over the nodes it already held, so an event for
    // an unknown task was a no-op: the CLI could start a task and the canvas
    // would sit there empty until someone pressed Sync.
    stubServer()
    render(<App />)
    await screen.findByText('hello')

    serverAcceptsAnotherTask(task({ id: 'task-external', type: 'echo' }))
    await act(async () => {
      live.emit('task.created', task({ id: 'task-external', type: 'echo' }))
    })

    await waitFor(() => expect(screen.getByText('task-external')).toBeTruthy())
  })

  it('updates an existing node in place on a lifecycle event', async () => {
    stubServer({ tasks: [task({ state: 'queued' })] })
    render(<App />)
    await screen.findByText('hello')
    await waitFor(() => expect(screen.getByText('QUEUED')).toBeTruthy())

    await act(async () => {
      live.emit('task.completed', task({ state: 'completed' }))
    })

    await waitFor(() => expect(screen.getByText('COMPLETED')).toBeTruthy())
  })
})