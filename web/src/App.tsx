import { useState, useEffect, useCallback, useRef } from 'react'
import './app.css'

// ── Types ─────────────────────────────────────────────────────────────────────

type Level = 'info' | 'warn' | 'error' | 'success'

type LogLine = {
  id: number
  ts: string
  job_id: string
  level: Level
  step: string
  message: string
  data?: Record<string, unknown>
  service?: string  // Added to identify which service
}

type ServiceStatus = {
  name: string
  desc: string
  status: 'idle' | 'working' | 'error'
  lastLog?: string
  lastUpdate?: number
}

type ScreenStatus = {
  name: string
  platform: string
  status: 'pending' | 'running' | 'done'
  score: number
  iteration: number
}

type JobState = {
  id: string
  figma_url: string
  platforms: string[]
  status: 'queued' | 'running' | 'done' | 'failed'
  screens: ScreenStatus[]
  avgScore: number
  totalIter: number
}

type ForgeEvent = {
  routing_key: string
  ts: string
  payload: Record<string, unknown>
}

// ── API ───────────────────────────────────────────────────────────────────────

const API = import.meta.env.VITE_API_URL ?? 'http://localhost:8080'
const WS  = import.meta.env.VITE_WS_URL  ?? 'ws://localhost:8080/ws'

// Map steps to services
const STEP_TO_SERVICE: Record<string, string> = {
  'parse_figma': 'figma-parser',
  'codegen': 'codegen',
  'sandbox': 'sandbox',
  'screenshot': 'differ',
  'diff': 'differ',
  'diff_result': 'differ',
  'job_submitted': 'gateway',
  'screen_passed': 'orchestrator',
  'job_done': 'orchestrator',
  'job_failed': 'orchestrator',
}

