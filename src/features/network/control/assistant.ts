/**
 * Asking the assistant about the network, and reading its answer as it arrives.
 *
 * Two things here are worth more than the panel that uses them.
 *
 * The first is the SSE parser. A streamed response arrives in chunks that have no
 * relation to frame boundaries: one network read can carry three events, and an
 * event can be split in the middle of its JSON. A parser that assumes `data:` lines
 * arrive whole works in every test with a mocked reader and then drops half an answer
 * over a real socket, where the split lands wherever TCP feels like. So the parser is
 * incremental and keeps its own buffer, and that is what the checks exercise.
 *
 * The second is that an unfinished answer is never presented as a finished one. A
 * model that fails halfway leaves text on the screen that reads like a conclusion;
 * `error` and `done` are therefore separate fields rather than inferred from whether
 * text arrived, and the panel shows both.
 */

import { API_PREFIX } from '../constants'

/** What `GET /api/v1/assistant` answers. */
export type AssistantReport = {
  /** Whether a model and a listener are both configured on the console API. */
  configured: boolean
  /** Which model answers, as the console was told to name it. */
  model: string
  /** Where the question is sent. Shown so a local model's claim can be checked. */
  endpoint: string
  /** Whether the console read the event vocabulary from the listener. */
  has_vocabulary: boolean
  /** Why not, when it is not configured. Empty when it is. */
  detail: string
}

/** One thing that happened while a question was being answered. */
export type AssistantEvent = {
  type: 'status' | 'tool_call' | 'tool_result' | 'token' | 'error' | 'done'
  text?: string
  tool?: string
  ok?: boolean
  model?: string
  endpoint?: string
  tokens?: number
}

export async function fetchAssistantReport(): Promise<AssistantReport> {
  const response = await fetch(`${API_PREFIX}/assistant`)
  if (!response.ok) throw new Error(`assistant status returned ${response.status}`)
  return (await response.json()) as AssistantReport
}

/**
 * The four states of the configuration, with the same shape as the listener's.
 *
 * `unreachable` here means the console's own API did not answer, which is different
 * from the listener being down: the assistant is part of this process, so there is no
 * third service to be at fault. Collapsing the two would send an operator to the MCP
 * container for a console that is simply not running.
 */
export type AssistantState = 'checking' | 'not-configured' | 'ready' | 'unreachable'

export type AssistantProbeOutcome = 'checking' | 'done' | 'failed'

export function assistantState(
  report: AssistantReport | null,
  outcome: AssistantProbeOutcome,
): AssistantState {
  if (outcome === 'checking') return 'checking'
  if (outcome === 'failed' || !report) return 'unreachable'
  return report.configured ? 'ready' : 'not-configured'
}

export const ASSISTANT_STATE_HEADLINE: Record<AssistantState, string> = {
  checking: 'Checking…',
  'not-configured': 'Not enabled on this deployment',
  ready: 'Ready',
  unreachable: 'Cannot be checked right now',
}

export const ASSISTANT_STATE_ACTION: Record<AssistantState, string> = {
  checking: 'Asking the console API whether a model is configured.',
  'not-configured': 'Set LLM_BASE_URL and LLM_MODEL on the console API, and MCP_URL so '
    + 'it has something to read, then restart it.',
  ready: 'Ask in plain words. It looks things up rather than answering from memory.',
  unreachable: 'The console could not reach its own API, so nothing has been established '
    + 'about the assistant.',
}

/**
 * What the console can and cannot promise about an answer.
 *
 * The vocabulary matters more than it sounds: without it the model has the reason
 * strings second-hand and will paraphrase them, so an operator who copies the
 * sentence into a search finds nothing. Saying so once, before the first question, is
 * cheaper than having someone debug a wrong conclusion.
 */
export function assistantCaveats(report: AssistantReport): string[] {
  const notes = [
    'It can only read. It cannot queue a frame, change a device or take one off the '
      + 'network, whatever the question asks for.',
    'A model that has not looked something up will still answer. If the answer does not '
      + 'mention what it read, ask it what it checked.',
  ]
  if (!report.has_vocabulary) {
    notes.push('The event vocabulary could not be read from the listener, so reason '
      + 'strings will be paraphrased rather than quoted. Check MCP_URL and MCP_AUTH_TOKEN.')
  }
  return notes
}

