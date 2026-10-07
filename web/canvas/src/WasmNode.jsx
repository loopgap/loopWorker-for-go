import React from 'react';
import { Handle, Position } from '@xyflow/react';
import { Cpu, CheckCircle, Play, XCircle, AlertCircle } from 'lucide-react';

export default function WasmNode({ data }) {
  const task = data.task || {};
  // The API serialises tasks with lower-case JSON keys (see the graph handler),
  // so reading task.State here silently showed every task as PENDING.
  const state = task.state || 'pending';

  const getStateStyles = () => {
    switch (state) {
      case 'completed':
        return {
          glow: 'node-glow-completed',
          border: 'border-emerald-500/30',
          badge: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20',
          icon: <CheckCircle className="w-3.5 h-3.5 text-emerald-400" />
        };
      case 'running':
        return {
          glow: 'node-glow-running',
          border: 'border-violet-500/30',
          badge: 'bg-violet-500/10 text-violet-400 border-violet-500/20 animate-pulse',
          icon: <Play className="w-3.5 h-3.5 text-violet-400 animate-spin" style={{ animationDuration: '3s' }} />
        };
      case 'failed':
        return {
          glow: 'node-glow-failed',
          border: 'border-red-500/30',
          badge: 'bg-red-500/10 text-red-400 border-red-500/20',
          icon: <XCircle className="w-3.5 h-3.5 text-red-400" />
        };
      default:
        return {
          glow: '',
          border: 'border-slate-500/20',
          badge: 'bg-slate-500/10 text-slate-400 border-slate-500/20',
          icon: <AlertCircle className="w-3.5 h-3.5 text-slate-400" />
        };
    }
  };

  const styles = getStateStyles();

  return (
    <div className={`glass-card p-4 min-w-[200px] border ${styles.border} ${styles.glow}`} style={{ textAlign: 'left' }}>
      <Handle type="target" position={Position.Left} />
      
      <div className="flex items-center gap-3" style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
        <div style={{
          padding: '8px',
          background: 'rgba(59, 130, 246, 0.1)',
          borderRadius: '8px',
          border: '1px solid rgba(59, 130, 246, 0.2)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center'
        }}>
          <Cpu className="w-5 h-5 text-blue-400" style={{ width: '20px', height: '20px', color: '#60a5fa' }} />
        </div>
        <div style={{ flex: 1 }}>
          <h4 style={{ margin: 0, fontWeight: 600, fontSize: '14px', color: '#e2e8f0' }}>{data.label}</h4>
          <span style={{ fontSize: '10px', color: '#94a3b8', fontFamily: 'monospace', display: 'block', marginTop: '2px' }}>
            {task.id || 'ID: Pending'}
          </span>
        </div>
      </div>

      <div style={{ marginTop: '12px', display: 'flex', alignItems: 'center', justifyContent: 'between', gap: '8px' }}>
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: '4px',
          fontSize: '10px',
          fontWeight: 500,
          padding: '2px 8px',
          borderRadius: '9999px',
          background: state === 'completed' ? 'rgba(16, 185, 129, 0.1)' : state === 'running' ? 'rgba(139, 92, 246, 0.1)' : state === 'failed' ? 'rgba(239, 68, 68, 0.1)' : 'rgba(148, 163, 184, 0.1)',
          color: state === 'completed' ? '#34d399' : state === 'running' ? '#a78bfa' : state === 'failed' ? '#f87171' : '#cbd5e1',
          border: '1px solid rgba(255, 255, 255, 0.05)'
        }}>
          {styles.icon}
          {state.toUpperCase()}
        </div>
        {task.retry > 0 && (
          <span style={{ fontSize: '10px', color: '#fbbf24', marginLeft: 'auto', fontFamily: 'monospace' }}>
            Retry: {task.retry}
          </span>
        )}
      </div>

      <Handle type="source" position={Position.Right} />
    </div>
  );
}
