import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import {
  ArrowUp, Bot, CircleCheck, CircleOff, Copy, Info, Loader, RefreshCw, Sparkles,
  Square, TriangleAlert,
} from 'lucide-react'
import {
  ASSISTANT_STATE_ACTION, ASSISTANT_STATE_HEADLINE, assistantCaveats, assistantState,
  applyEvent, askAssistant, emptyAnswer, fetchAssistantReport, historyFor, refusalMessage,
} from '../control/assistant'
import type {
  AssistantAnswer, AssistantReport, AssistantState, AssistantTurn,
} from '../control/assistant'

/**
 * Asking the network a question in plain words.
 *
 * The layout is a conversation: one scrolling thread with the composer pinned beneath
 * it, the way the chat products people already use work. The first version was three
 * stacked sections — question on top, answer in the middle, the exchange below that —
 * and it read as a form that had been cut into pieces. Asking a follow-up meant
 * scrolling past the answer you had just read to find what you had already asked, and
 * the thing you were waiting for sat between the two places you were looking.
 *
 * Three behaviours are load-bearing, and are why this is not a text box over a list.
 *
 * Progress is always visible. A model on CPU takes half a minute to decide it wants to
 * look at one more thing, and a page showing nothing for that long is indistinguishable
 * from one that has failed — so an operator who thinks it failed reloads and restarts
 * the expensive generation. The status line and the lookup trail make waiting legible.
 *
 * The lookups are listed. An answer that reads confidently and says nothing about what
 * it checked is the specific failure of a small local model, and the only defence is
 * letting the operator see whether it looked at anything at all.
 *
 * A partial answer stays visible when the answer fails. The model's first three
 * sentences were still its first three sentences; hiding them behind the error loses the
 * work and tells the operator nothing about it.
 *
 * It cannot change anything. That is enforced on the server, which filters the tools
 * before the model is told any exist, so this page is not the thing keeping a device
 * from being reconfigured — but it says so anyway, because an operator who is not
 * looking for the difference will not find it.
 */
export function Assistant() {
  const [report, setReport] = useState<AssistantReport | null>(null)
  const [loadFailed, setLoadFailed] = useState(false)
  const [loaded, setLoaded] = useState(false)

  const [question, setQuestion] = useState('')
  const [turns, setTurns] = useState<AssistantTurn[]>([])
  const [answer, setAnswer] = useState<AssistantAnswer>(emptyAnswer())
  const [asking, setAsking] = useState(false)
  const [refusal, setRefusal] = useState('')
  // Kept so a failed answer can be retried without retyping the question, which is the
  // difference between shrugging at a timeout and trying again.
  const [lastQuestion, setLastQuestion] = useState('')

  // The in-flight request, so leaving the page or pressing stop actually ends it.
  // Without this the console keeps generating an answer nobody will read, on a
  // machine that is also running a packet forwarder.
  const inflight = useRef<AbortController | null>(null)
  useEffect(() => () => inflight.current?.abort(), [])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const next = await fetchAssistantReport()
        if (!cancelled) setReport(next)
      } catch {
        if (!cancelled) setLoadFailed(true)
      } finally {
        if (!cancelled) { setLoaded(true) }
      }
    })()
    return () => { cancelled = true }
  }, [])

  const outcome = !loaded ? 'checking' : loadFailed ? 'failed' : 'done'
  const state: AssistantState = assistantState(report, outcome)

  // Takes the text rather than reading it from state, so a suggestion chip and a retry
  // can ask something the composer was never given.
  const ask = useCallback(async (text: string) => {
    const trimmed = text.trim()
    if (trimmed === '' || asking) return

    setAsking(true)
    setRefusal('')
    setAnswer(emptyAnswer())
    setLastQuestion(trimmed)
    setQuestion('')
    // The history is what came before this question, so it is taken before the new
    // turn is appended. Sending the question twice — once as history and once as the
    // question — would make the model answer an echo of itself.
    const history = historyFor(turns)
    setTurns((current) => [...current, { role: 'user', content: trimmed }])

    const controller = new AbortController()
    inflight.current = controller
    let settled: AssistantAnswer = emptyAnswer()
    try {
      await askAssistant(trimmed, history, (event) => {
        settled = applyEvent(settled, event)
        setAnswer(settled)
      }, controller.signal)
    } catch (error) {
      // An abort is the operator pressing stop, which is not a fault and needs no
      // message beyond the partial answer already on screen.
      if (!controller.signal.aborted) {
        setRefusal(refusalMessage(error, 'The console could not ask the model.'))
      }
    } finally {
      inflight.current = null
      setAsking(false)
      // Recorded whatever the model got to, not only a finished answer: the next
      // question is asked in the context of what was said, and a truncated answer is
      // still part of what was said.
      const said = settled.text.trim()
      if (said !== '') setTurns((current) => [...current, { role: 'assistant', content: said }])
    }
  }, [asking, turns])

  const stop = useCallback(() => inflight.current?.abort(), [])

  return (
    <div className="chat-surface">
      {state !== 'ready' && <AssistantNotice state={state} report={report} />}
      {state === 'ready' && report && (
        <>
          <Thread
            turns={turns} answer={answer} asking={asking} refusal={refusal}
            report={report} lastQuestion={lastQuestion}
            onRetry={() => void ask(lastQuestion)} onPick={setQuestion}
          />
          <Composer
            question={question} asking={asking} report={report}
            onChange={setQuestion} onAsk={() => void ask(question)} onStop={stop}
          />
        </>
      )}
    </div>
  )
}

