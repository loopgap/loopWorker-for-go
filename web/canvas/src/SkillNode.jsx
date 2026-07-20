import React from 'react';
import { Handle, Position } from 'reactflow';
import { Sparkles, CheckCircle, Play, XCircle, AlertCircle } from 'lucide-react';

export default function SkillNode({ data }) {
  const skill = data.skill || {};
  const state = skill.state || 'idle';

  const getStateStyles = () => {
    switch (state) {
      case 'invoked':
        return {
          glow: 'node-glow-running',
          border: 'border-amber-500/30',
          badge: 'bg-amber-500/10 text-amber-400 border-amber-500/20 animate-pulse',
          icon: <Play className="w-3.5 h-3.5 text-amber-400 animate-spin" style={{ animationDuration: '3s' }} />
        };
      case 'completed':
        return {
          glow: 'node-glow-completed',
          border: 'border-emerald-500/30',
          badge: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20',
          icon: <CheckCircle className="w-3.5 h-3.5 text-emerald-400" />
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
          border: 'border-amber-500/20',
          badge: 'bg-amber-500/10 text-amber-300 border-amber-500/20',
          icon: <Sparkles className="w-3.5 h-3.5 text-amber-300" />
        };
    }
  };

  const styles = getStateStyles();

  return (
    <div className={`glass-card p-4 min-w-[180px] border ${styles.border} ${styles.glow}`} style={{ textAlign: 'left' }}>
      <Handle type="target" position={Position.Left} />

      <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
        <div style={{
          padding: '8px',
          background: 'rgba(245, 158, 11, 0.1)',
          borderRadius: '8px',
          border: '1px solid rgba(245, 158, 11, 0.2)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center'
        }}>
          <Sparkles className="w-5 h-5" style={{ width: '20px', height: '20px', color: '#fbbf24' }} />
        </div>
        <div style={{ flex: 1 }}>
          <h4 style={{ margin: 0, fontWeight: 600, fontSize: '13px', color: '#fef3c7' }}>{skill.name || 'Skill'}</h4>
          <span style={{ fontSize: '10px', color: '#94a3b8', fontFamily: 'monospace', display: 'block', marginTop: '2px' }}>
            {skill.version || 'v1.0'}
          </span>
        </div>
      </div>

      <div style={{ marginTop: '10px', display: 'flex', alignItems: 'center', gap: '8px' }}>
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: '4px',
          fontSize: '10px',
          fontWeight: 500,
          padding: '2px 8px',
          borderRadius: '9999px',
          background: state === 'completed' ? 'rgba(16, 185, 129, 0.1)' : state === 'invoked' ? 'rgba(245, 158, 11, 0.1)' : state === 'failed' ? 'rgba(239, 68, 68, 0.1)' : 'rgba(148, 163, 184, 0.1)',
          color: state === 'completed' ? '#34d399' : state === 'invoked' ? '#fbbf24' : state === 'failed' ? '#f87171' : '#cbd5e1',
          border: '1px solid rgba(255, 255, 255, 0.05)'
        }}>
          {styles.icon}
          {state.toUpperCase()}
        </div>
        {skill.duration && (
          <span style={{ fontSize: '10px', color: '#94a3b8', marginLeft: 'auto', fontFamily: 'monospace' }}>
            {skill.duration}ms
          </span>
        )}
      </div>

      <Handle type="source" position={Position.Right} />
    </div>
  );
}
