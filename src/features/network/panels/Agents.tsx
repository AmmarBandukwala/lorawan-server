import { useCallback, useEffect, useState } from 'react'
import {
  Bot, CircleCheck, CircleOff, Info, KeyRound, Network, RefreshCw, ShieldCheck, Terminal,
  TriangleAlert,
} from 'lucide-react'
import { CopyValue } from '../components'
import {
  AGENT_STATE_ACTION, AGENT_STATE_HEADLINE, agentAccessState, fetchAgentAccess, probeSummary,
} from '../control/agentAccess'
import type { AgentAccessReport, AgentAccessState, AgentProbeOutcome } from '../control/agentAccess'
import { failureMessage } from '../control/http'
import {
  AGENT_NEVER_ALLOWED, AGENT_TOOLS, MCP_DEFAULT_HTTP_PORT, MCP_HEALTH_PATH,
  agentEndpointFor, httpAccessSnippet, stdioConfigSnippet,
} from '../constants'

/**
 * Letting an agent talk to the network.
 *
 * This is the only page in the console that describes something it cannot control.
 * The MCP listener is a separate process configured by environment variables: this
 * console cannot start it, stop it, change what it permits, or read its
 * configuration. So the page separates what it measured from what it knows about the
 * software and labels which is which, because an operator who reads a static
 * capability table as live state will believe writes are switched on when they are
 * not.
 *
 * Three things it deliberately does not do:
 *
 * - It never shows a token. The console does not hold the listener's token and must
 *   not — that separation is what stops one leak from opening both the listener and
 *   the network server's API. Generated configs carry an obvious placeholder instead,
 *   because a field that looks finished does not get checked.
 * - It does not claim a state it did not measure. If its own API cannot be reached,
 *   nothing has been established about the listener, and "not running" would be an
 *   invented finding. That is `unknown`, and it has its own words.
 * - It does not present the endpoint as authoritative. Only the browser knows a
 *   hostname its own machine can resolve, and even that is a guess when the listener
 *   sits behind a published port or a proxy.
 */
export function Agents() {
  const [report, setReport] = useState<AgentAccessReport | null>(null)
  const [loadFailed, setLoadFailed] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [loading, setLoading] = useState(false)
  // Whether the first read has come back at all. Distinct from `loading`, which is
  // also true while re-checking: a page that re-announces itself as "checking" on
  // every refresh discards the answer it already has.
  const [loaded, setLoaded] = useState(false)

  // The address an agent on *this machine* would use. Seeded from the browser, not
  // from the API: the console API knows the listener's container address
  // (http://mcp:8092), which nothing outside the Docker network can resolve. Guarded
  // because this module is also rendered server-side for the smoke check.
  const [host, setHost] = useState(
    () => (typeof window === 'undefined' ? '' : window.location.hostname),
  )
  const [port, setPort] = useState(MCP_DEFAULT_HTTP_PORT)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setReport(await fetchAgentAccess())
      setLoadFailed(false)
      setLoadError('')
    } catch (error) {
      // The console could not ask. Nothing is known about the listener, which is a
      // different situation from the listener being down and needs saying plainly:
      // an operator told "not running" here debugs the wrong container.
      setReport(null)
      setLoadFailed(true)
      setLoadError(failureMessage(error, 'The console could not answer its own check.'))
    } finally {
      setLoading(false)
      setLoaded(true)
    }
  }, [])

  useEffect(() => {
    const first = setTimeout(() => void load(), 0)
    return () => clearTimeout(first)
  }, [load])

  // Three mutually exclusive outcomes rather than two booleans, because the
  // combination `loadFailed && report` is meaningless and a pair of flags lets you
  // build it. `loaded` gates the first one: while the very first read is outstanding
  // the page has no answer to show, and "cannot be checked right now" would be a
  // claim about a check that has not run.
  const outcome: AgentProbeOutcome = !loaded ? 'checking' : loadFailed ? 'failed' : 'done'
  const state = agentAccessState(report, outcome)

  return (
    <div className="model-view">
      <StatusPanel
        state={state} report={report} loadError={loadError}
        loading={loading} onRecheck={() => void load()}
      />
      <ConnectPanel host={host} setHost={setHost} port={port} setPort={setPort} />
      <CapabilityPanel />
      <LimitsPanel />
    </div>
  )
}

/**
 * Whether a listener is answering, in as few words as possible.
 *
 * The headline, the action and the pill colour all come from lookups keyed on the
 * state rather than from chains of conditionals, so adding a state is a compile error
 * in three places instead of a page that falls through to whichever string or colour
 * happens to be last.
 *
 * Exported so the states can be rendered and asserted directly: which lines appear in
 * which state is the substance of this panel, and driving it only through the page
 * would mean every assertion ran against the pre-fetch render.
 */
