import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  ReactFlow,
  MiniMap,
  Controls,
  Background,
  useNodesState,
  useEdgesState,
  MarkerType
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { Sparkles, Send, Layers, RefreshCw, X, AlertTriangle } from 'lucide-react';
import MeshBackground from './MeshBackground';
import WasmNode from './WasmNode';
import AgentNode from './AgentNode';
import SkillNode from './SkillNode';
import { getToken, setToken, apiFetch, sseFetch, ApiError } from './api';

const nodeTypes = {
  wasmNode: WasmNode,
  agentNode: AgentNode,
  skillNode: SkillNode,
};

// The fitView prop only runs on mount, when the graph is still empty. Framing
  // is left to the operator via the Controls: calling fitView() as soon as the
  // nodes exist crashes the renderer here, and a blank canvas with a working
  // zoom control beats a canvas that reloads itself.

const GRAPH_PATH = '/api/v1/workflow/graph';

// The server tags each payload with how it was stored (input_encoding /
// result_encoding). A plain-text value run through atob() throws
// InvalidCharacterError, so the tag - not the shape of the string - decides.
function decodePayload(value, encoding) {
  if (!value) return '';
  if (encoding !== 'base64') return value;
  try {
    const binary = atob(value);
    return new TextDecoder().decode(Uint8Array.from(binary, (c) => c.charCodeAt(0)));
  } catch {
    return value;
  }
}

// Shared by the canvas and the startup probe, so both report failure the same way.
function ErrorBanner({ message }) {
  if (!message) return null;
  return (
    <div
      role="alert"
      style={{
        position: 'absolute',
        top: '84px',
        left: '20px',
        right: '20px',
        padding: '10px 14px',
        borderRadius: '10px',
        background: 'rgba(120, 20, 20, 0.85)',
        color: '#fff',
        fontSize: '13px',
        zIndex: 10,
      }}
    >
      {message}
    </div>
  );
}

// Neutral placeholder shown while we work out whether this server wants a
// credential. It must not hint at either answer before the probe replies.
function Splash({ children }) {
  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        position: 'relative',
      }}
    >
      <MeshBackground />
      <div
        style={{
          position: 'relative',
          display: 'flex',
          alignItems: 'center',
          gap: '10px',
          color: '#94a3b8',
          fontSize: '13px',
        }}
      >
        <RefreshCw className="w-4 h-4 animate-spin" style={{ width: '16px', height: '16px' }} />
        <span>{children}</span>
      </div>
    </div>
  );
}

