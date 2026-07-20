import React, { useState, useEffect, useCallback } from 'react';
import ReactFlow, {
  MiniMap,
  Controls,
  Background,
  useNodesState,
  useEdgesState,
  addEdge,
  MarkerType
} from 'reactflow';
import 'reactflow/dist/style.css';
import { Sparkles, Cpu, Send, Layers, RefreshCw, X, Play, AlertTriangle } from 'lucide-react';
import MeshBackground from './MeshBackground';
import WasmNode from './WasmNode';
import AgentNode from './AgentNode';
import SkillNode from './SkillNode';

const nodeTypes = {
  wasmNode: WasmNode,
  agentNode: AgentNode,
  skillNode: SkillNode,
};

export default function App() {
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);
  const [selectedNode, setSelectedNode] = useState(null);
  const [isCreating, setIsCreating] = useState(false);
  const [isLoading, setIsLoading] = useState(false);

  // Form State
  const [taskType, setTaskType] = useState('test-host-funcs');
  const [isAgent, setIsAgent] = useState(false);
  const [userInput, setUserInput] = useState('http://localhost:8080/');
  const [systemPrompt, setSystemPrompt] = useState('You are a structured assistant.');
  const [model, setModel] = useState('gpt-4o');
  const [responseSchema, setResponseSchema] = useState('{"type": "object", "properties": {"result": {"type": "string"}}, "required": ["result"]}');

  const fetchGraph = useCallback(() => {
    setIsLoading(true);
    fetch('/api/v1/workflow/graph')
      .then((res) => res.json())
      .then((resData) => {
        setIsLoading(false);
        if (resData.success && resData.data) {
          setNodes(resData.data.nodes || []);
          setEdges(resData.data.edges || []);
        }
      })
      .catch((e) => {
        setIsLoading(false);
        console.error('Fetch graph error:', e);
      });
  }, [setNodes, setEdges]);

  // Load graph initially
  useEffect(() => {
    fetchGraph();
  }, [fetchGraph]);

  // SSE Live updates
  useEffect(() => {
    const eventSource = new EventSource('/api/v1/events/live');

    const handleUpdate = (event) => {
      try {
        const task = JSON.parse(event.data);
        if (task && task.ID) {
          // Update node state
          setNodes((nds) =>
            nds.map((node) => {
              if (node.id === task.ID) {
                return {
                  ...node,
                  data: {
                    ...node.data,
                    task: task,
                  },
                };
              }
              return node;
            })
          );
          // Check if selected node is the updated one
          setSelectedNode((curr) => {
            if (curr && curr.ID === task.ID) {
              return task;
            }
            return curr;
          });
        }
      } catch (e) {
        // Fallback for non-task SSE payloads
      }
    };

    const events = ['task.created', 'task.started', 'task.completed', 'task.failed', 'task.retried', 'task.cancelled'];
    events.forEach((evtName) => {
      eventSource.addEventListener(evtName, handleUpdate);
    });

    return () => {
      eventSource.close();
    };
  }, [setNodes]);

  // Connect dependency
  const onConnect = useCallback(
    (params) => {
      fetch(`/api/v1/tasks/${params.target}/dependencies`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dependency_id: params.source }),
      })
        .then((res) => res.json())
        .then((resData) => {
          if (resData.success) {
            fetchGraph();
          } else {
            alert('Failed to connect dependency: ' + resData.error);
          }
        });
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
      input: btoa(userInput), // Base64 encode []byte
      is_agent: isAgent,
    };

    if (isAgent) {
      payload.agent_config = {
        system_prompt: systemPrompt,
        model: model,
        schema: responseSchema,
      };
    }

    fetch('/api/v1/tasks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
      .then((res) => res.json())
      .then((resData) => {
        if (resData.success) {
          setIsCreating(false);
          fetchGraph();
        } else {
          alert('Create task failed: ' + resData.error);
        }
      });
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
          fitView
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
              <span style={{ fontSize: '11px', color: '#64748b', fontFamily: 'monospace' }}>{selectedNode.ID}</span>
            </div>
            <button onClick={() => setSelectedNode(null)} style={{ background: 'transparent', border: 'none', color: '#94a3b8', cursor: 'pointer' }}>
              <X className="w-5 h-5" />
            </button>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
            <div>
              <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Type</label>
              <div style={{ fontSize: '14px', color: '#f1f5f9', fontWeight: 500 }}>{selectedNode.Type}</div>
            </div>

            <div>
              <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>State</label>
              <span style={{
                display: 'inline-block',
                fontSize: '11px',
                fontWeight: 600,
                padding: '3px 10px',
                borderRadius: '9999px',
                background: selectedNode.State === 'completed' ? 'rgba(16, 185, 129, 0.1)' : selectedNode.State === 'running' ? 'rgba(139, 92, 246, 0.1)' : selectedNode.State === 'failed' ? 'rgba(239, 68, 68, 0.1)' : 'rgba(148, 163, 184, 0.1)',
                color: selectedNode.State === 'completed' ? '#34d399' : selectedNode.State === 'running' ? '#a78bfa' : selectedNode.State === 'failed' ? '#f87171' : '#cbd5e1',
                border: '1px solid rgba(255, 255, 255, 0.05)'
              }}>
                {selectedNode.State.toUpperCase()}
              </span>
            </div>

            {selectedNode.IsAgent && selectedNode.AgentConfig && (
              <div style={{ border: '1px solid rgba(217, 70, 239, 0.15)', background: 'rgba(217, 70, 239, 0.02)', padding: '12px', borderRadius: '10px' }}>
                <label style={{ fontSize: '11px', color: '#d946ef', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '6px' }}>Agent Configuration</label>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', fontSize: '12px' }}>
                  <div><span style={{ color: '#94a3b8' }}>Model:</span> <span style={{ color: '#f1f5f9', fontFamily: 'monospace' }}>{selectedNode.AgentConfig.Model}</span></div>
                  <div><span style={{ color: '#94a3b8' }}>System Prompt:</span> <div style={{ color: '#e2e8f0', marginTop: '2px', background: 'rgba(0,0,0,0.2)', padding: '6px', borderRadius: '6px' }}>{selectedNode.AgentConfig.SystemPrompt}</div></div>
                </div>
              </div>
            )}

            <div>
              <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Input</label>
              <pre style={{ margin: 0, padding: '10px', background: 'rgba(0,0,0,0.3)', borderRadius: '8px', fontSize: '12px', color: '#38bdf8', overflowX: 'auto', fontFamily: 'monospace' }}>
                {atob(selectedNode.Input)}
              </pre>
            </div>

            {selectedNode.Result && (
              <div>
                <label style={{ fontSize: '11px', color: '#64748b', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '4px' }}>Result</label>
                <pre style={{ margin: 0, padding: '10px', background: 'rgba(0,0,0,0.4)', border: '1px solid rgba(16, 185, 129, 0.15)', borderRadius: '8px', fontSize: '12px', color: '#34d399', overflowX: 'auto', fontFamily: 'monospace', whiteSpace: 'pre-wrap' }}>
                  {new TextDecoder().decode(Uint8Array.from(atob(selectedNode.Result), c => c.charCodeAt(0)))}
                </pre>
              </div>
            )}

            {selectedNode.Error && (
              <div style={{ border: '1px solid rgba(239, 68, 68, 0.2)', background: 'rgba(239, 68, 68, 0.05)', padding: '12px', borderRadius: '10px', display: 'flex', gap: '8px', alignItems: 'start' }}>
                <AlertTriangle className="w-5 h-5 text-red-400" style={{ flexShrink: 0, marginTop: '2px' }} />
                <div>
                  <label style={{ fontSize: '11px', color: '#f87171', textTransform: 'uppercase', fontWeight: 600, display: 'block' }}>Error Details</label>
                  <p style={{ margin: '4px 0 0 0', fontSize: '12px', color: '#fca5a5', lineHeight: 1.4 }}>{selectedNode.Error}</p>
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