export function StatusPanel({ state, report, loadError, loading, onRecheck }: {
  state: AgentAccessState
  report: AgentAccessReport | null
  loadError: string
  loading: boolean
  onRecheck: () => void
}) {
  // Five states, four visually distinct treatments, because two of them ask the same
  // of the operator. "Not configured" and "still checking" are both quiet grey;
  // "configured but not answering" is the console's amber, the colour it already uses
  // for a setting that carries a consequence; and "cannot be established" is grey with
  // a dashed edge, since it is the absence of a finding rather than one.
  const tone: Record<AgentAccessState, string> = {
    checking: 'state-offline',
    'not-configured': 'state-offline',
    reachable: 'state-online',
    unreachable: 'state-alert',
    unknown: 'state-unknown',
  }

  return (
    <section className="panel agent-status">
      <div className="panel-heading">
        <div>
          <h2>Agent access</h2>
          <p>Whether an agent can reach this network through the MCP listener</p>
        </div>
        <button className="button button-secondary" onClick={onRecheck} disabled={loading}>
          <RefreshCw size={15} className={loading ? 'spinning' : ''} />Check again
        </button>
      </div>

      <div className="agent-state-row">
        <span className={`state-pill ${tone[state]}`}><i />{AGENT_STATE_HEADLINE[state]}</span>
        {report?.endpoint && <code>{report.endpoint}</code>}
      </div>

      <p className="agent-action">{AGENT_STATE_ACTION[state]}</p>

      {loadError && <p className="form-error" role="alert">{loadError}</p>}

      {/* The listener's own complaint, in the console's failure red — but only when
          something was configured and failed. In the not-configured state the API's
          detail is guidance ("set MCP_URL…"), and rendering guidance in the colour of
          a fault under a calm grey pill contradicts the pill. */}
      {state === 'unreachable' && report?.detail && (
        <p className="form-error" role="alert">{report.detail}</p>
      )}

      {/* Only say a check ran when one did. With no listener configured there is no
          address and nothing was probed, so the sentence would be invented. */}
      {report?.endpoint && (
        <p className="panel-note"><Info size={13} />{probeSummary(report, MCP_HEALTH_PATH)}</p>
      )}

      {!report && !loadError && (
        <p className="panel-note"><Info size={13} />Checking…</p>
      )}
    </section>
  )
}

/**
 * The two ways an agent gets here, with the address built in front of the operator.
 *
 * The host field is editable rather than fixed because the console API and the
 * browser see different addresses and only one of them is usable from outside. The
 * API's value is correct for the check it performs and useless for a config an
 * operator pastes on a laptop; the browser's value is the reverse. Showing the
 * derived endpoint beside an editable field makes it obvious which one was guessed.
 */
function ConnectPanel({ host, setHost, port, setPort }: {
  host: string
  setHost: (value: string) => void
  port: string
  setPort: (value: string) => void
}) {
  const endpoint = agentEndpointFor(host, port)

  return (
    <section className="panel connect-panel">
      <div className="panel-heading">
        <div>
          <h2>Connect an agent</h2>
          <p>Either the agent host spawns the server as a child process, or it connects over the network</p>
        </div>
      </div>

      <div className="agent-address">
        <label>
          <span>Host an agent can reach</span>
          <input value={host} onChange={(event) => setHost(event.target.value)}
            placeholder="192.168.1.20 or node.example" spellCheck={false} />
        </label>
        <label>
          <span>Port</span>
          <input value={port} onChange={(event) => setPort(event.target.value)}
            inputMode="numeric" spellCheck={false} />
        </label>
      </div>
      <p className="panel-note">
        <Info size={13} />
        Seeded from the address you are reading this on. Only your machine knows which
        hostname it can resolve, so check it — the listener may be reached through a
        published port, a reverse proxy, or a different host entirely.
      </p>

      <div className="connect-grid agent-connect-grid">
        <div className="connect-card">
          <div className="connect-card-top"><Terminal size={15} />Spawned by the agent host</div>
          <CopyValue
            label="the stdio configuration"
            note="For a host that starts the server as a child process. Nothing listens on a port."
            multiline
            value={stdioConfigSnippet('fieldnode-mcp', 'http://HOST:8091', 'http://HOST:8090')}
          />
          <p className="connect-note">
            <Info size={13} />
            The most contained option, and the default: whoever can start the process can
            use the token, so there is no second access control to get wrong.
          </p>
        </div>

        <div className="connect-card">
          <div className="connect-card-top"><Network size={15} />Over the network</div>
          <CopyValue
            label="the network endpoint and scheme"
            note="HTTP-capable hosts differ in config shape, so these are the two fixed facts rather than one host's file format."
            multiline
            value={httpAccessSnippet(endpoint)}
          />
          <p className="connect-warning">
            <TriangleAlert size={13} />
            Plain HTTP. A bearer token sent in the clear is readable by anyone who can
            see the wire, so the listener is published on loopback by default — put TLS
            in front of it before binding it to a network address.
          </p>
        </div>
      </div>

      <p className="form-note">
        <KeyRound size={13} />
        Both configs carry a placeholder where a token belongs, and this console will
        never fill it in. It does not hold either token and must not: one secret guarding
        both this listener and the network server's API means one leak opens both, and
        neither can be rotated without the other. Copy the config, then paste in
        MCP_AUTH_TOKEN and NS_API_TOKEN from the deployment's environment.
      </p>
    </section>
  )
}

