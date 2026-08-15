import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { Camera, DatabaseBackup, History, Plus, RefreshCw, RotateCcw, Trash2 } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge, SuccessBanner } from '../components/UI'
import type { Workspace, WorkspaceSnapshot } from '../types'

export function Snapshots() {
  const [searchParams] = useSearchParams()
  const [snapshots, setSnapshots] = useState<WorkspaceSnapshot[]>()
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [createFor, setCreateFor] = useState<string | null>(searchParams.get('workspace'))
  const [restore, setRestore] = useState<WorkspaceSnapshot | null>(null)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [removing, setRemoving] = useState<WorkspaceSnapshot | null>(null)
  const [removeBusy, setRemoveBusy] = useState(false)
  const [removeError, setRemoveError] = useState('')
  const workspaceMap = useMemo(() => new Map(workspaces.map((item) => [item.id, item])), [workspaces])

  const load = useCallback(async () => {
    const [snapshotResult, workspaceResult] = await Promise.all([
      api.get<{items?: WorkspaceSnapshot[]}>('/api/v1/workspace-snapshots'),
      api.get<{items?: Workspace[]}>('/api/v1/workspaces')
    ])
    setSnapshots(snapshotResult.items ?? [])
    setWorkspaces(workspaceResult.items ?? [])
  }, [])



  useEffect(() => { void load().catch((e) => { setSnapshots([]); setError(e instanceof Error ? e.message : 'Snapshot 목록을 불러오지 못했습니다.') }) }, [load])
  const remove = async () => {
    if (!removing) return
    setRemoveBusy(true); setRemoveError('')
    try { await api.delete(`/api/v1/workspace-snapshots/${removing.id}`); setRemoving(null); setSuccess('Snapshot 기록을 삭제했습니다.'); await load() }
    catch (e) { setRemoveError(e instanceof Error ? e.message : 'Snapshot을 삭제하지 못했습니다.') }
    finally { setRemoveBusy(false) }
  }
  useEffect(() => {
    if (!snapshots?.some((item) => ['pending', 'provisioning'].includes(item.status))) return
    const timer = window.setInterval(() => void load().catch(() => undefined), 5000)
    return () => window.clearInterval(timer)
  }, [load, snapshots])

  if (!snapshots) return <Loading />
  return <div className="page">
    <PageHeader eyebrow="PERSISTENT STORAGE" title="Workspace Snapshots" description="VolumeSnapshot으로 복원 지점을 만들고 새 Workspace로 안전하게 복원합니다." actions={<><button className="button ghost" onClick={() => void load()}><RefreshCw size={16}/>새로고침</button><button className="button primary" disabled={workspaces.length === 0} onClick={() => setCreateFor(workspaces[0]?.id ?? null)}><Plus size={16}/>Snapshot 생성</button></>} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {success && <SuccessBanner message={success} />}
    {snapshots.length === 0 ? <Empty icon={<DatabaseBackup/>} title="Snapshot이 없습니다" description="실행 중인 Agent를 중지한 뒤 Workspace 복원 지점을 만들어 보세요." action={workspaces.length > 0 ? <button className="button primary" onClick={() => setCreateFor(workspaces[0].id)}>첫 Snapshot 생성</button> : undefined}/> :
      <section className="snapshot-list">
        {snapshots.map((item) => <article className="snapshot-card" key={item.id}>
          <div className="snapshot-icon"><History/></div>
          <div className="snapshot-main"><div><h3>{item.name}</h3><StatusBadge status={item.status}/></div><p>{workspaceMap.get(item.workspaceId)?.name ?? item.workspaceId}</p><code>{item.storageRef}</code></div>
          <dl><div><dt>생성 시각</dt><dd>{new Date(item.createdAt).toLocaleString('ko-KR')}</dd></div><div><dt>크기</dt><dd>{formatBytes(item.sizeBytes)}</dd></div></dl>
          <div className="card-actions"><button className="button ghost" disabled={item.status !== 'ready'} onClick={() => setRestore(item)}><RotateCcw size={15}/>새 Workspace로 복원</button><button className="danger" title="삭제" onClick={() => { setRemoveError(''); setRemoving(item) }}><Trash2 size={15}/></button></div>
        </article>)}
      </section>}
    {createFor && <SnapshotDrawer workspaces={workspaces} initial={createFor} close={() => setCreateFor(null)} done={() => { setCreateFor(null); setSuccess('Snapshot 생성 요청을 접수했습니다.'); void load() }} error={setError}/>}
    {removing && <ConfirmDialog title="Snapshot을 삭제할까요?" message={<><strong>{removing.name}</strong> 복원 지점 기록이 삭제됩니다. 이미 복원해 만든 Workspace는 그대로 유지됩니다.</>} busy={removeBusy} error={removeError} onConfirm={() => void remove()} onCancel={() => setRemoving(null)}/>}
    {restore && <RestoreDrawer snapshot={restore} close={() => setRestore(null)} done={() => { setRestore(null); setSuccess('Snapshot을 새 Workspace로 복원했습니다.'); void load() }} error={setError}/>}
  </div>
}