/**
 * What to say when the assistant is not there to be asked.
 *
 * Three situations have three different remedies, and a page that renders them alike
 * sends the operator to the wrong place: one needs `.env` edited, one needs a container
 * started, and one needs nothing at all because the check is still running.
 */
export function AssistantNotice({ state, report }: { state: AssistantState; report: AssistantReport | null }) {
  const icon = state === 'checking'
    ? <Loader size={15} className="spinning" />
    : state === 'ready'
      ? <Sparkles size={15} />
      : <TriangleAlert size={15} />

  return (
    <div className="chat-notice">
      <span className={`chat-notice-icon is-${state}`}>{icon}</span>
      <div>
        <h2>{ASSISTANT_STATE_HEADLINE[state]}</h2>
        <p>{ASSISTANT_STATE_ACTION[state]}</p>
        {state !== 'checking' && report && !report.configured && (
          // No remedy is repeated here: ASSISTANT_STATE_ACTION already names the three
          // variables, and the first version of this panel said the same sentence twice
          // in a row, which reads as a page that is not sure what it meant.
          <p className="chat-notice-detail">
            Both are needed. A model with nothing to read answers from general LoRaWAN
            knowledge instead, and that is not distinguishable from a correct answer.
          </p>
        )}
      </div>
    </div>
  )
}

/**
 * The opening state, before anything has been asked.
 *
 * The suggestions are not decoration. The questions worth asking of this network are
 * specific in a way that is not obvious until you have seen one — "which gateways have
 * not reported" beats "is it working", and nobody thinks to type it — so the empty
 * state offers four worth the wait instead of an empty box waiting to be guessed at.
 *
 * Picking one fills the composer rather than sending it, which is where this differs
 * from the chat products it otherwise copies. Their suggestions are the whole question;
 * these are starting points that the operator usually narrows — to one device, one
 * group, one hour — and an answer costs tens of seconds of CPU on a machine that is
 * also running a packet forwarder. One click to edit is cheap; one click to waste forty
 * seconds on the wrong question is not.
 */
const SUGGESTIONS = [
  'Which gateways have not reported recently?',
  'Why has the newest device not joined?',
  'What did the network refuse in the last hour, and why?',
  'Are any devices stuck at a low data rate?',
]

export function EmptyState({ report, onPick }: { report: AssistantReport; onPick: (text: string) => void }) {
  return (
    <div className="chat-empty">
      <span className="chat-empty-mark"><Sparkles size={17} /></span>
      <h2>Ask about this network</h2>
      <p>
        It reads the live network rather than answering from general knowledge about
        LoRaWAN, and it can only read — no frame it queues, no device it changes.
      </p>
      <div className="chat-suggestions">
        {SUGGESTIONS.map((suggestion) => (
          <button className="chat-suggestion" key={suggestion} onClick={() => onPick(suggestion)}>
            {suggestion}
          </button>
        ))}
      </div>
      <ul className="assistant-caveats">
        {assistantCaveats(report).map((note) => (
          <li key={note}><Info size={13} />{note}</li>
        ))}
      </ul>
    </div>
  )
}

/**
 * The thread: what was asked, what was answered, and the answer still arriving.
 *
 * Scrolling holds the one real trap. New tokens have to stay in view or an answer looks
 * like it stopped early, but following the stream when the reader has scrolled up to
 * re-read something earlier yanks them back down mid-sentence. So it follows only while
 * it is already at the bottom — which is what every other chat does, and what makes the
 * behaviour invisible when it is right.
 */
