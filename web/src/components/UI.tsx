import type { ReactNode } from 'react'
import { AlertCircle, CheckCircle2, X } from 'lucide-react'

export function PageHeader({eyebrow,title,description,actions}:{eyebrow?:string;title:string;description:string;actions?:ReactNode}) { return <div className="page-header"><div>{eyebrow&&<span className="eyebrow">{eyebrow}</span>}<h1>{title}</h1><p>{description}</p></div>{actions&&<div className="page-actions">{actions}</div>}</div> }
// Kubernetes and the runtime adapters report state in English; the class name
// keeps the raw value so the palette stays keyed off it, only the label is
// translated. Unknown values fall through to the original text rather than
// being hidden behind a generic placeholder.
const STATUS_LABELS: Record<string, string> = {
  running: '실행 중', ready: '준비됨', active: '활성', online: '정상', success: '성공', passed: '통과',
  starting: '시작 중', spawning: '생성 중', provisioning: '준비 중', pending: '대기', stopping: '중지 중',
  idle: '유휴', stopped: '중지됨', disabled: '비활성', unsupported: '미지원',
  failed: '실패', crashed: '비정상 종료', error: '오류', rejected: '거부됨', approved: '승인됨',
  succeeded: '성공', skipped: '건너뜀', validating: '검증 중',
}
export function statusLabel(status: string) { return STATUS_LABELS[status.toLowerCase()] ?? status }
export function StatusBadge({status}:{status:string}) { const normal=status.toLowerCase().replaceAll('_','-');return <span className={`status-badge status-${normal}`} title={status}><span/>{statusLabel(status)}</span> }
export function Empty({icon,title,description,action}:{icon:ReactNode;title:string;description:string;action?:ReactNode}) { return <div className="empty-state"><div className="empty-icon">{icon}</div><h3>{title}</h3><p>{description}</p>{action}</div> }
export function Loading() { return <div className="loading-grid"><span/><span/><span/></div> }
export function ErrorBanner({message,onClose}:{message:string;onClose?:()=>void}) { return <div className="alert error"><AlertCircle size={18}/><span>{message}</span>{onClose&&<button onClick={onClose} aria-label="닫기"><X size={16}/></button>}</div> }
export function SuccessBanner({message}:{message:string}) { return <div className="alert success"><CheckCircle2 size={18}/><span>{message}</span></div> }
export function Drawer({title,subtitle,close,children,footer}:{title:string;subtitle?:string;close:()=>void;children:ReactNode;footer?:ReactNode}) { return <div className="drawer-layer"><button className="drawer-scrim" onClick={close} aria-label="닫기"/><aside className="drawer" role="dialog" aria-modal="true" aria-label={title}><header><div><h2>{title}</h2>{subtitle&&<p>{subtitle}</p>}</div><button className="icon-button" onClick={close} aria-label="닫기"><X size={20}/></button></header><div className="drawer-body custom-scroll">{children}</div>{footer&&<footer>{footer}</footer>}</aside></div> }

/**
 * Blocking confirmation for destructive actions. Deletes here remove Kubernetes
 * resources and platform records, so every call site routes through this rather
 * than firing on a single click.
 */
export function ConfirmDialog({title,message,confirmLabel='삭제',busy=false,error,onConfirm,onCancel}:{title:string;message:ReactNode;confirmLabel?:string;busy?:boolean;error?:string;onConfirm:()=>void;onCancel:()=>void}) {
  return <div className="drawer-layer">
    <button className="drawer-scrim" onClick={onCancel} aria-label="취소"/>
    <div className="confirm-dialog" role="alertdialog" aria-modal="true" aria-label={title}>
      <div className="confirm-icon"><AlertCircle size={22}/></div>
      <h3>{title}</h3>
      <div className="confirm-body">{message}</div>
      {error&&<ErrorBanner message={error}/>}
      <div className="confirm-actions">
        <button className="button ghost" onClick={onCancel} disabled={busy}>취소</button>
        <button className="button danger" onClick={onConfirm} disabled={busy} autoFocus>{busy?'처리 중…':confirmLabel}</button>
      </div>
    </div>
  </div>
}