/**
 * What an agent is able to ask for, and what no deployment can turn on.
 *
 * This table describes **the software**, not this deployment. Which tools are
 * actually live is decided by MCP_PROFILE in an environment this console cannot
 * read, so the panel says that in the same breath as the list — an operator who
 * reads "patch_device" here and does not read the caption will conclude an agent can
 * reconfigure their fleet tonight.
 *
 * The names are pinned against the Go server by a test in the mcp module that reads
 * this file as text, because the console and the MCP server share no code: a renamed
 * tool would otherwise leave this table describing something that no longer exists,
 * on the one page whose job is saying what an agent may do.
 */
function CapabilityPanel() {
  const reads = AGENT_TOOLS.filter((tool) => !tool.writes)
  const writes = AGENT_TOOLS.filter((tool) => tool.writes)

  return (
    <section className="panel model-panel">
      <div className="panel-heading">
        <div>
          <h2>What an agent can ask for</h2>
          <p>Every tool this build of the MCP server offers, and whether it changes anything</p>
        </div>
        <span className="uptime-badge">
          <ShieldCheck size={12} />{reads.length} reads · {writes.length} writes
        </span>
      </div>

      <div className="model-list">
        {AGENT_TOOLS.map((tool) => (
          <div className="model-row agent-tool-row" key={tool.name}>
            <span className="model-row-main">
              <b>{tool.name}</b>
              <small>{tool.summary}</small>
            </span>
            <span className={`model-codec ${tool.writes ? 'model-codec-writes' : 'model-codec-on'}`}>
              {tool.writes ? <TriangleAlert size={12} /> : <CircleCheck size={12} />}
              {tool.writes ? 'Changes the network' : 'Read only'}
            </span>
          </div>
        ))}
      </div>

      <p className="form-note">
        <Info size={13} />
        This is what the software offers, not what is switched on here. Writes exist in
        the `operate` profile and in nothing else, and the profile is set in the MCP
        service's environment — which this console cannot read, so it cannot tell you
        which one is running. Check `MCP_PROFILE` where the service is configured.
      </p>

      <div className="console-subheading">
        <b>Offered by no profile, whatever the deployment sets</b>
        <span>
          Listed rather than left out, because an operator who cannot find these will
          assume they exist under another name and go looking for them.
        </span>
      </div>
      <div className="model-list">
        {AGENT_NEVER_ALLOWED.map((item) => (
          <div className="model-row agent-never-row" key={item.what}>
            <span className="model-row-main">
              <b>{item.what}</b>
              <small>{item.why}</small>
            </span>
            <span className="model-codec model-codec-none">
              <CircleOff size={12} />Not available
            </span>
          </div>
        ))}
      </div>
    </section>
  )
}

/**
 * The caveats an operator needs before trusting an agent, in the console's own words.
 *
 * Everything here is a limit of the protocol or of this design rather than a fault to
 * fix, and all of it is the kind of thing that only becomes visible when it goes
 * wrong. Saying it on the page that invites the operator to connect an agent is the
 * only place it will be read.
 */
function LimitsPanel() {
  return (
    <section className="panel connect-panel">
      <div className="panel-heading">
        <div>
          <h2>Before you connect one</h2>
          <p>Four things about this that are not the way a normal API behaves</p>
        </div>
        <span className="uptime-badge"><Bot size={12} />Read these first</span>
      </div>

      <ol className="connect-steps">
        <li>
          <b>Nothing here forces a human to approve anything</b>
          <span>
            The read-only and destructive markers a tool carries are hints, and the
            Model Context Protocol cannot make an agent host ask permission — it can
            only tell the host that it probably should. What actually refuses a request
            is which tools exist at all. If writes must need a person, run the read
            profile and configure the host to confirm every call.
          </span>
        </li>
        <li>
          <b>One token is full power within its profile</b>
          <span>
            There is no per-device or per-group restriction. An agent holding the token
            can read every device on this network and, under `operate`, change any one
            of them. Do not hand it to an agent you would not give a read-only database
            credential for the whole facility.
          </span>
        </li>
        <li>
          <b>A downlink is queued, never sent</b>
          <span>
            A class-A device listens for about two seconds after it transmits and then
            goes quiet until it next speaks, so a frame waits in the device's single
            pending slot until then — possibly hours. The server answers 202 because it
            accepted the frame, not because anything was transmitted, and a retry is
            refused while the first one still waits.
          </span>
        </li>
        <li>
          <b>Every write is recorded, and not by this console</b>
          <span>
            The MCP process logs one line per write with the tool, the device and the
            reason the agent gave. Those lines are in that container's logs, not in this
            console's event feed, so an audit means looking at both.
          </span>
        </li>
      </ol>
    </section>
  )
}
