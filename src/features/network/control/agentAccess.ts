/**
 * What the console knows about the MCP listener, and how to say it.
 *
 * The listener is a separate process configured by environment variables, so this
 * console cannot change it and cannot read its settings. What it *can* do is ask
 * whether one is answering, which the API does through `GET /api/v1/mcp`. That is
 * the only live fact on the Agents page; everything else on it describes the
 * software rather than the deployment, and the page keeps those two apart because an
 * operator who reads a static table as live state will trust it wrongly.
 */

import { request } from './http'

/** What `GET /api/v1/mcp` answers. */
export type AgentAccessReport = {
  /** Whether this console was told where a listener is at all. */
  configured: boolean
  /** Whether it answered the health check just now. */
  reachable: boolean
  /** The address that was probed, so a wrong one is visible rather than guessed. */
  endpoint: string
  /** Why not, when it is not reachable. Empty when it is. */
  detail: string
  /** When the check ran, from the server, so the page does not claim a time it did not measure. */
  checked_at: string
}

export async function fetchAgentAccess(): Promise<AgentAccessReport> {
  return request<AgentAccessReport>('GET', '/mcp')
}

/**
 * The five states, in the words the page uses.
 *
 * `checking` is the state before the first read has come back, and it exists because
 * the alternative is worse: with no report yet the page would fall through to
 * `unknown` and announce "cannot be checked right now" about a check it has not
 * attempted. Every other panel in this console renders a loading state for the same
 * reason.
 *
 * `unknown` is then a real finding rather than a failure of the check: if the console
 * API itself cannot be reached, nothing has been established about the listener, and
 * saying "not running" would be inventing one. The distinction matters because the
 * operator's next action differs completely — one sends them to the MCP container,
 * the other to the console's own logs.
 */
export type AgentAccessState =
  | 'checking' | 'not-configured' | 'reachable' | 'unreachable' | 'unknown'

/** Whether the read is outstanding, came back, or never arrived. */
export type AgentProbeOutcome = 'checking' | 'done' | 'failed'

export function agentAccessState(
  report: AgentAccessReport | null,
  outcome: AgentProbeOutcome,
): AgentAccessState {
  if (outcome === 'checking') return 'checking'
  if (outcome === 'failed' || !report) return 'unknown'
  if (!report.configured) return 'not-configured'
  return report.reachable ? 'reachable' : 'unreachable'
}

/**
 * The heading for each state.
 *
 * Written as a lookup rather than nested conditionals so TypeScript checks that
 * every state has words: a new state would otherwise fall through to whichever
 * string happens to be the default, and a page about whether something is running
 * is the last place to be confidently wrong.
 */
export const AGENT_STATE_HEADLINE: Record<AgentAccessState, string> = {
  checking: 'Checking…',
  'not-configured': 'Not enabled on this deployment',
  reachable: 'Running and reachable',
  unreachable: 'Configured, but not answering',
  unknown: 'Cannot be checked right now',
}

/**
 * What to do next, per state. A status with no action attached is a shrug.
 */
export const AGENT_STATE_ACTION: Record<AgentAccessState, string> = {
  checking: 'Asking the console API whether a listener answers its health check.',
  'not-configured': 'Start the service with `docker compose --profile mcp up -d`, then '
    + 'set MCP_URL on the console API so it can check.',
  reachable: 'Connect an agent using one of the configurations below.',
  unreachable: 'Check that the service is running and that MCP_URL points at the port '
    + 'it is published on.',
  unknown: 'The console could not reach its own API, so nothing has been established '
    + 'about the listener. Resolve that first — this page is not reporting a failure '
    + 'of the MCP service.',
}

/**
 * How the check ran, as one line.
 *
 * Names the path that was probed. An operator debugging a misconfiguration needs to
 * know the console asked for `/healthz` on a particular address, because the two
 * common mistakes — wrong port, wrong path — are only visible if both are stated.
 */
export function probeSummary(report: AgentAccessReport, healthPath: string): string {
  const when = report.checked_at ? new Date(report.checked_at) : null
  const stamp = when && !Number.isNaN(when.getTime())
    ? when.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    : 'just now'
  return `Checked ${healthPath} on ${report.endpoint || 'no address'} at ${stamp}.`
}
