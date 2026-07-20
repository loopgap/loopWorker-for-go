package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"loopworker/internal/core/observer"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/utils"
)

type Dashboard struct {
	scheduler *scheduler.Scheduler
	observer  *observer.Observer
	eventBus  *event.EventBus
	clients   map[chan []byte]bool
	mu        sync.RWMutex

	// throttleState tracks the last broadcast time to prevent self-triggering loops
	throttleMu sync.Mutex
	lastBroadcast time.Time
}

type DashboardData struct {
	Tasks   []*scheduler.Task      `json:"tasks"`
	Metrics []observer.Metric      `json:"metrics"`
	Logs    []observer.LogEntry    `json:"logs"`
	Health  map[string]interface{} `json:"health"`
	Stats   map[string]interface{} `json:"stats"`
}

func NewDashboard(s *scheduler.Scheduler, o *observer.Observer, bus *event.EventBus) *Dashboard {
	return &Dashboard{
		scheduler: s,
		observer:  o,
		eventBus:  bus,
		clients:   make(map[chan []byte]bool),
	}
}

func (d *Dashboard) Start(addr string) error {
	d.registerHandlers()
	fmt.Printf("Dashboard starting on %s\n", addr)
	return http.ListenAndServe(addr, nil)
}

// RegisterHandlers registers all dashboard HTTP handlers on http.DefaultServeMux.
// Use this when the caller manages the http.Server lifecycle.
func (d *Dashboard) RegisterHandlers() {
	d.registerHandlers()
}