async function createJob(body: {
  figma_url: string
  repo_url?: string
  platforms: string[]
  styling: string
  threshold: number
}) {
  const r = await fetch(`${API}/api/jobs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!r.ok) throw new Error(await r.text())
  return r.json() as Promise<{ job_id: string }>
}

// ── App ───────────────────────────────────────────────────────────────────────

export default function App() {
  // Form
  const [figmaURL, setFigmaURL]     = useState('')
  const [repoURL, setRepoURL]       = useState('')
  const [platforms, setPlatforms]   = useState<string[]>(['react', 'kmp'])
  const [styling, setStyling]       = useState('tailwind')
  const [threshold, setThreshold]   = useState(95)

  // State
  const [connected, setConnected]   = useState(false)
  const [logs, setLogs]             = useState<LogLine[]>([])
  const [jobs, setJobs]             = useState<JobState[]>([])
  const [activeJobID, setActiveJobID] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [activeStep, setActiveStep] = useState('')
  const [logFilter, setLogFilter]   = useState<'all' | string>('all')
  const [services, setServices]     = useState<Record<string, ServiceStatus>>({
    'gateway':      { name: 'gateway',      desc: 'API + WS',      status: 'idle' },
    'orchestrator': { name: 'orchestrator', desc: 'State machine', status: 'idle' },
    'figma-parser': { name: 'figma-parser', desc: 'Figma API',     status: 'idle' },
    'codegen':      { name: 'codegen',      desc: 'Claude API',    status: 'idle' },
    'sandbox':      { name: 'sandbox',      desc: 'Docker runner', status: 'idle' },
    'differ':       { name: 'differ',       desc: 'Pixel diff',    status: 'idle' },
    'notifier':     { name: 'notifier',     desc: 'Telegram',      status: 'idle' },
  })

  const logRef   = useRef<HTMLDivElement>(null)
  const lineID   = useRef(0)
  const wsRef    = useRef<WebSocket | null>(null)

  // ── WebSocket ──────────────────────────────────────────────────────────────

  const connectWS = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return
    const ws = new WebSocket(WS)
    wsRef.current = ws

    ws.onopen = () => { setConnected(true); console.log('[forge] WS connected') }

    ws.onmessage = (e) => {
      try {
        const ev: ForgeEvent = JSON.parse(e.data)
        handleEvent(ev)
      } catch {}
    }

    ws.onclose = () => {
      setConnected(false)
      setTimeout(connectWS, 3000)
    }

    ws.onerror = () => ws.close()
  }, [])

  useEffect(() => {
    connectWS()
    return () => wsRef.current?.close()
  }, [connectWS])

  // Auto-scroll log
  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight, behavior: 'smooth' })
  }, [logs])

  // ── Event handler ──────────────────────────────────────────────────────────

  const handleEvent = useCallback((ev: ForgeEvent) => {
    const key = ev.routing_key
    const p   = ev.payload

    // Log lines
    if (key.startsWith('log.')) {
      const step = (p.step as string) ?? key
      const line: LogLine = {
        id: lineID.current++,
        ts: new Date(ev.ts).toLocaleTimeString('en', { hour12: false }),
        job_id: (p.job_id as string) ?? '',
        level: (p.level as Level) ?? 'info',
        step: step,
        message: (p.message as string) ?? '',
        data: p.data as Record<string, unknown>,
        service: STEP_TO_SERVICE[step],
      }
      setLogs(prev => [...prev.slice(-500), line])
      setActiveStep(step)

      // Update service status
      const service = STEP_TO_SERVICE[step]
      if (service) {
        setServices(prev => ({
          ...prev,
          [service]: {
            ...prev[service],
            status: line.level === 'error' ? 'error' : 'working',
            lastLog: line.message,
            lastUpdate: Date.now(),
          }
        }))
      }
    }

    if (key === 'job.submitted' || (key === 'log.event' && p.step === 'job_submitted')) {
      const jobID = p.job_id as string
      setActiveJobID(jobID)
      setJobs(prev => {
        const exists = prev.find(j => j.id === jobID)
        if (exists) return prev
        return [{
          id: jobID,
          figma_url: (p.figma_url as string) ?? '',
          platforms: platforms,
          status: 'queued',
          screens: [],
          avgScore: 0,
          totalIter: 0,
        }, ...prev]
      })
    }

    if (key === 'log.event' && p.step === 'figma_parsed') {
      const jobID = p.job_id as string
      const count = (p.data as Record<string, unknown>)?.screens as number ?? 0
      setJobs(prev => prev.map(j => j.id !== jobID ? j : {
        ...j,
        status: 'running',
        screens: Array.from({ length: count * platforms.length }, (_, i) => ({
          name:      `Screen ${Math.floor(i / platforms.length) + 1}`,
          platform:  platforms[i % platforms.length],
          status:    'pending',
          score:     0,
          iteration: 0,
        })),
      }))
    }

    if (key === 'log.event' && p.step === 'diff_result') {
      const jobID    = p.job_id as string
      const score    = (p.data as Record<string, unknown>)?.score as number ?? 0
      setJobs(prev => prev.map(j => {
        if (j.id !== jobID) return j
        // Update first running screen
        const screens = j.screens.map(s => {
          if (s.status === 'running') return { ...s, score, iteration: s.iteration + 1 }
          return s
        })
        return { ...j, screens }
      }))
    }

    if (key === 'screen.done') {
      const jobID    = p.job_id as string
      const name     = p.screen_name as string
      const platform = p.platform as string
      const score    = p.score as number
      const iter     = p.iterations as number
      setJobs(prev => prev.map(j => {
        if (j.id !== jobID) return j
        const screens = j.screens.map(s =>
          s.name === name && s.platform === platform
            ? { ...s, status: 'done' as const, score, iteration: iter }
            : s.status === 'pending' && s.platform === platform
              ? { ...s, status: 'running' as const }
              : s
        )
        return { ...j, screens }
      }))
    }

    if (key === 'job.done') {
      const jobID   = p.job_id as string
      const avg     = p.avg_score as number
      const total   = p.total_iter as number
      setJobs(prev => prev.map(j => j.id !== jobID ? j : {
        ...j,
        status: 'done',
        avgScore: avg,
        totalIter: total,
        screens: j.screens.map(s => s.status !== 'done' ? { ...s, status: 'done' as const } : s),
      }))
    }

    if (key === 'job.failed') {
      const jobID = p.job_id as string
      setJobs(prev => prev.map(j => j.id !== jobID ? j : { ...j, status: 'failed' }))
    }
  }, [platforms])

  // ── Submit ─────────────────────────────────────────────────────────────────

  const submit = async () => {
    if (!figmaURL.trim() || submitting) return
    setSubmitting(true)
    setLogs([])
    try {
      const { job_id } = await createJob({
        figma_url:  figmaURL,
        repo_url:   repoURL || undefined,
        platforms,
        styling,
        threshold,
      })
      setActiveJobID(job_id)
    } catch (err) {
      setLogs([{
        id:      lineID.current++,
        ts:      new Date().toLocaleTimeString('en', { hour12: false }),
        job_id:  '',
        level:   'error',
        step:    'submit',
        message: String(err),
      }])
    } finally {
      setSubmitting(false)
    }
  }

  const togglePlatform = (p: string) => {
    setPlatforms(prev =>
      prev.includes(p) ? prev.filter(x => x !== p) : [...prev, p]
    )
  }

  const activeJob = jobs.find(j => j.id === activeJobID)

  // ── Pipeline steps ─────────────────────────────────────────────────────────
  const STEPS = [
    { id: 'parse_figma',    icon: '🎨', label: 'Parse Figma' },
    { id: 'codegen',        icon: '🤖', label: 'Generate Code' },
    { id: 'sandbox',        icon: '📦', label: 'Build Sandbox' },
    { id: 'screenshot',     icon: '📸', label: 'Screenshot' },
    { id: 'diff',           icon: '🔍', label: 'Pixel Diff' },
    { id: 'screen_passed',  icon: '✅', label: 'Screen Done' },
    { id: 'job_done',       icon: '🎉', label: 'Job Done' },
  ]
  const activeStepIdx = STEPS.findIndex(s => activeStep.includes(s.id))

  return (
    <div className="root">
      {/* Sidebar */}
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-icon">⚡</div>
          <div>
            <div className="brand-name">FORGE</div>
            <div className="brand-sub">v0.2 · microservices</div>
          </div>
          <div className={`ws-pill ${connected ? 'on' : 'off'}`}>
            {connected ? 'live' : 'off'}
          </div>
        </div>

        <div className="jobs-list">
          <div className="section-title">Jobs</div>
          {jobs.length === 0 && <div className="empty">No jobs yet</div>}
          {jobs.map(job => (
            <button
              key={job.id}
              className={`job-row ${activeJobID === job.id ? 'active' : ''} ${job.status}`}
              onClick={() => setActiveJobID(job.id)}
            >
              <div className={`job-dot ${job.status}`} />
              <div className="job-info">
                <div className="job-id">{job.id.slice(0, 8)}…</div>
                <div className="job-platforms">{job.platforms.join(' + ')}</div>
              </div>
              {job.status === 'done' && (
                <div className="job-score">{job.avgScore.toFixed(0)}%</div>
              )}
            </button>
          ))}
        </div>

        <div className="service-map">
          <div className="section-title">Services</div>
          {Object.values(services).map((svc) => (
            <div key={svc.name} className={`svc-row ${svc.status}`}>
              <div className={`svc-dot ${svc.status}`} />
              <div className="svc-info">
                <div className="svc-name">{svc.name}</div>
                <div className="svc-desc" title={svc.lastLog}>{svc.lastLog || svc.desc}</div>
              </div>
              <button
                className="svc-filter-btn"
                onClick={() => setLogFilter(logFilter === svc.name ? 'all' : svc.name)}
                title={`Filter logs for ${svc.name}`}
              >
                {logFilter === svc.name ? '✓' : '→'}
              </button>
            </div>
          ))}
        </div>
      </aside>

      {/* Main */}
      <main className="main">
        {/* Input bar */}
        <section className="input-bar">
          <div className="inputs">
            <input
              className="url-field"
              type="url"
              placeholder="🎨  Figma URL  —  https://figma.com/file/…"
              value={figmaURL}
              onChange={e => setFigmaURL(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && submit()}
            />
            <input
              className="url-field secondary"
              type="url"
              placeholder="📦  Git repo  (optional — style reference)"
              value={repoURL}
              onChange={e => setRepoURL(e.target.value)}
            />
          </div>

          <div className="controls">
            <div className="platform-group">
              {[
                { id: 'react',   label: '⚛ React',  badge: 'web' },
                { id: 'nextjs',  label: '▲ Next.js', badge: 'web' },
                { id: 'kmp',     label: '🤖 KMP',    badge: 'mobile' },
                { id: 'flutter', label: '💙 Flutter', badge: 'mobile' },
              ].map(p => (
                <button
                  key={p.id}
                  className={`plat-btn ${platforms.includes(p.id) ? 'on' : ''}`}
                  onClick={() => togglePlatform(p.id)}
                >
                  {p.label}
                  <span className={`plat-badge ${p.badge}`}>{p.badge}</span>
                </button>
              ))}
            </div>

            <select className="sel" value={styling} onChange={e => setStyling(e.target.value)}>
              <option value="tailwind">Tailwind</option>
              <option value="cssmodules">CSS Modules</option>
            </select>

            <select className="sel" value={threshold} onChange={e => setThreshold(+e.target.value)}>
              <option value={95}>95% strict</option>
              <option value={90}>90% balanced</option>
              <option value={80}>80% relaxed</option>
            </select>

            <button
              className={`run-btn ${submitting ? 'spinning' : ''}`}
              onClick={submit}
              disabled={submitting || !figmaURL.trim()}
            >
              {submitting ? '⏳' : '⚡ Run'}
            </button>
          </div>
        </section>

        {/* Pipeline */}
        <section className="pipeline">
          {STEPS.map((step, i) => {
            const done   = activeJob?.status === 'done' || activeStepIdx > i
            const active = activeStepIdx === i
            return (
              <div key={step.id} className={`pipe-node ${done ? 'done' : active ? 'active' : ''}`}>
                <div className="pipe-circle">
                  {done ? '✓' : step.icon}
                </div>
                <div className="pipe-label">{step.label}</div>
                {i < STEPS.length - 1 && (
                  <div className={`pipe-line ${done ? 'done' : ''}`} />
                )}
              </div>
            )
          })}
        </section>

        {/* Body */}
        <div className="body-grid">
          {/* Terminal */}
          <section className="terminal">
            <div className="term-header">
              <span className="dot r" /><span className="dot y" /><span className="dot g" />
              <span className="term-title">
                {logFilter === 'all'
                  ? 'forge · live stream'
                  : `${logFilter} logs`}
              </span>
              <select
                className="log-filter-select"
                value={logFilter}
                onChange={e => setLogFilter(e.target.value as 'all' | string)}
              >
                <option value="all">All Services</option>
                {Object.values(services).map(svc => (
                  <option key={svc.name} value={svc.name}>{svc.name}</option>
                ))}
              </select>
              <button className="btn-clear" onClick={() => setLogs([])}>clear</button>
            </div>
            <div className="log-scroll" ref={logRef}>
              {logs.length === 0 && (
                <div className="log-empty">Waiting for events<span className="blink">_</span></div>
              )}
              {logs
                .filter(line => logFilter === 'all' || line.service === logFilter)
                .map(line => (
                <div key={line.id} className={`log-line ${line.level} ${line.service || ''}`}>
                  <span className="lt">{line.ts}</span>
                  {line.service && <span className="lsvc">[{line.service}]</span>}
                  <span className="ls">[{line.step}]</span>
                  <span className="lm">{line.message}</span>
                </div>
              ))}
              {logs.length > 0 && <span className="blink">▌</span>}
            </div>
          </section>

          {/* Right panel */}
          <section className="side">
            {/* Platform progress */}
            <div className="progress-section">
              <div className="section-title">Progress by Platform</div>
              {['react', 'nextjs', 'kmp', 'flutter']
                .filter(p => activeJob?.platforms.includes(p))
                .map(platform => {
                  const pScreens = activeJob?.screens.filter(s => s.platform === platform) ?? []
                  const done     = pScreens.filter(s => s.status === 'done').length
                  const total    = pScreens.length
                  const pct      = total > 0 ? (done / total) * 100 : 0
                  const avgScore = done > 0
                    ? pScreens.filter(s => s.status === 'done').reduce((a, s) => a + s.score, 0) / done
                    : 0
                  return (
                    <div key={platform} className="plat-progress">
                      <div className="pp-header">
                        <span className="pp-name">{platform}</span>
                        <span className="pp-stat">{done}/{total} screens · {avgScore.toFixed(0)}% avg</span>
                      </div>
                      <div className="pp-bar">
                        <div className="pp-fill" style={{ width: `${pct}%` }} />
                      </div>
                    </div>
                  )
                })}
            </div>

            {/* Screen list */}
            <div className="screens-section">
              <div className="section-title">Screen Queue</div>
              <div className="screens-list">
                {activeJob?.screens.length === 0 && (
                  <div className="empty">Waiting for Figma parse…</div>
                )}
                {activeJob?.screens.map((s, i) => (
                  <div key={i} className={`screen-item ${s.status}`}>
                    <div className="si-platform">{s.platform}</div>
                    <div className="si-name">{s.name}</div>
                    <div className="si-iter">
                      {s.iteration > 0 ? `iter ${s.iteration}` : ''}
                    </div>
                    <div className={`si-badge ${s.status}`}>
                      {s.status === 'done'
                        ? `✓ ${s.score.toFixed(0)}%`
                        : s.status === 'running'
                          ? `⚙ ${s.score > 0 ? s.score.toFixed(0)+'%' : '…'}`
                          : '—'}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </section>
        </div>
      </main>
    </div>
  )
}