function Thread({ turns, answer, asking, refusal, report, lastQuestion, onRetry, onPick }: {
  turns: AssistantTurn[]
  answer: AssistantAnswer
  asking: boolean
  refusal: string
  report: AssistantReport
  lastQuestion: string
  onRetry: () => void
  onPick: (text: string) => void
}) {
  const scroller = useRef<HTMLDivElement | null>(null)
  // Whether the bottom is where the reader is. Recomputed from the element on scroll
  // rather than assumed, because a reader who scrolled up has to be left alone.
  const atBottom = useRef(true)
  // One string that changes whenever the content could have grown, so the effect below
  // re-runs on any change without a dependency list that reads as though each field
  // were individually significant.
  const growth = `${turns.length}|${answer.text.length}|${answer.status}|${answer.tools.length}|${answer.error}`

  useLayoutEffect(() => {
    const node = scroller.current
    // Layout, not paint: scrolling after the browser has painted the new tokens shows
    // the answer jump, which is the flicker this whole effect exists to avoid.
    if (node && atBottom.current) { node.scrollTop = node.scrollHeight }
  }, [growth])

  const onScroll = useCallback(() => {
    const node = scroller.current
    if (!node) return
    // A tolerance rather than an equality: fractional layout and subpixel scrolling
    // make an exact comparison report "scrolled up" on a thread plainly at its bottom.
    atBottom.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40
  }, [])

  // Anything in flight or on screen means the opening state has been replaced.
  const live = asking || answer.text !== '' || answer.tools.length > 0
    || answer.error !== '' || refusal !== ''

  return (
    <div className="chat-thread" ref={scroller} onScroll={onScroll}>
      {turns.length === 0 && !live && <EmptyState report={report} onPick={onPick} />}

      {turns.map((turn, index) => (
        turn.role === 'user'
          ? (
            <div className="chat-row is-user" key={`u-${index}`}>
              <div className="chat-bubble">{turn.content}</div>
            </div>
          )
          : (
            <div className="chat-row is-assistant" key={`a-${index}`}>
              <span className="chat-avatar"><Bot size={14} /></span>
              <div className="chat-body">
                <AnswerPanel
                  answer={{ ...emptyAnswer(), text: turn.content, done: true }}
                  asking={false} refusal=""
                />
              </div>
            </div>
          )
      ))}

      {live && (
        <div className="chat-row is-assistant">
          <span className="chat-avatar"><Bot size={14} /></span>
          <div className="chat-body">
            <AnswerPanel
              answer={answer} asking={asking} refusal={refusal}
              lastQuestion={lastQuestion} onRetry={onRetry}
            />
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * Where the next question is typed.
 *
 * Pinned under the thread rather than above it, because the question and the answer
 * belong in one line of sight. The first version put the box at the top of the page and
 * the answer below it, so reading a long reply made the composer vanish and asking a
 * follow-up meant scrolling back up to find it again.
 *
 * The stop control lives here and only here. There was one beside the answer as well,
 * and two controls for one action is two chances to press the wrong one — the composer
 * is always on screen, so nothing is gained by repeating it near the text.
 */
export function Composer({ question, asking, report, onChange, onAsk, onStop }: {
  question: string
  asking: boolean
  report: AssistantReport
  onChange: (value: string) => void
  onAsk: () => void
  onStop: () => void
}) {
  const box = useRef<HTMLTextAreaElement | null>(null)

  // Grows with what is typed, up to the cap in the stylesheet. A fixed three-line box
  // hides the start of a pasted event log, which is the one thing operators paste here,
  // and a box that never shrinks again leaves a wall of empty space behind it.
  useEffect(() => {
    const node = box.current
    if (!node) return
    node.style.height = 'auto'
    node.style.height = `${Math.min(node.scrollHeight, 190)}px`
  }, [question])

  const canAsk = question.trim() !== '' && !asking

  return (
    <div className="chat-composer">
      <div className="chat-composer-box">
        <textarea
          className="assistant-question"
          ref={box}
          value={question}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => {
            // Enter sends, shift+Enter breaks a line. The opposite convention would
            // make a pasted event log impossible to enter, and a question that needs
            // one is the common case here.
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              if (canAsk) onAsk()
            }
          }}
          rows={1}
          spellCheck={false}
          placeholder="Ask why something is happening…"
        />
        {asking
          ? (
            <button className="chat-send is-stop" onClick={onStop} title="Stop" aria-label="Stop">
              <Square size={13} />
            </button>
          )
          : (
            <button
              className="chat-send" onClick={onAsk} disabled={!canAsk}
              title="Ask" aria-label="Ask"
            >
              <ArrowUp size={15} />
            </button>
          )}
      </div>
      <p className="chat-composer-foot">
        {/* Named on every question, because the argument for running a model here is
            that nothing leaves the network, and that is only reassuring if the page
            shows where the question went. */}
        <span className="assistant-model">
          <Bot size={11} />{report.model} · <code>{report.endpoint}</code>
        </span>
        <span>Enter to send · shift+Enter for a new line · it can only read</span>
      </p>
    </div>
  )
}

/**
 * The answer, the lookups behind it, and whatever went wrong.
 *
 * Exported so the states can be rendered and asserted directly: which lines appear in
 * which state is the substance of this panel, and driving it only through the page
 * would mean every assertion ran against the empty initial render.
 */
export function AnswerPanel({ answer, asking, refusal, lastQuestion = '', onRetry }: {
  answer: AssistantAnswer
  asking: boolean
  refusal: string
  lastQuestion?: string
  onRetry?: () => void
}) {
  const [copied, setCopied] = useState(false)
  const nothingYet = answer.status === '' && answer.text === ''
    && answer.tools.length === 0 && answer.error === '' && refusal === ''

  const failed = answer.error !== '' || refusal !== ''

  const copy = useCallback(() => {
    // Guarded rather than assumed: the clipboard API is absent over plain http, which
    // is how a console on a LAN is usually reached, and an unhandled rejection here
    // would surface as a console error on a page that is otherwise working.
    void navigator.clipboard?.writeText(answer.text).then(
      () => setCopied(true),
      () => setCopied(false),
    )
  }, [answer.text])

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1600)
    return () => clearTimeout(timer)
  }, [copied])

  return (
    <div className="chat-answer">
      {nothingYet && <p className="panel-note"><Info size={13} />Nothing asked yet.</p>}

      {/* The progress line rather than a spinner: it says what the model is doing,
          which is the difference between waiting and wondering whether anything is. */}
      {answer.status !== '' && (
        <p className="assistant-status"><Loader size={13} className="spinning" />{answer.status}</p>
      )}

      {answer.tools.length > 0 && (
        <details className="assistant-tools" open={asking || !answer.done}>
          {/* Counted in the summary, because the question a reader is asking when this
              is collapsed is "did it look at anything", and that should be answerable
              without expanding it. Built as one string rather than interleaved
              expressions: React separates adjacent text nodes with comment markers in
              server output, which is invisible in a browser and unreadable in a test. */}
          <summary>{`Read ${answer.tools.length} ${answer.tools.length === 1 ? 'thing' : 'things'}`}</summary>
          <ul>
            {answer.tools.map((step, index) => (
              <li key={`${step.name}-${index}`}>
                {step.ok === null
                  ? <Loader size={12} className="spinning" />
                  : step.ok
                    ? <CircleCheck size={12} />
                    : <CircleOff size={12} />}
                <code>{step.name}</code>
                {step.ok === false && <span>could not be read</span>}
              </li>
            ))}
          </ul>
        </details>
      )}

      {answer.text !== '' && (
        <div className="assistant-text">
          {/* Split on blank lines. The model writes paragraphs and a single <p> ran them
              together into one block, which is the difference between an answer that
              reads like an answer and a wall of text. The caret goes inside the last one
              rather than beside it: as a sibling it became its own grid row and dropped
              onto a line of its own mid-sentence. */}
          {answer.text.split(/\n{2,}/).map((block, index, all) => (
            <p key={index}>
              {block}
              {asking && index === all.length - 1 && <span className="chat-caret" aria-hidden="true" />}
            </p>
          ))}
        </div>
      )}

      {/* Both when there are both. A failure after three useful sentences is not a
          failure instead of those sentences, and showing one rather than the other
          loses information the operator would otherwise have to ask for again. */}
      {answer.error !== '' && (
        <p className="form-error" role="alert">
          <TriangleAlert size={13} />{answer.error}
        </p>
      )}
      {refusal !== '' && (
        <p className="form-error" role="alert">
          <TriangleAlert size={13} />{refusal}
        </p>
      )}

      {failed && lastQuestion !== '' && onRetry && (
        <button className="chat-inline-action" onClick={onRetry}>
          <RefreshCw size={11} />Try again
        </button>
      )}

      {answer.done && answer.meta && (
        <div className="chat-answer-foot">
          <span className="panel-note">
            <Info size={12} />
            {answer.meta.tokens > 0
              ? `${answer.meta.tokens} tokens through ${answer.meta.model}`
              : `Answered by ${answer.meta.model || 'the configured model'}`}
            {answer.meta.endpoint ? ` at ${answer.meta.endpoint}` : ''}.
          </span>
          {answer.text !== '' && (
            <button className="chat-inline-action" onClick={copy}>
              {copied ? <CircleCheck size={11} /> : <Copy size={11} />}
              {copied ? 'Copied' : 'Copy'}
            </button>
          )}
        </div>
      )}

      {asking && answer.text !== '' && (
        <p className="panel-note"><Loader size={13} className="spinning" />Still writing.</p>
      )}
    </div>
  )
}