func (d *Dashboard) registerHandlers() {
	http.HandleFunc("/", d.handleIndex)
	http.HandleFunc("/api/tasks", d.handleTasks)
	http.HandleFunc("/api/metrics", d.handleMetrics)
	http.HandleFunc("/api/logs", d.handleLogs)
	http.HandleFunc("/api/health", d.handleHealth)
	http.HandleFunc("/api/stats", d.handleStats)
	http.HandleFunc("/events", d.handleSSE)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

func (d *Dashboard) handleTasks(w http.ResponseWriter, r *http.Request) {
	tasks := d.scheduler.ListTasks(scheduler.TaskFilter{})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func (d *Dashboard) handleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := d.observer.GetMetrics()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func (d *Dashboard) handleLogs(w http.ResponseWriter, r *http.Request) {
	logs := d.observer.GetLogs()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

func (d *Dashboard) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := d.observer.GetHealth()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func (d *Dashboard) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := d.scheduler.GetStats()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (d *Dashboard) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientChan := make(chan []byte, 10)
	d.mu.Lock()
	d.clients[clientChan] = true
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.clients, clientChan)
		d.mu.Unlock()
	}()

	ctx := r.Context()

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send heartbeat comment to keep connection alive
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
			d.sendUpdate(clientChan)
		case data := <-clientChan:
			fmt.Fprintf(w, "event: update\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (d *Dashboard) sendUpdate(clientChan chan []byte) {
	data := DashboardData{
		Tasks:   d.scheduler.ListTasks(scheduler.TaskFilter{}),
		Metrics: d.observer.GetMetrics(),
		Logs:    d.observer.GetLogs(),
		Health:  d.observer.GetHealth(),
		Stats:   d.scheduler.GetStats(),
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}

	select {
	case clientChan <- jsonData:
	default:
	}
}

func (d *Dashboard) BroadcastUpdate() {
	// Throttle broadcasts to prevent self-triggering loops.
	// SSE clients poll every 1s via ticker, so 100ms minimum interval
	// is invisible to users but prevents cascade amplification.
	d.throttleMu.Lock()
	if time.Since(d.lastBroadcast) < 100*time.Millisecond {
		d.throttleMu.Unlock()
		return
	}
	d.lastBroadcast = time.Now()
	d.throttleMu.Unlock()

	data := DashboardData{
		Tasks:   d.scheduler.ListTasks(scheduler.TaskFilter{}),
		Metrics: d.observer.GetMetrics(),
		Logs:    d.observer.GetLogs(),
		Health:  d.observer.GetHealth(),
		Stats:   d.scheduler.GetStats(),
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	for client := range d.clients {
		select {
		case client <- jsonData:
		default:
		}
	}
}

func (d *Dashboard) StartEventListening() {
	if d.eventBus == nil {
		return
	}

	// Subscribe to all relevant events for real-time updates
	eventTypes := []event.EventType{
		event.EventTaskCreated,
		event.EventTaskStarted,
		event.EventTaskCompleted,
		event.EventTaskFailed,
		event.EventTaskRetried,
		event.EventTaskCancelled,
		event.EventWorkerSpawned,
		event.EventWorkerExited,
		event.EventPluginExecuted,
		event.EventSkillInvoked,
		event.EventResearchFinding,
		event.EventWorkflowStepCompleted,
		event.EventWorkflowStarted,
		event.EventWorkflowCompleted,
		event.EventWorkflowFailed,
	}

	for _, eventType := range eventTypes {
		sub := d.eventBus.Subscribe(eventType, 100)
		utils.GoSafe(context.Background(), func(ctx context.Context) {
			for range sub.Chan() {
				d.BroadcastUpdate()
			}
		})
	}
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>LoopWorker</title>
    <style>
        :root {
            --glass-bg: rgba(255, 255, 255, 0.72);
            --glass-bg-hover: rgba(255, 255, 255, 0.85);
            --glass-border: rgba(255, 255, 255, 0.5);
            --glass-shadow: 0 8px 32px rgba(0, 0, 0, 0.08);
            --glass-shadow-hover: 0 12px 40px rgba(0, 0, 0, 0.12);
            --glass-blur: blur(20px) saturate(180%);
            --glass-blur-heavy: blur(40px) saturate(200%);
            
            --spring-bounce: cubic-bezier(0.34, 1.56, 0.64, 1);
            --spring-smooth: cubic-bezier(0.25, 0.46, 0.45, 0.94);
            --ease-out-expo: cubic-bezier(0.16, 1, 0.3, 1);
            --ease-in-out-quart: cubic-bezier(0.76, 0, 0.24, 1);
            
            --color-blue: #007AFF;
            --color-green: #34C759;
            --color-orange: #FF9500;
            --color-red: #FF3B30;
            --color-purple: #AF52DE;
            --color-pink: #FF2D55;
            --color-teal: #5AC8FA;
            --color-indigo: #5856D6;
            
            --bg-primary: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            --bg-secondary: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
            --bg-gradient: linear-gradient(135deg, #667eea 0%, #764ba2 50%, #f093fb 100%);
            
            --text-primary: rgba(0, 0, 0, 0.87);
            --text-secondary: rgba(0, 0, 0, 0.6);
            --text-tertiary: rgba(0, 0, 0, 0.4);
            
            --spacing-xs: 4px;
            --spacing-sm: 8px;
            --spacing-md: 16px;
            --spacing-lg: 24px;
            --spacing-xl: 32px;
            --spacing-2xl: 48px;
            
            --radius-sm: 8px;
            --radius-md: 12px;
            --radius-lg: 16px;
            --radius-xl: 24px;
            --radius-full: 9999px;
        }

        * { box-sizing: border-box; margin: 0; padding: 0; }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Display', 'SF Pro Text', sans-serif;
            background: var(--bg-gradient);
            min-height: 100vh;
            overflow-x: hidden;
        }

        .app {
            display: flex;
            min-height: 100vh;
            position: relative;
        }

        .app::before {
            content: '';
            position: fixed;
            top: 0;
            left: 0;
            right: 0;
            bottom: 0;
            background: 
                radial-gradient(circle at 20% 20%, rgba(255,255,255,0.3) 0%, transparent 50%),
                radial-gradient(circle at 80% 80%, rgba(255,255,255,0.2) 0%, transparent 50%),
                radial-gradient(circle at 50% 50%, rgba(255,255,255,0.1) 0%, transparent 70%);
            pointer-events: none;
            z-index: 0;
        }

        .sidebar {
            width: 280px;
            background: var(--glass-bg);
            backdrop-filter: var(--glass-blur);
            -webkit-backdrop-filter: var(--glass-blur);
            border-right: 1px solid var(--glass-border);
            padding: var(--spacing-lg);
            position: fixed;
            top: 0;
            left: 0;
            bottom: 0;
            z-index: 100;
            transition: transform 0.4s var(--spring-smooth);
        }

        .logo {
            display: flex;
            align-items: center;
            gap: var(--spacing-md);
            padding-bottom: var(--spacing-xl);
            margin-bottom: var(--spacing-xl);
            border-bottom: 1px solid rgba(0,0,0,0.06);
        }

        .logo-icon {
            width: 44px;
            height: 44px;
            background: linear-gradient(135deg, var(--color-blue), var(--color-purple));
            border-radius: var(--radius-md);
            display: flex;
            align-items: center;
            justify-content: center;
            color: white;
            font-weight: 700;
            font-size: 18px;
            box-shadow: 0 4px 12px rgba(0, 122, 255, 0.3);
            transition: transform 0.3s var(--spring-bounce);
        }

        .logo-icon:hover {
            transform: scale(1.1) rotate(-5deg);
        }

        .logo-text {
            font-size: 20px;
            font-weight: 600;
            color: var(--text-primary);
            letter-spacing: -0.3px;
        }

        .nav-section {
            margin-bottom: var(--spacing-lg);
        }

        .nav-label {
            font-size: 11px;
            font-weight: 600;
            color: var(--text-tertiary);
            text-transform: uppercase;
            letter-spacing: 0.8px;
            padding: 0 var(--spacing-sm);
            margin-bottom: var(--spacing-sm);
        }

        .nav-item {
            display: flex;
            align-items: center;
            gap: var(--spacing-sm);
            padding: 10px var(--spacing-md);
            border-radius: var(--radius-md);
            color: var(--text-secondary);
            cursor: pointer;
            transition: all 0.2s var(--spring-smooth);
            font-size: 15px;
            font-weight: 500;
            border: none;
            background: transparent;
            width: 100%;
            text-align: left;
            position: relative;
            overflow: hidden;
        }

        .nav-item::before {
            content: '';
            position: absolute;
            inset: 0;
            background: linear-gradient(135deg, rgba(0,122,255,0.1), rgba(88,86,214,0.1));
            opacity: 0;
            transition: opacity 0.2s ease;
            border-radius: var(--radius-md);
        }

        .nav-item:hover::before {
            opacity: 1;
        }

        .nav-item:hover {
            transform: translateX(4px);
        }

        .nav-item.active {
            background: linear-gradient(135deg, var(--color-blue), var(--color-indigo));
            color: white;
            box-shadow: 0 4px 16px rgba(0, 122, 255, 0.4);
            transform: translateX(4px);
        }

        .nav-item.active::before {
            opacity: 0;
        }

        .nav-item svg {
            width: 20px;
            height: 20px;
            flex-shrink: 0;
            transition: transform 0.2s var(--spring-bounce);
        }

        .nav-item:hover svg {
            transform: scale(1.1);
        }

        .main {
            flex: 1;
            margin-left: 280px;
            padding: var(--spacing-xl);
            position: relative;
            z-index: 1;
        }

        .header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: var(--spacing-xl);
            animation: slideDown 0.6s var(--ease-out-expo);
        }

        @keyframes slideDown {
            from { opacity: 0; transform: translateY(-20px); }
            to { opacity: 1; transform: translateY(0); }
        }

        .header h1 {
            font-size: 34px;
            font-weight: 700;
            color: white;
            letter-spacing: -0.5px;
            text-shadow: 0 2px 8px rgba(0,0,0,0.1);
        }

        .stats-grid {
            display: grid;
            grid-template-columns: repeat(4, 1fr);
            gap: var(--spacing-md);
            margin-bottom: var(--spacing-xl);
        }

        .stat-card {
            background: var(--glass-bg);
            backdrop-filter: var(--glass-blur);
            -webkit-backdrop-filter: var(--glass-blur);
            border: 1px solid var(--glass-border);
            border-radius: var(--radius-lg);
            padding: var(--spacing-lg);
            box-shadow: var(--glass-shadow);
            transition: all 0.4s var(--spring-bounce);
            animation: cardAppear 0.6s var(--ease-out-expo) forwards;
            opacity: 0;
            transform: translateY(20px);
        }

        .stat-card:nth-child(1) { animation-delay: 0.1s; }
        .stat-card:nth-child(2) { animation-delay: 0.15s; }
        .stat-card:nth-child(3) { animation-delay: 0.2s; }
        .stat-card:nth-child(4) { animation-delay: 0.25s; }

        @keyframes cardAppear {
            to { opacity: 1; transform: translateY(0); }
        }

        .stat-card:hover {
            transform: translateY(-8px) scale(1.02);
            box-shadow: var(--glass-shadow-hover), 0 0 0 1px rgba(255,255,255,0.5) inset;
            background: var(--glass-bg-hover);
        }

        .stat-label {
            font-size: 13px;
            color: var(--text-secondary);
            font-weight: 500;
            margin-bottom: var(--spacing-xs);
        }

        .stat-value {
            font-size: 36px;
            font-weight: 700;
            color: var(--text-primary);
            line-height: 1.1;
            transition: transform 0.3s var(--spring-bounce);
        }

        .stat-card:hover .stat-value {
            transform: scale(1.05);
        }

        .stat-value.blue { color: var(--color-blue); }
        .stat-value.green { color: var(--color-green); }
        .stat-value.orange { color: var(--color-orange); }
        .stat-value.red { color: var(--color-red); }

        .card {
            background: var(--glass-bg);
            backdrop-filter: var(--glass-blur);
            -webkit-backdrop-filter: var(--glass-blur);
            border: 1px solid var(--glass-border);
            border-radius: var(--radius-lg);
            box-shadow: var(--glass-shadow);
            overflow: hidden;
            animation: cardAppear 0.6s var(--ease-out-expo) 0.3s forwards;
            opacity: 0;
            transform: translateY(20px);
        }

        .card-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: var(--spacing-md) var(--spacing-lg);
            border-bottom: 1px solid rgba(0,0,0,0.06);
            background: rgba(255,255,255,0.3);
        }

        .card-title {
            font-size: 17px;
            font-weight: 600;
            color: var(--text-primary);
        }

        .card-body {
            padding: 0;
        }

        .table {
            width: 100%;
            border-collapse: collapse;
        }

        .table th {
            padding: var(--spacing-sm) var(--spacing-lg);
            text-align: left;
            font-size: 11px;
            font-weight: 600;
            color: var(--text-tertiary);
            text-transform: uppercase;
            letter-spacing: 0.5px;
            background: rgba(0,0,0,0.02);
        }

        .table td {
            padding: var(--spacing-md) var(--spacing-lg);
            font-size: 15px;
            border-bottom: 1px solid rgba(0,0,0,0.04);
            transition: background 0.2s ease;
        }

        .table tr:last-child td {
            border-bottom: none;
        }

        .table tr {
            transition: all 0.2s var(--spring-smooth);
        }

        .table tbody tr:hover {
            background: rgba(0, 122, 255, 0.04);
            transform: scale(1.01);
        }

        .badge {
            display: inline-flex;
            align-items: center;
            padding: 4px 10px;
            border-radius: var(--radius-full);
            font-size: 12px;
            font-weight: 600;
            transition: all 0.2s var(--spring-bounce);
        }

        .badge:hover {
            transform: scale(1.1);
        }

        .badge-pending { background: rgba(255, 149, 0, 0.15); color: var(--color-orange); }
        .badge-queued { background: rgba(175, 82, 222, 0.15); color: var(--color-purple); }
        .badge-running { background: rgba(0, 122, 255, 0.15); color: var(--color-blue); }
        .badge-completed { background: rgba(52, 199, 89, 0.15); color: var(--color-green); }
        .badge-failed { background: rgba(255, 59, 48, 0.15); color: var(--color-red); }

        .log-entry {
            display: flex;
            gap: var(--spacing-sm);
            padding: var(--spacing-sm) var(--spacing-lg);
            border-bottom: 1px solid rgba(0,0,0,0.04);
            font-size: 13px;
            font-family: 'SF Mono', SFMono-Regular, Menlo, monospace;
            transition: all 0.2s ease;
        }

        .log-entry:hover {
            background: rgba(0,0,0,0.02);
            transform: translateX(4px);
        }

        .log-entry:last-child {
            border-bottom: none;
        }

        .log-time {
            color: var(--text-tertiary);
        }

        .log-level {
            padding: 2px 8px;
            border-radius: var(--radius-full);
            font-size: 11px;
            font-weight: 600;
        }

        .log-level.info { background: rgba(0, 122, 255, 0.15); color: var(--color-blue); }
        .log-level.warn { background: rgba(255, 149, 0, 0.15); color: var(--color-orange); }
        .log-level.error { background: rgba(255, 59, 48, 0.15); color: var(--color-red); }

        .status-indicator {
            display: flex;
            align-items: center;
            gap: var(--spacing-sm);
            padding: var(--spacing-sm) var(--spacing-md);
            background: var(--glass-bg);
            backdrop-filter: var(--glass-blur);
            -webkit-backdrop-filter: var(--glass-blur);
            border: 1px solid var(--glass-border);
            border-radius: var(--radius-full);
            box-shadow: var(--glass-shadow);
        }

        .status-dot {
            width: 10px;
            height: 10px;
            border-radius: 50%;
            transition: all 0.3s var(--spring-bounce);
        }

        .status-dot.connected {
            background: var(--color-green);
            box-shadow: 0 0 12px rgba(52, 199, 89, 0.6);
            animation: pulse 2s infinite;
        }

        .status-dot.disconnected {
            background: var(--color-red);
        }

        @keyframes pulse {
            0%, 100% { transform: scale(1); opacity: 1; }
            50% { transform: scale(1.2); opacity: 0.8; }
        }

        @media (max-width: 1200px) {
            .stats-grid {
                grid-template-columns: repeat(2, 1fr);
            }
        }

        @media (max-width: 768px) {
            .sidebar {
                transform: translateX(-100%);
            }
            .main {
                margin-left: 0;
            }
        }
    </style>
</head>
<body>
    <div class="app">
        <nav class="sidebar">
            <div class="logo">
                <div class="logo-icon">LW</div>
                <span class="logo-text">LoopWorker</span>
            </div>
            
            <div class="nav-section">
                <div class="nav-label">Overview</div>
                <button class="nav-item active" onclick="showTab('dashboard')">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></svg>
                    Dashboard
                </button>
            </div>

            <div class="nav-section">
                <div class="nav-label">Management</div>
                <button class="nav-item" onclick="showTab('tasks')">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 11l3 3L22 4"/><path d="M21 12v7a2 2 0 01-2 2H5a2 2 0 01-2-2V5a2 2 0 012-2h11"/></svg>
                    Tasks
                </button>
                <button class="nav-item" onclick="showTab('workers')">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4-4v2"/><circle cx="9" cy="7" r="4"/></svg>
                    Workers
                </button>
            </div>

            <div class="nav-section">
                <div class="nav-label">Monitoring</div>
                <button class="nav-item" onclick="showTab('metrics')">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 20V10"/><path d="M12 20V4"/><path d="M6 20v-6"/></svg>
                    Metrics
                </button>
                <button class="nav-item" onclick="showTab('logs')">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><path d="M14 2v6h6"/></svg>
                    Logs
                </button>
            </div>
        </nav>

        <main class="main">
            <header class="header">
                <h1>Dashboard</h1>
                <div class="status-indicator">
                    <div id="status-dot" class="status-dot disconnected"></div>
                    <span id="status-text" style="font-size: 13px; font-weight: 500;">Connecting...</span>
                </div>
            </header>

            <div class="stats-grid">
                <div class="stat-card">
                    <div class="stat-label">Total Tasks</div>
                    <div class="stat-value" id="stat-total">0</div>
                </div>
                <div class="stat-card">
                    <div class="stat-label">Running</div>
                    <div class="stat-value blue" id="stat-running">0</div>
                </div>
                <div class="stat-card">
                    <div class="stat-label">Completed</div>
                    <div class="stat-value green" id="stat-completed">0</div>
                </div>
                <div class="stat-card">
                    <div class="stat-label">Failed</div>
                    <div class="stat-value red" id="stat-failed">0</div>
                </div>
            </div>

            <div class="card" style="margin-bottom: 16px">
                <div class="card-header">
                    <h2 class="card-title">Recent Tasks</h2>
                </div>
                <div class="card-body">
                    <table class="table">
                        <thead>
                            <tr><th>ID</th><th>Type</th><th>State</th><th>Created</th></tr>
                        </thead>
                        <tbody id="tasks-body"></tbody>
                    </table>
                </div>
            </div>

            <div class="card">
                <div class="card-header">
                    <h2 class="card-title">Recent Logs</h2>
                </div>
                <div class="card-body" id="logs-body"></div>
            </div>
        </main>
    </div>

    <script>
        function showTab(tab) {
            document.querySelectorAll('.nav-item').forEach(item => {
                item.classList.remove('active');
            });
            event.target.closest('.nav-item').classList.add('active');
        }

        function updateStatus(connected) {
            const dot = document.getElementById('status-dot');
            const text = document.getElementById('status-text');
            dot.className = 'status-dot ' + (connected ? 'connected' : 'disconnected');
            text.textContent = connected ? 'Connected' : 'Disconnected';
        }

        function updateStats(stats) {
            if (stats) {
                animateValue('stat-total', stats.total || 0);
                animateValue('stat-running', stats.running || 0);
                animateValue('stat-completed', stats.completed || 0);
                animateValue('stat-failed', stats.failed || 0);
            }
        }

        function animateValue(id, newValue) {
            const el = document.getElementById(id);
            const currentValue = parseInt(el.textContent) || 0;
            if (currentValue === newValue) return;
            
            const duration = 500;
            const start = performance.now();
            
            function update(timestamp) {
                const progress = Math.min((timestamp - start) / duration, 1);
                const eased = 1 - Math.pow(1 - progress, 3);
                el.textContent = Math.round(currentValue + (newValue - currentValue) * eased);
                if (progress < 1) requestAnimationFrame(update);
            }
            
            requestAnimationFrame(update);
        }

        function updateTasks(tasks) {
            const tbody = document.getElementById('tasks-body');
            tbody.innerHTML = tasks.slice(-10).reverse().map((t, i) => 
                '<tr style="animation: slideIn 0.3s ease ' + (i * 0.05) + 's both">' +
                '<td style="font-family: monospace; font-size: 13px; color: rgba(0,0,0,0.4);">' + t.ID + '</td>' +
                '<td>' + t.Type + '</td>' +
                '<td><span class="badge badge-' + t.State + '">' + t.State + '</span></td>' +
                '<td style="color: rgba(0,0,0,0.4);">' + new Date(t.CreatedAt).toLocaleString() + '</td></tr>'
            ).join('');
        }

        function updateLogs(logs) {
            const body = document.getElementById('logs-body');
            body.innerHTML = logs.slice(-10).reverse().map((l, i) => 
                '<div class="log-entry" style="animation: slideIn 0.3s ease ' + (i * 0.05) + 's both">' +
                '<span class="log-time">' + new Date(l.Timestamp).toLocaleTimeString() + '</span>' +
                '<span class="log-level ' + l.Level + '">' + l.Level + '</span>' +
                '<span>' + l.Message + '</span></div>'
            ).join('');
        }

        function connect() {
            const evtSource = new EventSource('/events');
            evtSource.onopen = () => updateStatus(true);
            evtSource.onerror = () => {
                updateStatus(false);
                // Auto-reconnect after 3 seconds
                setTimeout(connect, 3000);
            };
            evtSource.addEventListener('update', (e) => {
                try {
                    const data = JSON.parse(e.data);
                    if (data.stats) updateStats(data.stats);
                    if (data.tasks) updateTasks(data.tasks);
                    if (data.logs) updateLogs(data.logs);
                } catch (err) {}
            });
            evtSource.addEventListener('connected', () => {
                updateStatus(true);
            });
        }

        fetch('/api/stats').then(r => r.json()).then(updateStats).catch(() => {});
        fetch('/api/tasks').then(r => r.json()).then(updateTasks).catch(() => {});
        fetch('/api/logs').then(r => r.json()).then(updateLogs).catch(() => {});
        connect();
    </script>
</body>
</html>`