/**
 * An incremental server-sent-events reader.
 *
 * Feed it whatever the socket hands over and it returns the events that became
 * complete. Everything after the last blank line stays buffered, because that text is
 * not an event yet — it is the beginning of one, and the rest may arrive in the next
 * read or in three reads' time.
 *
 * A parser that split on newlines and assumed each chunk ended on a boundary would
 * work against every fake reader and fail on a real one, which is the worst possible
 * distribution for a test to have.
 */
export function createEventReader(): (chunk: string) => AssistantEvent[] {
  let buffer = ''

  return (chunk: string) => {
    buffer += chunk
    const events: AssistantEvent[] = []

    // A blank line terminates a frame. Splitting on it rather than scanning line by
    // line means an incomplete final frame is left in the buffer by construction
    // rather than by remembering to.
    let boundary = buffer.indexOf('\n\n')
    while (boundary >= 0) {
      const frame = buffer.slice(0, boundary)
      buffer = buffer.slice(boundary + 2)
      const parsed = parseFrame(frame)
      if (parsed) events.push(parsed)
      boundary = buffer.indexOf('\n\n')
    }
    return events
  }
}

/** One complete frame's text, which may hold several `data:` lines. */
function parseFrame(frame: string): AssistantEvent | null {
  const data: string[] = []
  for (const line of frame.split('\n')) {
    // A colon-prefixed line is a comment, used for keepalives. Ignoring it is
    // correct; failing on it would end a long answer the first time a proxy pings.
    if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
  }
  if (data.length === 0) return null

  const payload = data.join('\n')
  try {
    return JSON.parse(payload) as AssistantEvent
  } catch {
    // A frame that is not JSON is a proxy or a browser extension having rewritten the
    // stream. Dropping one frame costs a status line; throwing would discard the
    // answer the operator has been waiting a minute for.
    return null
  }
}

/** One entry in the trail of lookups the model performed. */
export type ToolStep = {
  name: string
  /** Whether it came back, or null while it is still outstanding. */
  ok: boolean | null
}

/** The answer as the panel holds it. */
export type AssistantAnswer = {
  /** The latest progress line, so a slow model never looks like a hung one. */
  status: string
  /** Everything the model has said so far. */
  text: string
  /** What it looked up, in order, with each one's outcome. */
  tools: ToolStep[]
  /** Why it stopped, when it stopped for a reason other than finishing. */
  error: string
  /** Which model answered and from where, once it has finished. */
  meta: { model: string; endpoint: string; tokens: number } | null
  done: boolean
}

export const emptyAnswer = (): AssistantAnswer => ({
  status: '', text: '', tools: [], error: '', meta: null, done: false,
})

/**
 * Fold one event into the answer.
 *
 * Pure and returning a new object, because React needs the identity to change and
 * because the whole interesting behaviour of this feature — what a failure does to the
 * text already on screen — is then assertable without a DOM or a socket.
 */
export function applyEvent(answer: AssistantAnswer, event: AssistantEvent): AssistantAnswer {
  switch (event.type) {
    case 'status':
      return { ...answer, status: event.text ?? answer.status }

    case 'token':
      // Appended, not replaced: the stream carries the answer in pieces and each one
      // is real text the model wrote.
      return { ...answer, text: answer.text + (event.text ?? ''), status: '' }

    case 'tool_call':
      return {
        ...answer,
        status: '',
        tools: [...answer.tools, { name: event.tool ?? '?', ok: null }],
      }

    case 'tool_result': {
      // The result names the tool rather than an index, so the last outstanding call
      // for that name is the one it answers.
      const tools = [...answer.tools]
      for (let i = tools.length - 1; i >= 0; i--) {
        if (tools[i].name === event.tool && tools[i].ok === null) {
          tools[i] = { ...tools[i], ok: event.ok !== false }
          break
        }
      }
      return { ...answer, tools }
    }

    case 'error':
      // The text stays. A model that said three useful sentences and then lost the
      // runtime has still said three useful sentences, and throwing them away to show
      // only the failure loses the work while telling the operator nothing about it.
      return { ...answer, error: event.text ?? 'the assistant stopped without a reason', status: '' }

    case 'done':
      return {
        ...answer,
        done: true,
        status: '',
        meta: {
          model: event.model ?? '', endpoint: event.endpoint ?? '', tokens: event.tokens ?? 0,
        },
      }

    default:
      // An event type this build does not know is ignored rather than fatal: the
      // console and the API are built from the same repository but deployed
      // independently, and a new event kind must not blank an answer.
      return answer
  }
}

