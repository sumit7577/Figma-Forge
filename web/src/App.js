import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useEffect, useCallback, useRef } from 'react';
import './app.css';
// ── API ───────────────────────────────────────────────────────────────────────
const API = import.meta.env.VITE_API_URL ?? 'http://localhost:8080';
const WS = import.meta.env.VITE_WS_URL ?? 'ws://localhost:8080/ws';
// Map steps to services
const STEP_TO_SERVICE = {
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
};
async function createJob(body) {
    const r = await fetch(`${API}/api/jobs`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!r.ok)
        throw new Error(await r.text());
    return r.json();
}
// ── App ───────────────────────────────────────────────────────────────────────
export default function App() {
    // Form
    const [figmaURL, setFigmaURL] = useState('');
    const [repoURL, setRepoURL] = useState('');
    const [platforms, setPlatforms] = useState(['react', 'kmp']);
    const [styling, setStyling] = useState('tailwind');
    const [threshold, setThreshold] = useState(95);
    // State
    const [connected, setConnected] = useState(false);
    const [logs, setLogs] = useState([]);
    const [jobs, setJobs] = useState([]);
    const [activeJobID, setActiveJobID] = useState(null);
    const [submitting, setSubmitting] = useState(false);
    const [activeStep, setActiveStep] = useState('');
    const [logFilter, setLogFilter] = useState('all');
    const [services, setServices] = useState({
        'gateway': { name: 'gateway', desc: 'API + WS', status: 'idle' },
        'orchestrator': { name: 'orchestrator', desc: 'State machine', status: 'idle' },
        'figma-parser': { name: 'figma-parser', desc: 'Figma API', status: 'idle' },
        'codegen': { name: 'codegen', desc: 'Claude API', status: 'idle' },
        'sandbox': { name: 'sandbox', desc: 'Docker runner', status: 'idle' },
        'differ': { name: 'differ', desc: 'Pixel diff', status: 'idle' },
        'notifier': { name: 'notifier', desc: 'Telegram', status: 'idle' },
    });
    const logRef = useRef(null);
    const lineID = useRef(0);
    const wsRef = useRef(null);
    // ── WebSocket ──────────────────────────────────────────────────────────────
    const connectWS = useCallback(() => {
        if (wsRef.current?.readyState === WebSocket.OPEN)
            return;
        const ws = new WebSocket(WS);
        wsRef.current = ws;
        ws.onopen = () => { setConnected(true); console.log('[forge] WS connected'); };
        ws.onmessage = (e) => {
            try {
                const ev = JSON.parse(e.data);
                handleEvent(ev);
            }
            catch { }
        };
        ws.onclose = () => {
            setConnected(false);
            setTimeout(connectWS, 3000);
        };
        ws.onerror = () => ws.close();
    }, []);
    useEffect(() => {
        connectWS();
        return () => wsRef.current?.close();
    }, [connectWS]);
    // Auto-scroll log
    useEffect(() => {
        logRef.current?.scrollTo({ top: logRef.current.scrollHeight, behavior: 'smooth' });
    }, [logs]);
    // ── Event handler ──────────────────────────────────────────────────────────
    const handleEvent = useCallback((ev) => {
        const key = ev.routing_key;
        const p = ev.payload;
        // Log lines
        if (key.startsWith('log.')) {
            const step = p.step ?? key;
            const line = {
                id: lineID.current++,
                ts: new Date(ev.ts).toLocaleTimeString('en', { hour12: false }),
                job_id: p.job_id ?? '',
                level: p.level ?? 'info',
                step: step,
                message: p.message ?? '',
                data: p.data,
                service: STEP_TO_SERVICE[step],
            };
            setLogs(prev => [...prev.slice(-500), line]);
            setActiveStep(step);
            // Update service status
            const service = STEP_TO_SERVICE[step];
            if (service) {
                setServices(prev => ({
                    ...prev,
                    [service]: {
                        ...prev[service],
                        status: line.level === 'error' ? 'error' : 'working',
                        lastLog: line.message,
                        lastUpdate: Date.now(),
                    }
                }));
            }
        }
        if (key === 'job.submitted' || (key === 'log.event' && p.step === 'job_submitted')) {
            const jobID = p.job_id;
            setActiveJobID(jobID);
            setJobs(prev => {
                const exists = prev.find(j => j.id === jobID);
                if (exists)
                    return prev;
                return [{
                        id: jobID,
                        figma_url: p.figma_url ?? '',
                        platforms: platforms,
                        status: 'queued',
                        screens: [],
                        avgScore: 0,
                        totalIter: 0,
                    }, ...prev];
            });
        }
        if (key === 'log.event' && p.step === 'figma_parsed') {
            const jobID = p.job_id;
            const count = p.data?.screens ?? 0;
            setJobs(prev => prev.map(j => j.id !== jobID ? j : {
                ...j,
                status: 'running',
                screens: Array.from({ length: count * platforms.length }, (_, i) => ({
                    name: `Screen ${Math.floor(i / platforms.length) + 1}`,
                    platform: platforms[i % platforms.length],
                    status: 'pending',
                    score: 0,
                    iteration: 0,
                })),
            }));
        }
        if (key === 'log.event' && p.step === 'diff_result') {
            const jobID = p.job_id;
            const score = p.data?.score ?? 0;
            setJobs(prev => prev.map(j => {
                if (j.id !== jobID)
                    return j;
                // Update first running screen
                const screens = j.screens.map(s => {
                    if (s.status === 'running')
                        return { ...s, score, iteration: s.iteration + 1 };
                    return s;
                });
                return { ...j, screens };
            }));
        }
        if (key === 'screen.done') {
            const jobID = p.job_id;
            const name = p.screen_name;
            const platform = p.platform;
            const score = p.score;
            const iter = p.iterations;
            setJobs(prev => prev.map(j => {
                if (j.id !== jobID)
                    return j;
                const screens = j.screens.map(s => s.name === name && s.platform === platform
                    ? { ...s, status: 'done', score, iteration: iter }
                    : s.status === 'pending' && s.platform === platform
                        ? { ...s, status: 'running' }
                        : s);
                return { ...j, screens };
            }));
        }
        if (key === 'job.done') {
            const jobID = p.job_id;
            const avg = p.avg_score;
            const total = p.total_iter;
            setJobs(prev => prev.map(j => j.id !== jobID ? j : {
                ...j,
                status: 'done',
                avgScore: avg,
                totalIter: total,
                screens: j.screens.map(s => s.status !== 'done' ? { ...s, status: 'done' } : s),
            }));
        }
        if (key === 'job.failed') {
            const jobID = p.job_id;
            setJobs(prev => prev.map(j => j.id !== jobID ? j : { ...j, status: 'failed' }));
        }
    }, [platforms]);
    // ── Submit ─────────────────────────────────────────────────────────────────
    const submit = async () => {
        if (!figmaURL.trim() || submitting)
            return;
        setSubmitting(true);
        setLogs([]);
        try {
            const { job_id } = await createJob({
                figma_url: figmaURL,
                repo_url: repoURL || undefined,
                platforms,
                styling,
                threshold,
            });
            setActiveJobID(job_id);
        }
        catch (err) {
            setLogs([{
                    id: lineID.current++,
                    ts: new Date().toLocaleTimeString('en', { hour12: false }),
                    job_id: '',
                    level: 'error',
                    step: 'submit',
                    message: String(err),
                }]);
        }
        finally {
            setSubmitting(false);
        }
    };
    const togglePlatform = (p) => {
        setPlatforms(prev => prev.includes(p) ? prev.filter(x => x !== p) : [...prev, p]);
    };
    const activeJob = jobs.find(j => j.id === activeJobID);
    // ── Pipeline steps ─────────────────────────────────────────────────────────
    const STEPS = [
        { id: 'parse_figma', icon: '🎨', label: 'Parse Figma' },
        { id: 'codegen', icon: '🤖', label: 'Generate Code' },
        { id: 'sandbox', icon: '📦', label: 'Build Sandbox' },
        { id: 'screenshot', icon: '📸', label: 'Screenshot' },
        { id: 'diff', icon: '🔍', label: 'Pixel Diff' },
        { id: 'screen_passed', icon: '✅', label: 'Screen Done' },
        { id: 'job_done', icon: '🎉', label: 'Job Done' },
    ];
    const activeStepIdx = STEPS.findIndex(s => activeStep.includes(s.id));
    return (_jsxs("div", { className: "root", children: [_jsxs("aside", { className: "sidebar", children: [_jsxs("div", { className: "brand", children: [_jsx("div", { className: "brand-icon", children: "\u26A1" }), _jsxs("div", { children: [_jsx("div", { className: "brand-name", children: "FORGE" }), _jsx("div", { className: "brand-sub", children: "v0.2 \u00B7 microservices" })] }), _jsx("div", { className: `ws-pill ${connected ? 'on' : 'off'}`, children: connected ? 'live' : 'off' })] }), _jsxs("div", { className: "jobs-list", children: [_jsx("div", { className: "section-title", children: "Jobs" }), jobs.length === 0 && _jsx("div", { className: "empty", children: "No jobs yet" }), jobs.map(job => (_jsxs("button", { className: `job-row ${activeJobID === job.id ? 'active' : ''} ${job.status}`, onClick: () => setActiveJobID(job.id), children: [_jsx("div", { className: `job-dot ${job.status}` }), _jsxs("div", { className: "job-info", children: [_jsxs("div", { className: "job-id", children: [job.id.slice(0, 8), "\u2026"] }), _jsx("div", { className: "job-platforms", children: job.platforms.join(' + ') })] }), job.status === 'done' && (_jsxs("div", { className: "job-score", children: [job.avgScore.toFixed(0), "%"] }))] }, job.id)))] }), _jsxs("div", { className: "service-map", children: [_jsx("div", { className: "section-title", children: "Services" }), Object.values(services).map((svc) => (_jsxs("div", { className: `svc-row ${svc.status}`, children: [_jsx("div", { className: `svc-dot ${svc.status}` }), _jsxs("div", { className: "svc-info", children: [_jsx("div", { className: "svc-name", children: svc.name }), _jsx("div", { className: "svc-desc", title: svc.lastLog, children: svc.lastLog || svc.desc })] }), _jsx("button", { className: "svc-filter-btn", onClick: () => setLogFilter(logFilter === svc.name ? 'all' : svc.name), title: `Filter logs for ${svc.name}`, children: logFilter === svc.name ? '✓' : '→' })] }, svc.name)))] })] }), _jsxs("main", { className: "main", children: [_jsxs("section", { className: "input-bar", children: [_jsxs("div", { className: "inputs", children: [_jsx("input", { className: "url-field", type: "url", placeholder: "\uD83C\uDFA8  Figma URL  \u2014  https://figma.com/file/\u2026", value: figmaURL, onChange: e => setFigmaURL(e.target.value), onKeyDown: e => e.key === 'Enter' && submit() }), _jsx("input", { className: "url-field secondary", type: "url", placeholder: "\uD83D\uDCE6  Git repo  (optional \u2014 style reference)", value: repoURL, onChange: e => setRepoURL(e.target.value) })] }), _jsxs("div", { className: "controls", children: [_jsx("div", { className: "platform-group", children: [
                                            { id: 'react', label: '⚛ React', badge: 'web' },
                                            { id: 'nextjs', label: '▲ Next.js', badge: 'web' },
                                            { id: 'kmp', label: '🤖 KMP', badge: 'mobile' },
                                            { id: 'flutter', label: '💙 Flutter', badge: 'mobile' },
                                        ].map(p => (_jsxs("button", { className: `plat-btn ${platforms.includes(p.id) ? 'on' : ''}`, onClick: () => togglePlatform(p.id), children: [p.label, _jsx("span", { className: `plat-badge ${p.badge}`, children: p.badge })] }, p.id))) }), _jsxs("select", { className: "sel", value: styling, onChange: e => setStyling(e.target.value), children: [_jsx("option", { value: "tailwind", children: "Tailwind" }), _jsx("option", { value: "cssmodules", children: "CSS Modules" })] }), _jsxs("select", { className: "sel", value: threshold, onChange: e => setThreshold(+e.target.value), children: [_jsx("option", { value: 95, children: "95% strict" }), _jsx("option", { value: 90, children: "90% balanced" }), _jsx("option", { value: 80, children: "80% relaxed" })] }), _jsx("button", { className: `run-btn ${submitting ? 'spinning' : ''}`, onClick: submit, disabled: submitting || !figmaURL.trim(), children: submitting ? '⏳' : '⚡ Run' })] })] }), _jsx("section", { className: "pipeline", children: STEPS.map((step, i) => {
                            const done = activeJob?.status === 'done' || activeStepIdx > i;
                            const active = activeStepIdx === i;
                            return (_jsxs("div", { className: `pipe-node ${done ? 'done' : active ? 'active' : ''}`, children: [_jsx("div", { className: "pipe-circle", children: done ? '✓' : step.icon }), _jsx("div", { className: "pipe-label", children: step.label }), i < STEPS.length - 1 && (_jsx("div", { className: `pipe-line ${done ? 'done' : ''}` }))] }, step.id));
                        }) }), _jsxs("div", { className: "body-grid", children: [_jsxs("section", { className: "terminal", children: [_jsxs("div", { className: "term-header", children: [_jsx("span", { className: "dot r" }), _jsx("span", { className: "dot y" }), _jsx("span", { className: "dot g" }), _jsx("span", { className: "term-title", children: logFilter === 'all'
                                                    ? 'forge · live stream'
                                                    : `${logFilter} logs` }), _jsxs("select", { className: "log-filter-select", value: logFilter, onChange: e => setLogFilter(e.target.value), children: [_jsx("option", { value: "all", children: "All Services" }), Object.values(services).map(svc => (_jsx("option", { value: svc.name, children: svc.name }, svc.name)))] }), _jsx("button", { className: "btn-clear", onClick: () => setLogs([]), children: "clear" })] }), _jsxs("div", { className: "log-scroll", ref: logRef, children: [logs.length === 0 && (_jsxs("div", { className: "log-empty", children: ["Waiting for events", _jsx("span", { className: "blink", children: "_" })] })), logs
                                                .filter(line => logFilter === 'all' || line.service === logFilter)
                                                .map(line => (_jsxs("div", { className: `log-line ${line.level} ${line.service || ''}`, children: [_jsx("span", { className: "lt", children: line.ts }), line.service && _jsxs("span", { className: "lsvc", children: ["[", line.service, "]"] }), _jsxs("span", { className: "ls", children: ["[", line.step, "]"] }), _jsx("span", { className: "lm", children: line.message })] }, line.id))), logs.length > 0 && _jsx("span", { className: "blink", children: "\u258C" })] })] }), _jsxs("section", { className: "side", children: [_jsxs("div", { className: "progress-section", children: [_jsx("div", { className: "section-title", children: "Progress by Platform" }), ['react', 'nextjs', 'kmp', 'flutter']
                                                .filter(p => activeJob?.platforms.includes(p))
                                                .map(platform => {
                                                const pScreens = activeJob?.screens.filter(s => s.platform === platform) ?? [];
                                                const done = pScreens.filter(s => s.status === 'done').length;
                                                const total = pScreens.length;
                                                const pct = total > 0 ? (done / total) * 100 : 0;
                                                const avgScore = done > 0
                                                    ? pScreens.filter(s => s.status === 'done').reduce((a, s) => a + s.score, 0) / done
                                                    : 0;
                                                return (_jsxs("div", { className: "plat-progress", children: [_jsxs("div", { className: "pp-header", children: [_jsx("span", { className: "pp-name", children: platform }), _jsxs("span", { className: "pp-stat", children: [done, "/", total, " screens \u00B7 ", avgScore.toFixed(0), "% avg"] })] }), _jsx("div", { className: "pp-bar", children: _jsx("div", { className: "pp-fill", style: { width: `${pct}%` } }) })] }, platform));
                                            })] }), _jsxs("div", { className: "screens-section", children: [_jsx("div", { className: "section-title", children: "Screen Queue" }), _jsxs("div", { className: "screens-list", children: [activeJob?.screens.length === 0 && (_jsx("div", { className: "empty", children: "Waiting for Figma parse\u2026" })), activeJob?.screens.map((s, i) => (_jsxs("div", { className: `screen-item ${s.status}`, children: [_jsx("div", { className: "si-platform", children: s.platform }), _jsx("div", { className: "si-name", children: s.name }), _jsx("div", { className: "si-iter", children: s.iteration > 0 ? `iter ${s.iteration}` : '' }), _jsx("div", { className: `si-badge ${s.status}`, children: s.status === 'done'
                                                                    ? `✓ ${s.score.toFixed(0)}%`
                                                                    : s.status === 'running'
                                                                        ? `⚙ ${s.score > 0 ? s.score.toFixed(0) + '%' : '…'}`
                                                                        : '—' })] }, i)))] })] })] })] })] })] }));
}
