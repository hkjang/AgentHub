import { FormEvent, useEffect, useState } from 'react'
import { ArrowRight, Boxes, LockKeyhole, ShieldCheck } from 'lucide-react'
import { api } from '../api'
import type { Version } from '../types'
import { ErrorBanner } from '../components/UI'

export function Login({version,onLogin}:{version:Version;onLogin:()=>Promise<void>}) {
  const [methods,setMethods]=useState({local:true,oidc:false,oidcLabel:'Keycloak SSO'})
  const [username,setUsername]=useState(''),[password,setPassword]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false)
  useEffect(()=>{api.get<typeof methods>('/api/v1/auth/methods').then(setMethods).catch(()=>undefined)},[])
  const submit=async(event:FormEvent)=>{event.preventDefault();setError('');setBusy(true);try{await api.post('/api/v1/auth/login',{username,password});await onLogin()}catch(err){setError(err instanceof Error?err.message:'로그인하지 못했습니다.')}finally{setBusy(false)}}
  return <main className="login-page">
    <section className="login-story"><div className="story-glow"/><div className="login-brand"><img src="/logo.svg" alt="AgentHub Logo" className="brand-logo-img large" /><span>AgentHub</span></div><div className="story-copy"><span className="eyebrow light">Enterprise Agent Runtime Platform</span><h1>Agent가 일할<br/><em>안전한 공간.</em></h1><p>Agent Runtime, Workspace, MCP와 Model을 하나의 통제면에서 운영하세요.</p><div className="feature-row"><span><Boxes size={18}/>영속 작업공간</span><span><ShieldCheck size={18}/>Policy by Default</span></div></div><div className="story-foot"><span className="status-dot online"/>Offline-ready control plane</div></section>
    <section className="login-form-side"><div className="login-card"><div className="login-heading"><div className="login-lock"><LockKeyhole size={22}/></div><h2>AgentHub에 로그인</h2><p>조직 계정으로 Runtime Workspace에 접속합니다.</p></div>{error&&<ErrorBanner message={error}/>}
      {methods.oidc&&<><a className="button primary wide" href="/api/v1/auth/oidc/start">{methods.oidcLabel}<ArrowRight size={17}/></a>{methods.local&&<div className="separator"><span>또는 로컬 관리자 계정</span></div>}</>}
      {methods.local&&<form onSubmit={submit} className="form-stack"><label><span>아이디</span><input autoComplete="username" required maxLength={120} value={username} onChange={(e)=>setUsername(e.target.value)} placeholder="admin"/></label><label><span>비밀번호</span><input type="password" autoComplete="current-password" required value={password} onChange={(e)=>setPassword(e.target.value)} placeholder="비밀번호 입력"/></label><button className="button primary wide" disabled={busy}>{busy?'확인 중…':'로그인'}<ArrowRight size={17}/></button></form>}
      <p className="login-help">접속에 문제가 있으면 AgentHub 관리자에게 문의하세요.</p></div><footer className="login-version"><span>{version.name} v{version.version}</span><span>Commit {version.commit.slice(0,8)}</span></footer></section>
  </main>
}