function Canvas() {
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);
  const [selectedNode, setSelectedNode] = useState(null);

  // Derived once, here: the API speaks lower-case JSON keys and tags every
  // payload with its encoding, so the detail panel never guesses either.
  const selectedState = (selectedNode && selectedNode.state) || 'pending';
  const selectedInput = selectedNode
    ? decodePayload(selectedNode.input, selectedNode.input_encoding)
    : '';
  const selectedResult = selectedNode
    ? decodePayload(selectedNode.result, selectedNode.result_encoding)
    : '';
  const selectedAgent = (selectedNode && selectedNode.agent_config) || null;
  const [isCreating, setIsCreating] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState('');

  // Form State
  const [taskType, setTaskType] = useState('test-host-funcs');
  const [isAgent, setIsAgent] = useState(false);
  const [userInput, setUserInput] = useState('http://localhost:8080/');
  const [systemPrompt, setSystemPrompt] = useState('You are a structured assistant.');
  const [model, setModel] = useState('gpt-4o');
  const [responseSchema, setResponseSchema] = useState('{"type": "object", "properties": {"result": {"type": "string"}}, "required": ["result"]}');

  // setNodes/setEdges are React state setters, so an empty dependency list is
  // both correct and stable. Depending on them re-created this callback on every
  // render, which re-ran the effect below and re-fetched the graph each time.
  const fetchGraph = useCallback(() => {
    setIsLoading(true);
    return apiFetch(GRAPH_PATH)
      .then((resData) => {
        setIsLoading(false);
        setError('');
        if (resData.success && resData.data) {
          setNodes(resData.data.nodes || []);
          setEdges(resData.data.edges || []);
        }
      })
      .catch((e) => {
        setIsLoading(false);
        setError(e.message || 'the server did not answer');
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Load graph initially
  useEffect(() => {
    fetchGraph();
  }, [fetchGraph]);

  // An event for a task this canvas does not hold yet cannot be painted onto a
  // node that is not here: position, depth and edges are the server's layout.
  // A burst of task.created events - a workflow fanning out, a scheduler firing -
  // is collapsed into one refetch per window instead of one request per task.
  const refetchTimer = useRef(null);
  const scheduleRefetch = useCallback(() => {
    if (refetchTimer.current) return;
    refetchTimer.current = setTimeout(() => {
      refetchTimer.current = null;
      fetchGraph();
    }, 250);
  }, [fetchGraph]);

  // SSE Live updates. sseFetch hands the event name over as the second argument.
  const handleUpdate = useCallback((raw, eventName) => {
    let task = null;
    try {
      task = JSON.parse(raw);
    } catch {
      // Not a task payload (stream.opened, a system event); nothing to paint.
      return;
    }
    // A task this caller does not own arrives as {"hidden":true,...} rather than
    // as data, so there is deliberately nothing to update here.
    // Lower-case JSON keys: the event payload is the same task object the
    // graph endpoint returns, not the Go struct's exported field names.
    if (!task || !task.id) return;

    // task.created changes the shape of the graph, so ask the server for it
    // again. Every event after it only fills in fields we already hold - those
    // are patched in place and cost no request at all.
    if (eventName === 'task.created') {
      scheduleRefetch();
      return;
    }

    setNodes((nds) =>
      nds.map((node) =>
        node.id === task.id ? { ...node, data: { ...node.data, task } } : node
      )
    );
    setSelectedNode((curr) => (curr && curr.id === task.id ? task : curr));
  }, [setNodes, scheduleRefetch]);

  useEffect(() => {
    const controller = new AbortController();
    // These names are the server's EventType values; see pkg/event/event.go.
    const names = [
      'task.created',
      'task.started',
      'task.completed',
      'task.failed',
      'task.retried',
      'task.cancelled',
    ];
    const handlers = {};
    names.forEach((name) => {
      handlers[name] = handleUpdate;
    });

    sseFetch('/api/v1/events/live', handlers, controller.signal).catch((e) => {
      if (e.name !== 'AbortError') setError(e.message);
    });

    return () => {
      controller.abort();
      if (refetchTimer.current) {
        clearTimeout(refetchTimer.current);
        refetchTimer.current = null;
      }
    };
  }, [handleUpdate]);

  // Escape closes the detail drawer. Without this the X button is the only way
  // out, which reads as "the panel is stuck" to anyone who reaches for Escape
  // first.
  useEffect(() => {
    if (!selectedNode) return undefined;
    const onKey = (e) => {
      if (e.key === 'Escape') setSelectedNode(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [selectedNode]);

  // Connect dependency
  const onConnect = useCallback(
    (params) => {
      apiFetch(`/api/v1/tasks/${params.target}/dependencies`, {
        method: 'POST',
        body: JSON.stringify({ dependency_id: params.source }),
      })
        .then((resData) => {
          if (resData.success) {
            setError('');
            fetchGraph();
          }
        })
        .catch((e) => setError(e.message));
    },
    [fetchGraph]
  );

  // Click node to view details
  const onNodeClick = (event, node) => {
    setSelectedNode(node.data.task);
  };

  // Submit task
  const handleSubmit = (e) => {
    e.preventDefault();
    const payload = {
      type: taskType,
      config: {},
      // input_text is the plain-text path. The old code sent btoa(userInput) with
      // no input_encoding, and the server took the base64 text as the payload
      // verbatim - the task silently ran against "aHR0cDov..." instead of the URL.
      input_text: userInput,
      is_agent: isAgent,
    };

    if (isAgent) {
      payload.agent_config = {
        system_prompt: systemPrompt,
        model: model,
        schema: responseSchema,
      };
    }

    apiFetch('/api/v1/tasks', { method: 'POST', body: JSON.stringify(payload) })
      .then((resData) => {
        if (resData.success) {
          setIsCreating(false);
          setError('');
          fetchGraph();
        }
      })
      .catch((e) => setError(e.message));
  };

  return (
    <div style={{ width: '100%', height: '100%', position: 'relative' }}>
      <MeshBackground />

      {/* Main Canvas Header */}
      <header className="glass-panel" style={{
        position: 'absolute',
        top: '20px',
        left: '20px',
        right: '20px',
        zIndex: 10,
        padding: '12px 24px',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        border: '1px solid rgba(255, 255, 255, 0.08)'
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
          <div style={{
            background: 'linear-gradient(135deg, #8b5cf6, #d946ef)',
            width: '32px',
            height: '32px',
            borderRadius: '8px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            boxShadow: '0 0 15px rgba(139, 92, 246, 0.5)'
          }}>
            <Sparkles className="w-5 h-5" style={{ color: '#fff', width: '18px', height: '18px' }} />
          </div>
          <div>
            <h1 style={{ fontSize: '18px', margin: 0, fontWeight: 700, letterSpacing: '-0.5px', color: '#f8fafc', textShadow: '0 2px 4px rgba(0,0,0,0.5)' }}>
              LoopWorker Canvas
            </h1>
            <span style={{ fontSize: '10px', color: '#94a3b8', display: 'block' }}>V6 Agentic Orchestration Engine</span>
          </div>
        </div>

        <div style={{ display: 'flex', gap: '12px' }}>
          <button onClick={fetchGraph} className="glass-card" style={{
            padding: '8px 14px',
            color: '#fff',
            display: 'flex',
            alignItems: 'center',
            gap: '6px',
            fontSize: '13px',
            cursor: 'pointer',
            border: '1px solid rgba(255, 255, 255, 0.08)',
            background: 'rgba(255, 255, 255, 0.02)'
          }}>
            <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} style={{ width: '14px', height: '14px' }} />
            Sync
          </button>
          <button onClick={() => setIsCreating(true)} className="glass-card" style={{
            padding: '8px 16px',
            background: 'linear-gradient(135deg, #8b5cf6, #6d28d9)',
            border: 'none',
            color: '#fff',
            display: 'flex',
            alignItems: 'center',
            gap: '6px',
            fontSize: '13px',
            fontWeight: 600,
            cursor: 'pointer',
            boxShadow: '0 4px 15px rgba(139, 92, 246, 0.3)'
          }}>
            <Layers className="w-4 h-4" style={{ width: '14px', height: '14px' }} />
            New Node
          </button>
        </div>
      </header>

      <ErrorBanner message={error} />

      {/* React Flow Editor */}
      <div style={{ width: '100%', height: '100%', zIndex: 1 }}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onNodeClick={onNodeClick}
          nodeTypes={nodeTypes}
          defaultViewport={{ x: 0, y: 0, zoom: 1 }}
          defaultEdgeOptions={{
            type: 'smoothstep',
            markerEnd: {
              type: MarkerType.ArrowClosed,
              color: 'rgba(255, 255, 255, 0.2)'
            },
            style: { stroke: 'rgba(255, 255, 255, 0.15)' }
          }}
        >
          <Background color="rgba(255, 255, 255, 0.02)" gap={16} />
          <Controls />
          <MiniMap 
            nodeColor={(node) => {
              if (node.type === 'agentNode') return 'rgba(217, 70, 239, 0.3)';
              return 'rgba(59, 130, 246, 0.3)';
            }}
            maskColor="rgba(0, 0, 0, 0.6)"
            style={{
              background: 'rgba(0, 0, 0, 0.5)',
              border: '1px solid rgba(255, 255, 255, 0.05)',
              borderRadius: '10px',
              backdropFilter: 'blur(10px)'
            }}
          />
        </ReactFlow>
      </div>

      {/* Slide Sidebar: Task Details */}
      {selectedNode && (
        <div className="glass-panel" style={{
          position: 'absolute',
          top: '90px',
          right: '20px',
          bottom: '20px',
          width: '380px',
          zIndex: 10,
          padding: '24px',
          overflowY: 'auto',
          border: '1px solid rgba(255, 255, 255, 0.08)',
          textAlign: 'left'
        }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', borderBottom: '1px solid rgba(255, 255, 255, 0.08)', paddingBottom: '16px', marginBottom: '20px' }}>
            <div>
              <h2 style={{ fontSize: '18px', margin: 0, fontWeight: 700, color: '#f8fafc' }}>Task Details</h2>
              <span style={{ fontSize: '11px', color: '#64748b', fontFamily: 'monospace' }}>{selectedNode.id}</span>
            </div>
            <button onClick={() => setSelectedNode(null)} style={{ background: 'transparent', border: 'none', color: '#94a3b8', cursor: 'pointer' }}>
              <X className="w-5 h-5" />
            </button>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
            <div>
              <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Type</label>
              <div style={{ fontSize: '14px', color: '#f1f5f9', fontWeight: 500 }}>{selectedNode.type}</div>
            </div>

            <div>
              <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>State</label>
              <span style={{
                display: 'inline-block',
                fontSize: '11px',
                fontWeight: 600,
                padding: '3px 10px',
                borderRadius: '9999px',
                background: selectedState === 'completed' ? 'rgba(16, 185, 129, 0.1)' : selectedState === 'running' ? 'rgba(139, 92, 246, 0.1)' : selectedState === 'failed' ? 'rgba(239, 68, 68, 0.1)' : 'rgba(148, 163, 184, 0.1)',
                color: selectedState === 'completed' ? '#34d399' : selectedState === 'running' ? '#a78bfa' : selectedState === 'failed' ? '#f87171' : '#cbd5e1',
                border: '1px solid rgba(255, 255, 255, 0.05)'
              }}>
                {selectedState.toUpperCase()}
              </span>
            </div>

            {selectedNode.is_agent && selectedAgent && (
              <div style={{ border: '1px solid rgba(217, 70, 239, 0.15)', background: 'rgba(217, 70, 239, 0.02)', padding: '12px', borderRadius: '10px' }}>
                <label style={{ fontSize: '11px', color: '#d946ef', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '6px' }}>Agent Configuration</label>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', fontSize: '12px' }}>
                  <div><span style={{ color: '#94a3b8' }}>Model:</span> <span style={{ color: '#f1f5f9', fontFamily: 'monospace' }}>{selectedAgent.model}</span></div>
                  <div><span style={{ color: '#94a3b8' }}>System Prompt:</span> <div style={{ color: '#e2e8f0', marginTop: '2px', background: 'rgba(0,0,0,0.2)', padding: '6px', borderRadius: '6px' }}>{selectedAgent.system_prompt}</div></div>
                </div>
              </div>
            )}

            <div>
              <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Input</label>
              <pre style={{ margin: 0, padding: '10px', background: 'rgba(0,0,0,0.3)', borderRadius: '8px', fontSize: '12px', color: '#38bdf8', overflowX: 'auto', fontFamily: 'monospace' }}>
                {selectedInput}
              </pre>
            </div>

            {selectedResult && (
              <div>
                <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Result</label>
                <pre style={{ margin: 0, padding: '10px', background: 'rgba(0,0,0,0.4)', border: '1px solid rgba(16, 185, 129, 0.15)', borderRadius: '8px', fontSize: '12px', color: '#34d399', overflowX: 'auto', fontFamily: 'monospace', whiteSpace: 'pre-wrap' }}>
                  {selectedResult}
                </pre>
              </div>
            )}

            {selectedNode.error && (
              <div style={{ border: '1px solid rgba(239, 68, 68, 0.2)', background: 'rgba(239, 68, 68, 0.05)', padding: '12px', borderRadius: '10px', display: 'flex', gap: '8px', alignItems: 'start' }}>
                <AlertTriangle className="w-5 h-5 text-red-400" style={{ flexShrink: 0, marginTop: '2px' }} />
                <div>
                  <label style={{ fontSize: '11px', color: '#f87171', textTransform: 'uppercase', fontWeight: 600, display: 'block' }}>Error Details</label>
                  <p style={{ margin: '4px 0 0 0', fontSize: '12px', color: '#fca5a5', lineHeight: 1.4 }}>{selectedNode.error}</p>
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Modal: New Node Creation */}
      {isCreating && (
        <div style={{
          position: 'absolute',
          top: 0,
          left: 0,
          width: '100%',
          height: '100%',
          background: 'rgba(0, 0, 0, 0.6)',
          backdropFilter: 'blur(6px)',
          zIndex: 20,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center'
        }}>
          <div className="glass-panel" style={{
            width: '450px',
            padding: '30px',
            border: '1px solid rgba(255, 255, 255, 0.08)',
            textAlign: 'left'
          }}>
            <div style={{ display: 'flex', alignItems: 'center', justify: 'between', borderBottom: '1px solid rgba(255, 255, 255, 0.08)', paddingBottom: '12px', marginBottom: '20px' }}>
              <h2 style={{ fontSize: '20px', margin: 0, fontWeight: 700, color: '#f8fafc' }}>Create Workflow Node</h2>
              <button onClick={() => setIsCreating(false)} style={{ marginLeft: 'auto', background: 'transparent', border: 'none', color: '#94a3b8', cursor: 'pointer' }}>
                <X className="w-5 h-5" />
              </button>
            </div>

            <form onSubmit={handleSubmit} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={{ fontSize: '12px', color: '#94a3b8', fontWeight: 600, display: 'block', marginBottom: '6px' }}>Node Type (WASM / Plugin Name)</label>
                <input value={taskType} onChange={(e) => setTaskType(e.target.value)} required style={{
                  width: '100%',
                  padding: '10px',
                  background: 'rgba(0,0,0,0.3)',
                  border: '1px solid rgba(255,255,255,0.08)',
                  borderRadius: '8px',
                  color: '#fff',
                  boxSizing: 'border-box'
                }} />
              </div>

              <div style={{ display: 'flex', alignItems: 'center', gap: '10px', padding: '10px', background: 'rgba(255,255,255,0.02)', border: '1px solid rgba(255,255,255,0.05)', borderRadius: '8px' }}>
                <input type="checkbox" checked={isAgent} onChange={(e) => setIsAgent(e.target.checked)} id="isAgentCheck" style={{ width: '16px', height: '16px' }} />
                <label htmlFor="isAgentCheck" style={{ fontSize: '13px', color: '#f1f5f9', fontWeight: 600, cursor: 'pointer' }}>Enable AI Agent Capability</label>
              </div>

              {isAgent ? (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px', borderLeft: '2px solid #d946ef', paddingLeft: '14px', margin: '4px 0' }}>
                  <div>
                    <label style={{ fontSize: '11px', color: '#d946ef', fontWeight: 600, display: 'block', marginBottom: '4px' }}>System Prompt</label>
                    <textarea value={systemPrompt} onChange={(e) => setSystemPrompt(e.target.value)} style={{
                      width: '100%',
                      padding: '8px',
                      background: 'rgba(0,0,0,0.3)',
                      border: '1px solid rgba(255,255,255,0.08)',
                      borderRadius: '6px',
                      color: '#fff',
                      minHeight: '60px',
                      boxSizing: 'border-box'
                    }} />
                  </div>
                  <div>
                    <label style={{ fontSize: '11px', color: '#d946ef', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Model</label>
                    <input value={model} onChange={(e) => setModel(e.target.value)} style={{
                      width: '100%',
                      padding: '8px',
                      background: 'rgba(0,0,0,0.3)',
                      border: '1px solid rgba(255,255,255,0.08)',
                      borderRadius: '6px',
                      color: '#fff',
                      boxSizing: 'border-box'
                    }} />
                  </div>
                  <div>
                    <label style={{ fontSize: '11px', color: '#d946ef', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Response Schema (JSON Schema)</label>
                    <textarea value={responseSchema} onChange={(e) => setResponseSchema(e.target.value)} style={{
                      width: '100%',
                      padding: '8px',
                      background: 'rgba(0,0,0,0.3)',
                      border: '1px solid rgba(255,255,255,0.08)',
                      borderRadius: '6px',
                      color: '#a78bfa',
                      fontFamily: 'monospace',
                      fontSize: '11px',
                      minHeight: '60px',
                      boxSizing: 'border-box'
                    }} />
                  </div>
                </div>
              ) : null}

              <div>
                <label style={{ fontSize: '12px', color: '#94a3b8', fontWeight: 600, display: 'block', marginBottom: '6px' }}>Input Data (Plain Text / URL)</label>
                <textarea value={userInput} onChange={(e) => setUserInput(e.target.value)} required style={{
                  width: '100%',
                  padding: '10px',
                  background: 'rgba(0,0,0,0.3)',
                  border: '1px solid rgba(255,255,255,0.08)',
                  borderRadius: '8px',
                  color: '#fff',
                  minHeight: '60px',
                  boxSizing: 'border-box'
                }} />
              </div>

              <button type="submit" style={{
                marginTop: '10px',
                padding: '12px',
                background: 'linear-gradient(135deg, #8b5cf6, #6d28d9)',
                border: 'none',
                borderRadius: '8px',
                color: '#fff',
                fontWeight: 600,
                fontSize: '14px',
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: '8px',
                boxShadow: '0 4px 15px rgba(139, 92, 246, 0.4)'
              }}>
                <Send className="w-4 h-4" />
                Deploy Node
              </button>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

function TokenGate({ onUnlock }) {
  const [value, setValue] = useState('');

  const submit = (e) => {
    e.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) return;
    setToken(trimmed);
    onUnlock(trimmed);
  };

  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <MeshBackground />
      <form
        onSubmit={submit}
        className="glass-panel"
        style={{ position: 'relative', width: '420px', padding: '28px', borderRadius: '16px' }}
      >
        <h2 style={{ margin: '0 0 6px', fontSize: '18px' }}>Connect to LoopWorker</h2>
        <p style={{ margin: '0 0 16px', fontSize: '13px', opacity: 0.75 }}>
          This server requires an API key. It is printed once in the server log when it
          starts; paste it here. It is stored in this browser only.
        </p>
        <input
          type="password"
          value={value}
          autoFocus
          onChange={(e) => setValue(e.target.value)}
          placeholder="X-API-Key"
          style={{
            width: '100%',
            padding: '10px 12px',
            borderRadius: '8px',
            border: '1px solid rgba(255,255,255,0.2)',
            background: 'rgba(0,0,0,0.25)',
            color: 'inherit',
            boxSizing: 'border-box',
          }}
        />
        <button
          type="submit"
          style={{ marginTop: '14px', width: '100%', padding: '10px', cursor: 'pointer' }}
        >
          Connect
        </button>
      </form>
    </div>
  );
}

export default function App() {
  const [token, setTokenState] = useState(getToken());
  // 'probing' -> 'anonymous' | 'locked' | 'error'. The gate is a reaction to a
  // 401, never a precondition for making a request.
  const [probe, setProbe] = useState(() => (getToken() ? 'anonymous' : 'probing'));
  const [probeError, setProbeError] = useState('');
  const [attempt, setAttempt] = useState(0);
  // No "already started" guard here. Under a double-invoked effect (React's
  // StrictMode remount) such a guard makes the second pass return early while
  // the first pass has already marked itself stale, so the probe never settles
  // and the splash screen never clears. Probing twice is harmless; hanging
  // forever is not.
  useEffect(() => {
    if (token) return undefined;

    let live = true;
    // No token in localStorage, so apiFetch sends this unauthenticated.
    apiFetch(GRAPH_PATH)
      .then(() => {
        if (live) setProbe('anonymous');
      })
      .catch((e) => {
        if (!live) return;
        // Only an explicit refusal means "credential required". A dead network or
        // a 5xx is not a credential problem and must not put a key form on screen.
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
          setProbe('locked');
          return;
        }
        setProbeError(e.message);
        setProbe('error');
      });

    return () => {
      live = false;
    };
  }, [token, attempt]);

  const retry = () => {
    setAttempt((n) => n + 1);
  };

  if (token || probe === 'anonymous') {
    // key remounts the canvas when the credential changes, so no stale state or
    // half-open stream survives a re-connect.
    return <Canvas key={token || 'anonymous'} />;
  }
  if (probe === 'locked') {
    return <TokenGate onUnlock={setTokenState} />;
  }
  if (probe === 'error') {
    return (
      <div style={{ width: '100%', height: '100%', position: 'relative' }}>
        <MeshBackground />
        <ErrorBanner message={probeError} />
        <div
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <button onClick={retry} className="glass-card" style={{
            padding: '10px 18px',
            color: '#fff',
            cursor: 'pointer',
            border: '1px solid rgba(255, 255, 255, 0.08)',
            background: 'rgba(255, 255, 255, 0.02)'
          }}>
            Retry connection
          </button>
        </div>
      </div>
    );
  }
  return <Splash>Connecting to LoopWorker…</Splash>;
}

