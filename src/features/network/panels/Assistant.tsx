import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Bot, CircleCheck, CircleOff, Info, Loader, Send, Square, TriangleAlert,
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
 * The panel is deliberately spare, because what it is showing is unusual: an answer
 * that arrives over tens of seconds, assembled from lookups the model chose. Three
 * things follow from that and are the reason the markup is not just a text box.
 *
 * Progress is always on screen. A model on CPU takes half a minute to decide it wants
 * to look at one more thing, and a page that shows nothing for that long is
 * indistinguishable from one that has failed — so an operator who thinks it failed
 * reloads and starts the expensive generation again. The status line and the lookup
 * trail exist to make waiting legible.
 *
 * The lookups are listed. An answer that reads confidently and says nothing about what
 * it checked is the specific failure of a small local model, and the only defence is
 * letting the operator see whether it looked at anything at all.
 *
 * A partial answer stays visible when the answer fails. The model's first three
 * sentences were still its first three sentences; hiding them behind the error loses
 * the work and tells the operator nothing about it.
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

  const ask = useCallback(async () => {
    const text = question.trim()
    if (text === '' || asking) return

    setAsking(true)
    setRefusal('')
    setAnswer(emptyAnswer())
    // The history is what came before this question, so it is taken before the new
    // turn is appended. Sending the question twice — once as history and once as the
    // question — would make the model answer an echo of itself.
    const history = historyFor(turns)
    setTurns((current) => [...current, { role: 'user', content: text }])
    setQuestion('')

    const controller = new AbortController()
    inflight.current = controller
    let settled: AssistantAnswer = emptyAnswer()
    try {
      await askAssistant(text, history, (event) => {
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
  }, [asking, question, turns])

  return (
    <div className="model-view">
      <AskPanel
        state={state} report={report} asking={asking} question={question}
        setQuestion={setQuestion} onAsk={() => void ask()}
        onStop={() => inflight.current?.abort()}
      />
      <AnswerPanel answer={answer} asking={asking} refusal={refusal} />
      {report?.configured && <ConversationPanel turns={turns} />}
    </div>
  )
}

/** Where the question is typed, and what is said about it before one is asked. */
function AskPanel({ state, report, asking, question, setQuestion, onAsk, onStop }: {
  state: AssistantState
  report: AssistantReport | null
  asking: boolean
  question: string
  setQuestion: (value: string) => void
  onAsk: () => void
  onStop: () => void
}) {
  return (
    <section className="panel assistant-ask">
      <div className="panel-heading">
        <div>
          <h2>Ask the network</h2>
          <p>It looks things up rather than answering from memory about LoRaWAN generally</p>
        </div>
        <span className="uptime-badge"><Bot size={12} />{ASSISTANT_STATE_HEADLINE[state]}</span>
      </div>

      <p className="agent-action">{ASSISTANT_STATE_ACTION[state]}</p>

      {state === 'ready' && report && (
        <>
          <textarea
            className="assistant-question"
            value={question}
            onChange={(event) => setQuestion(event.target.value)}
            onKeyDown={(event) => {
              // Enter sends, shift+Enter breaks a line. The opposite convention would
              // make a pasted event log impossible to enter, and a question that needs
              // one is the common case here.
              if (event.key === 'Enter' && !event.shiftKey) {
                event.preventDefault()
                onAsk()
              }
            }}
            rows={3}
            spellCheck={false}
            placeholder="Why has tank 3 not reported since this morning?"
            disabled={asking}
          />
          <div className="assistant-ask-row">
            {asking ? (
              <button className="button button-secondary" onClick={onStop}>
                <Square size={14} />Stop
              </button>
            ) : (
              <button
                className="button button-primary" onClick={onAsk}
                disabled={question.trim() === ''}
              >
                <Send size={14} />Ask
              </button>
            )}
            {/* Named on every answer, because the argument for running a model here is
                that nothing leaves the network, and that is only reassuring if the page
                shows where the question went. */}
            <span className="assistant-model">
              {report.model} · <code>{report.endpoint}</code>
            </span>
          </div>

          <ul className="assistant-caveats">
            {assistantCaveats(report).map((note) => (
              <li key={note}><Info size={13} />{note}</li>
            ))}
          </ul>
        </>
      )}
    </section>
  )
}

/**
 * The answer, the lookups behind it, and whatever went wrong.
 *
 * Exported so the states can be rendered and asserted directly: which lines appear in
 * which state is the substance of this panel, and driving it only through the page
 * would mean every assertion ran against the empty initial render.
 */
export function AnswerPanel({ answer, asking, refusal }: {
  answer: AssistantAnswer
  asking: boolean
  refusal: string
}) {
  const nothingYet = answer.status === '' && answer.text === ''
    && answer.tools.length === 0 && answer.error === '' && refusal === ''

  return (
    <section className="panel assistant-answer">
      <div className="panel-heading">
        <div>
          <h2>Answer</h2>
          <p>What it said, and what it read in order to say it</p>
        </div>
        {answer.meta?.model && (
          <span className="uptime-badge"><Bot size={12} />{answer.meta.model}</span>
        )}
      </div>

      {nothingYet && <p className="panel-note"><Info size={13} />Nothing asked yet.</p>}

      {/* The progress line rather than a spinner: it says what the model is doing,
          which is the difference between waiting and wondering whether anything is. */}
      {answer.status !== '' && (
        <p className="assistant-status"><Loader size={13} className="spinning" />{answer.status}</p>
      )}

      {answer.tools.length > 0 && (
        <ul className="assistant-tools">
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
      )}

      {answer.text !== '' && <p className="assistant-text">{answer.text}</p>}

      {/* Both when there are both. A failure after three useful sentences is not a
          failure instead of those sentences, and showing one rather than the other
          loses information the operator would otherwise have to ask for again. */}
      {answer.error !== '' && (
        <p className="form-error" role="alert">
          <TriangleAlert size={13} />{answer.error}
        </p>
      )}
      {refusal !== '' && <p className="form-error" role="alert">{refusal}</p>}

      {answer.done && answer.meta && (
        <p className="panel-note">
          <Info size={13} />
          {answer.meta.tokens > 0
            ? `${answer.meta.tokens} tokens through ${answer.meta.model}`
            : `Answered by ${answer.meta.model || 'the configured model'}`}
          {answer.meta.endpoint ? ` at ${answer.meta.endpoint}` : ''}.
        </p>
      )}

      {asking && answer.text !== '' && (
        <p className="panel-note"><Loader size={13} className="spinning" />Still writing.</p>
      )}
    </section>
  )
}

/** The earlier exchanges, so a follow-up has something to refer to. */
function ConversationPanel({ turns }: { turns: AssistantTurn[] }) {
  if (turns.length === 0) return null

  return (
    <section className="panel assistant-conversation">
      <div className="panel-heading">
        <div>
          <h2>This conversation</h2>
          <p>Sent with each question; the oldest turns are dropped once it grows long</p>
        </div>
      </div>
      <div className="assistant-turns">
        {turns.map((turn, index) => (
          <div className={`assistant-turn is-${turn.role}`} key={`${turn.role}-${index}`}>
            <b>{turn.role === 'user' ? 'You' : 'Assistant'}</b>
            <p>{turn.content}</p>
          </div>
        ))}
      </div>
    </section>
  )
}