/** A turn the page sends back as context on the next question. */
export type AssistantTurn = { role: 'user' | 'assistant'; content: string }

/**
 * The history to send with a question.
 *
 * Capped at the API's own limit and from the front rather than the end: the API
 * refuses more than it accepts, and a conversation that outgrows the cap should lose
 * its oldest exchange, which is the one the model is least likely to be relying on.
 * The assistant trims again on the server and says so in its prompt; this only stops
 * the request being refused outright.
 */
export const MAX_HISTORY_TURNS = 40

export function historyFor(turns: AssistantTurn[]): AssistantTurn[] {
  const usable = turns.filter((turn) => turn.content.trim() !== '')
  return usable.length > MAX_HISTORY_TURNS ? usable.slice(-MAX_HISTORY_TURNS) : usable
}

/** A question the console refused before asking the model. */
export class AssistantRefused extends Error {
  readonly status: number
  readonly code: string
  readonly detail: string

  constructor(status: number, code: string, detail: string) {
    super(detail || code)
    this.status = status
    this.code = code
    this.detail = detail
  }
}

/**
 * Ask one question and stream the answer.
 *
 * `onEvent` is called for every event as it arrives, not once at the end: the entire
 * reason this endpoint streams is that a model on CPU takes tens of seconds, and a
 * panel that fills in at the end is indistinguishable from one that has failed.
 *
 * The signal is the caller's, so closing the panel or reloading the page stops the
 * generation instead of leaving the server computing an answer nobody will read.
 */
export async function askAssistant(
  question: string,
  history: AssistantTurn[],
  onEvent: (event: AssistantEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch(`${API_PREFIX}/assistant/ask`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify({ question, history }),
    signal,
  })

  // A rejection here happened before any streaming started, so it is a normal JSON
  // error body and there is nothing on screen to preserve.
  if (!response.ok) {
    let code = 'assistant_failed'
    let detail = ''
    try {
      const body = await response.json() as { error?: string; detail?: string }
      code = body.error ?? code
      detail = body.detail ?? ''
    } catch {
      // A body that is not JSON is a proxy answering instead of the console. The
      // status is then the only fact worth reporting, and it is already in the throw.
    }
    throw new AssistantRefused(response.status, code, detail)
  }
  if (!response.body) throw new Error('the browser gave no response body to read')

  const reader = response.body.getReader()
  const decode = new TextDecoder()
  const read = createEventReader()

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    for (const event of read(decode.decode(value, { stream: true }))) onEvent(event)
  }
  // Anything left after the stream closed is a frame the server wrote without its
  // terminating blank line. It is still worth surfacing: an answer cut off by a proxy
  // timeout arrives exactly this way, and dropping it silently is how a truncated
  // sentence gets read as a complete one.
  for (const event of read('\n\n')) onEvent(event)
}

/**
 * What to show when a question failed before any of it streamed.
 *
 * The two codes worth distinguishing are the ones an operator can act on and that
 * nothing else on the page reports: the assistant being switched off, and the browser
 * having sent something the console refuses. Everything else keeps the console's own
 * sentence, which is written for the person who has to fix it.
 *
 * A plain error keeps its own message, as `failureMessage` does elsewhere: "Failed to
 * fetch" names a browser that could not reach the console at all, which is a different
 * diagnosis from any code the console could have sent, and replacing it with a generic
 * string would lose the only fact the page has.
 */
export function refusalMessage(error: unknown, fallback: string): string {
  if (!(error instanceof AssistantRefused)) {
    return error instanceof Error && error.message.trim() !== '' ? error.message : fallback
  }
  switch (error.code) {
    case 'assistant_not_configured':
      return 'No model is configured on this console, so there is nothing to ask. '
        + 'Set LLM_BASE_URL and LLM_MODEL, and MCP_URL so it can read the network.'
    case 'assistant_streaming_unsupported':
      return 'Something between this page and the console is buffering the response, '
        + 'which would deliver the answer in one piece after a minute of blank screen. '
        + 'Turn off proxy buffering for /api/v1/assistant.'
    case 'history_roles_must_be_user_or_assistant':
      return 'The conversation carried a turn this page does not write. Reload the page '
        + 'and ask again — something has altered the request.'
    default:
      return error.detail || error.code
  }
}
