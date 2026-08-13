import type { ReactNode } from 'react'
import { AlertCircle, CheckCircle2, X } from 'lucide-react'

export function PageHeader({eyebrow,title,description,actions}:{eyebrow?:string;title:string;description:string;actions?:ReactNode}) { return <div className="page-header"><div>{eyebrow&&<span className="eyebrow">{eyebrow}</span>}<h1>{title}</h1><p>{description}</p></div>{actions&&<div className="page-actions">{actions}</div>}</div> }
export function StatusBadge({status}:{status:string}) { const normal=status.toLowerCase().replaceAll('_','-');return <span className={`status-badge status-${normal}`}><span/>{status}</span> }
export function Empty({icon,title,description,action}:{icon:ReactNode;title:string;description:string;action?:ReactNode}) { return <div className="empty-state"><div className="empty-icon">{icon}</div><h3>{title}</h3><p>{description}</p>{action}</div> }
export function Loading() { return <div className="loading-grid"><span/><span/><span/></div> }
export function ErrorBanner({message,onClose}:{message:string;onClose?:()=>void}) { return <div className="alert error"><AlertCircle size={18}/><span>{message}</span>{onClose&&<button onClick={onClose} aria-label="닫기"><X size={16}/></button>}</div> }
export function SuccessBanner({message}:{message:string}) { return <div className="alert success"><CheckCircle2 size={18}/><span>{message}</span></div> }
export function Drawer({title,subtitle,close,children,footer}:{title:string;subtitle?:string;close:()=>void;children:ReactNode;footer?:ReactNode}) { return <div className="drawer-layer"><button className="drawer-scrim" onClick={close} aria-label="닫기"/><aside className="drawer" role="dialog" aria-modal="true" aria-label={title}><header><div><h2>{title}</h2>{subtitle&&<p>{subtitle}</p>}</div><button className="icon-button" onClick={close} aria-label="닫기"><X size={20}/></button></header><div className="drawer-body custom-scroll">{children}</div>{footer&&<footer>{footer}</footer>}</aside></div> }