function SnapshotDrawer({workspaces, initial, close, done, error}:{workspaces:Workspace[];initial:string;close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const [workspaceId, setWorkspaceId] = useState(initial)
  const [name, setName] = useState(`snapshot-${new Date().toISOString().slice(0, 10)}`)
  const [busy, setBusy] = useState(false)
  const submit = async (event:FormEvent) => { event.preventDefault(); setBusy(true); try { await api.post(`/api/v1/workspaces/${workspaceId}/snapshots`, {name}); done() } catch (e) { error(e instanceof Error ? e.message : 'Snapshot을 생성하지 못했습니다.') } finally { setBusy(false) } }
  return <Drawer title="Workspace Snapshot 생성" subtitle="CSI VolumeSnapshotClass가 구성된 Kubernetes Cluster에서 동작합니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="snapshot-form" disabled={busy}>{busy?'요청 중…':'Snapshot 생성'}</button></>}><form id="snapshot-form" className="drawer-form" onSubmit={submit}><label><span>Workspace</span><select required value={workspaceId} onChange={(e) => setWorkspaceId(e.target.value)}>{workspaces.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.sizeGb} GB</option>)}</select></label><label><span>Snapshot 이름</span><input required maxLength={80} value={name} onChange={(e) => setName(e.target.value)}/></label><div className="info-box"><Camera size={17}/><div><strong>일관된 복원 지점</strong><p>파일 변경이 진행 중인 Runtime을 먼저 중지하면 더 일관된 Snapshot을 얻을 수 있습니다.</p></div></div></form></Drawer>
}

function RestoreDrawer({snapshot, close, done, error}:{snapshot:WorkspaceSnapshot;close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const [name, setName] = useState(`${snapshot.name}-restored`)
  const [busy, setBusy] = useState(false)
  const submit = async (event:FormEvent) => { event.preventDefault(); setBusy(true); try { await api.post(`/api/v1/workspace-snapshots/${snapshot.id}/restore`, {name}); done() } catch (e) { error(e instanceof Error ? e.message : '복원하지 못했습니다.') } finally { setBusy(false) } }
  return <Drawer title="Snapshot 복원" subtitle="원본은 유지하고 새 Workspace/PVC를 만듭니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="restore-form" disabled={busy}>{busy?'복원 중…':'새 Workspace로 복원'}</button></>}><form id="restore-form" className="drawer-form" onSubmit={submit}><label><span>복원할 Snapshot</span><input disabled value={snapshot.name}/></label><label><span>새 Workspace 이름</span><input required maxLength={80} value={name} onChange={(e) => setName(e.target.value)}/></label></form></Drawer>
}

function formatBytes(bytes:number) { if (!bytes) return '확인 중'; const units=['B','KB','MB','GB','TB']; const index=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),units.length-1); return `${(bytes/1024**index).toFixed(index>2?1:0)} ${units[index]}` }
