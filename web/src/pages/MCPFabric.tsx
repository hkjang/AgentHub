import { useEffect, useMemo, useState } from 'react'
import { Boxes, Network, ShieldAlert } from 'lucide-react'
import { api } from '../api'
import { Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import type { MCPBundle, MCPServer } from '../types'

export function MCPFabric({view}:{view:'catalog'|'bundles'}) {
  const [servers, setServers] = useState<MCPServer[]>()
  const [bundles, setBundles] = useState<MCPBundle[]>()
  const [error, setError] = useState('')
  useEffect(() => { Promise.all([
    api.get<{items:MCPServer[]}>('/api/v1/mcp-servers').then((v) => setServers(v.items)),
    api.get<{items:MCPBundle[]}>('/api/v1/mcp-bundles').then((v) => setBundles(v.items))
  ]).catch((e) => setError(e.message)) }, [])
  const serverMap = useMemo(() => new Map((servers ?? []).map((item) => [item.id,item])), [servers])
  if (!servers || !bundles) return <Loading/>
  const catalog = view === 'catalog'
  return <div className="page">
    <PageHeader eyebrow="MCP FABRIC" title={catalog?'MCP Catalog':'MCP Bundles'} description={catalog?'Agent Runtime에 연결할 수 있는 활성 MCP와 실행 격리 방식을 확인합니다.':'업무 목적별 MCP 조합을 Agent Builder에서 한 번에 선택합니다.'}/>
    {error&&<ErrorBanner message={error}/>}
    {catalog ? (servers.length === 0 ? <Empty icon={<Network/>} title="사용 가능한 MCP가 없습니다" description="관리자가 MCP Server를 활성화하면 이곳에 표시됩니다."/> : <section className="mcp-grid">{servers.map((item) => <article className="mcp-card" key={item.id}><header><div className="list-icon"><Network/></div><StatusBadge status={item.mode}/></header><h3>{item.name}</h3><p>{item.description || 'MCP 도구 연결'}</p><dl><div><dt>Transport</dt><dd>{item.transport}</dd></div><div><dt>Risk</dt><dd>{item.riskLevel}</dd></div><div><dt>Endpoint</dt><dd>{item.mode === 'shared' ? safeHost(item.endpoint) : `Pod :${item.port}`}</dd></div></dl>{item.approvalRequired&&<footer><ShieldAlert size={15}/>도구 실행 전 승인 필요</footer>}</article>)}</section>) :
      (bundles.length === 0 ? <Empty icon={<Boxes/>} title="사용 가능한 Bundle이 없습니다" description="관리자가 업무별 MCP Bundle을 활성화하면 이곳에 표시됩니다."/> : <section className="bundle-catalog">{bundles.map((bundle) => <article key={bundle.id}><div className="bundle-symbol"><Boxes/></div><div><h3>{bundle.name}</h3><p>{bundle.description || '표준 MCP Bundle'}</p><div className="server-tags">{bundle.serverIds.map((id) => <span key={id}>{serverMap.get(id)?.name ?? '비활성 MCP'}</span>)}</div></div><strong>{bundle.serverIds.length}<small>MCP</small></strong></article>)}</section>)}
  </div>
}

function safeHost(value:string) { try { return new URL(value).host } catch { return value || '—' } }
