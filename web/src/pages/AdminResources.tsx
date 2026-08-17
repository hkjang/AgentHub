import { FormEvent, useCallback, useEffect, useState } from "react";
import { Boxes, Bot, Network, Pencil, Plus, Sparkles, Trash2 } from "lucide-react";
import { api } from "../api";
import {
  ConfirmDialog,
  Drawer,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  StatusBadge,
} from "../components/UI";
import { RUNTIME_TYPES, runtimeLabel } from "../runtime";

type Kind = "profiles" | "images" | "models" | "mcp" | "bundles";
type Item = Record<string, unknown> & {
  id: string;
  name: string;
  enabled?: boolean;
  approved?: boolean;
};
const meta: Record<
  Kind,
  { title: string; description: string; endpoint: string; icon: typeof Bot }
> = {
  profiles: {
    title: "런타임 프로파일",
    description:
      "사용자가 선택할 수 있는 CPU, Memory, Storage 및 Idle 정책입니다.",
    endpoint: "runtime-profiles",
    icon: Bot,
  },
  images: {
    title: "런타임 이미지",
    description:
      "승인된 OpenCode, Hermes 및 Custom Runtime 이미지 카탈로그입니다.",
    endpoint: "runtime-images",
    icon: Boxes,
  },
  models: {
    title: "모델 엔드포인트",
    description: "사내 vLLM, Ollama와 OpenAI-compatible 모델 연결입니다.",
    endpoint: "models",
    icon: Sparkles,
  },
  mcp: {
    title: "MCP 서버",
    description: "Shared, Dedicated, Sidecar MCP의 실행 및 승인 정책입니다.",
    endpoint: "mcp-servers",
    icon: Network,
  },
  bundles: {
    title: "MCP 번들",
    description: "용도별 MCP 조합을 만들어 Agent 생성 단계를 간소화합니다.",
    endpoint: "mcp-bundles",
    icon: Boxes,
  },
};

export function AdminResources({ kind }: { kind: Kind }) {
  const config = meta[kind],
    [items, setItems] = useState<Item[]>(),
    [selected, setSelected] = useState<Item | null>(),
    [error, setError] = useState("");
  const [removing, setRemoving] = useState<Item | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [removeError, setRemoveError] = useState("");
  const load = useCallback(() =>
    api
      .get<{ items?: Item[] }>(`/api/v1/admin/${config.endpoint}`)
      .then((v) => setItems(v.items ?? []))
      .catch((e) => { setItems([]); setError(e instanceof Error ? e.message : "목록을 불러오지 못했습니다."); }), [config.endpoint]);
  useEffect(() => {
    setItems(undefined);
    void load();
  }, [load]);
  const remove = async () => {
    if (!removing) return;
    setRemoveBusy(true);
    setRemoveError("");
    try {
      await api.delete(`/api/v1/admin/${config.endpoint}/${removing.id}`);
      setRemoving(null);
      await load();
    } catch (e) {
      setRemoveError(e instanceof Error ? e.message : "삭제하지 못했습니다.");
    } finally {
      setRemoveBusy(false);
    }
  };
  if (!items) return <Loading />;
  const Icon = config.icon;
  return (
    <div className="page">
      <PageHeader
        eyebrow="관리자"
        title={config.title}
        description={config.description}
        actions={
          <button className="button primary" onClick={() => setSelected(null)}>
            <Plus size={16} />새 항목
          </button>
        }
      />
      {error && <ErrorBanner message={error} onClose={() => setError("")} />}{" "}
      {items.length === 0 ? (
        <Empty
          icon={<Icon />}
          title="등록된 항목이 없습니다"
          description="운영 표준에 맞는 첫 항목을 등록하세요."
          action={
            <button
              className="button primary"
              onClick={() => setSelected(null)}
            >
              새 항목
            </button>
          }
        />
      ) : (
        <section className="resource-grid">
          {items.map((item) => (
            <article className="resource-card" key={item.id}>
              <div>
                <div className="list-icon">
                  <Icon />
                </div>
                <StatusBadge
                  status={
                    (item.enabled ?? item.approved) ? "active" : "disabled"
                  }
                />
              </div>
              <h3>{item.name}</h3>
              <p>{summary(kind, item)}</p>
              <dl>
                {facts(kind, item).map(([label, value]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>{String(value ?? "—")}</dd>
                  </div>
                ))}
              </dl>
              <div className="card-actions">
                <button title="수정" onClick={() => setSelected(item)}>
                  <Pencil size={15} />수정
                </button>
                <button
                  className="danger"
                  title="삭제"
                  onClick={() => {
                    setRemoveError("");
                    setRemoving(item);
                  }}
                >
                  <Trash2 size={15} />
                </button>
              </div>
            </article>
          ))}
        </section>
      )}{" "}
      {removing && (
        <ConfirmDialog
          title={`${config.title} 항목을 삭제할까요?`}
          message={
            <>
              <strong>{removing.name}</strong> 항목이 삭제됩니다. 이 리소스를
              사용 중인 Agent가 있으면 삭제가 거부됩니다.
            </>
          }
          busy={removeBusy}
          error={removeError}
          onConfirm={() => void remove()}
          onCancel={() => setRemoving(null)}
        />
      )}
      {selected !== undefined && (
        <ResourceDrawer
          kind={kind}
          item={selected}
          close={() => setSelected(undefined)}
          done={() => {
            setSelected(undefined);
            void load();
          }}
          error={setError}
        />
      )}
    </div>
  );
}

function summary(kind: Kind, item: Item) {
  if (kind === "profiles")
    return String(item.description || "표준 Runtime 자원");
  if (kind === "images") return String(item.image || "");
  if (kind === "models") return String(item.baseUrl || "");
  return String(item.description || item.endpoint || "MCP 구성");
}
function facts(kind: Kind, item: Item): [string, unknown][] {
  if (kind === "profiles")
    return [
      ["CPU", `${Number(item.cpuMillis) / 1000} Core`],
      ["Memory", `${Number(item.memoryMb) / 1024} GB`],
      ["Storage", `${item.storageGb} GB`],
    ];
  if (kind === "images")
    return [
      ["Runtime", runtimeLabel(String(item.runtimeType ?? ""))],
      ["Version", item.version],
      ["Digest", item.digest || "미등록"],
    ];
  if (kind === "models")
    return [
      ["Provider", item.provider],
      ["Model", item.defaultModel],
      ["Secret", item.secretConfigured ? "설정됨" : "없음"],
    ];
  if (kind === "bundles")
    return [
      ["Servers", ((item.serverIds as string[]) || []).length],
      ["Status", item.enabled ? "Enabled" : "Disabled"],
      ["Type", "Bundle"],
    ];
  const authType = String(item.authType ?? "none");
  const auth =
    authType === "none"
      ? "없음"
      : item.perUserCredential
        ? `${authType} · 사용자별`
        : item.credentialConfigured
          ? `${authType} · 등록됨`
          : `${authType} · 미등록`;
  return [
    ["Mode", item.mode],
    ["Risk", item.riskLevel],
    ["인증", auth],
  ];
}

function ResourceDrawer({
  kind,
  item,
  close,
  done,
  error,
}: {
  kind: Kind;
  item: Item | null;
  close: () => void;
  done: () => void;
  error: (v: string) => void;
}) {
  const defaults =
    kind === "profiles"
      ? {
          name: "",
          description: "",
          cpuMillis: 2000,
          memoryMb: 4096,
          storageGb: 10,
          gpuCount: 0,
          idleTimeoutSeconds: 3600,
          enabled: true,
        }
      : kind === "images"
        ? {
            name: "",
            runtimeType: "opencode",
            image: "",
            version: "",
            digest: "",
            sbomUri: "",
            approved: false,
            deprecated: false,
          }
        : kind === "models"
          ? {
              name: "",
              provider: "openai-compatible",
              baseUrl: "",
              defaultModel: "",
              secret: "",
              enabled: true,
            }
          : kind === "bundles"
            ? { name: "", description: "", serverIds: [], enabled: true }
            : {
                name: "",
                description: "",
                mode: "shared",
                transport: "streamable-http",
                endpoint: "",
                image: "",
                port: 8000,
                riskLevel: "low",
                approvalRequired: false,
                enabled: true,
                authType: "none",
                authHeader: "",
                perUserCredential: false,
              };
  // Kept out of `form`: the credential is write-only and must never be part of a
  // resource body that a listing echoes back.
  const [credential, setCredential] = useState("");
  const [form, setForm] = useState<Record<string, unknown>>({
      ...defaults,
      ...item,
    }),
    [busy, setBusy] = useState(false),
    [servers, setServers] = useState<Item[]>([]);
  useEffect(() => {
    if (kind === "bundles")
      void api
        .get<{ items: Item[] }>("/api/v1/admin/mcp-servers")
        .then((v) => setServers(v.items));
  }, [kind]);
  const field = (name: string) => (form[name] ?? "") as string | number;
  const update = (name: string, value: unknown) =>
    setForm((v) => ({ ...v, [name]: value }));
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const saved = await api.post<Item>(`/api/v1/admin/${meta[kind].endpoint}`, form);
      if (kind === "mcp" && credential.trim() && !form.perUserCredential) {
        await api.put(`/api/v1/admin/mcp-servers/${saved.id}/credential`, {
          value: credential,
        });
      }
      done();
    } catch (e) {
      error(e instanceof Error ? e.message : "저장하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Drawer
      title={`${item ? "수정" : "새로 등록"} · ${meta[kind].title}`}
      close={close}
      footer={
        <>
          <button className="button ghost" onClick={close}>
            취소
          </button>
          <button
            className="button primary"
            form="resource-form"
            disabled={busy}
          >
            {busy ? "저장 중…" : "저장"}
          </button>
        </>
      }
    >
      <form id="resource-form" className="drawer-form" onSubmit={submit}>
        <label>
          <span>
            이름 <b>*</b>
          </span>
          <input
            required
            value={field("name")}
            onChange={(e) => update("name", e.target.value)}
          />
        </label>
        {kind === "profiles" && (
          <>
            <label>
              <span>설명</span>
              <textarea
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
              />
            </label>
            <div className="form-grid">
              <NumberInput
                label="CPU (millicores)"
                name="cpuMillis"
                form={form}
                update={update}
              />
              <NumberInput
                label="Memory (MB)"
                name="memoryMb"
                form={form}
                update={update}
              />
              <NumberInput
                label="Storage (GB)"
                name="storageGb"
                form={form}
                update={update}
              />
              <NumberInput
                label="GPU"
                name="gpuCount"
                form={form}
                update={update}
              />
            </div>
            <NumberInput
              label="Idle Timeout (초)"
              name="idleTimeoutSeconds"
              form={form}
              update={update}
            />
            <Check
              label="사용자 선택 허용"
              name="enabled"
              form={form}
              update={update}
            />
          </>
        )}
        {kind === "mcp" && (
          <>
            <label>
              <span>인증 방식</span>
              <select
                value={String(field("authType") || "none")}
                onChange={(e) => update("authType", e.target.value)}
              >
                <option value="none">없음 (공개 MCP)</option>
                <option value="bearer">Bearer 토큰</option>
                <option value="header">커스텀 헤더</option>
                <option value="basic">Basic 인증</option>
              </select>
            </label>
            {String(field("authType") || "none") === "header" && (
              <label>
                <span>
                  헤더 이름 <b>*</b>
                </span>
                <input
                  required
                  value={String(field("authHeader") || "")}
                  onChange={(e) => update("authHeader", e.target.value)}
                  placeholder="X-Api-Key"
                />
              </label>
            )}
            {String(field("authType") || "none") !== "none" && (
              <>
                <Check
                  label="사용자별 자격증명 사용"
                  name="perUserCredential"
                  form={form}
                  update={update}
                />
                <label>
                  <span>공용 자격증명</span>
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={credential}
                    disabled={Boolean(form.perUserCredential)}
                    onChange={(e) => setCredential(e.target.value)}
                    placeholder={
                      form.perUserCredential
                        ? "사용자별 자격증명 모드에서는 각 사용자가 직접 등록합니다"
                        : item?.credentialConfigured
                          ? "등록됨 · 변경하려면 새 값을 입력하세요"
                          : "미등록"
                    }
                  />
                  <small>
                    값은 암호화되어 저장되며 Runtime에는 Secret으로만 전달됩니다.
                  </small>
                </label>
              </>
            )}
          </>
        )}
        {kind === "images" && (
          <>
            <label>
              <span>Runtime</span>
              <select
                value={field("runtimeType")}
                onChange={(e) => update("runtimeType", e.target.value)}
              >
                {RUNTIME_TYPES.map((value) => (
                  <option key={value} value={value}>
                    {runtimeLabel(value)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              <span>
                Image <b>*</b>
              </span>
              <input
                required
                value={field("image")}
                onChange={(e) => update("image", e.target.value)}
                placeholder="registry.local/agent/opencode"
              />
            </label>
            <label>
              <span>
                Version <b>*</b>
              </span>
              <input
                required
                value={field("version")}
                onChange={(e) => update("version", e.target.value)}
              />
            </label>
            <label>
              <span>Digest</span>
              <input
                value={field("digest")}
                onChange={(e) => update("digest", e.target.value)}
                placeholder="sha256:…"
              />
            </label>
            <label>
              <span>SBOM URI</span>
              <input
                value={field("sbomUri")}
                onChange={(e) => update("sbomUri", e.target.value)}
              />
            </label>
            <Check
              label="운영 승인"
              name="approved"
              form={form}
              update={update}
            />
            <Check
              label="Deprecated"
              name="deprecated"
              form={form}
              update={update}
            />
          </>
        )}
        {kind === "models" && (
          <>
            <label>
              <span>Provider</span>
              <input
                required
                value={field("provider")}
                onChange={(e) => update("provider", e.target.value)}
              />
            </label>
            <label>
              <span>
                Base URL <b>*</b>
              </span>
              <input
                required
                type="url"
                value={field("baseUrl")}
                onChange={(e) => update("baseUrl", e.target.value)}
                placeholder="https://vllm.local/v1"
              />
            </label>
            <label>
              <span>
                Default Model <b>*</b>
              </span>
              <input
                required
                value={field("defaultModel")}
                onChange={(e) => update("defaultModel", e.target.value)}
              />
            </label>
            <label>
              <span>API Key</span>
              <input
                type="password"
                value={field("secret")}
                onChange={(e) => update("secret", e.target.value)}
                placeholder={
                  item?.secretConfigured
                    ? "•••••••• · 변경할 때만 입력"
                    : "API Key"
                }
              />
            </label>
            <Check label="사용" name="enabled" form={form} update={update} />
          </>
        )}
        {kind === "mcp" && (
          <>
            <label>
              <span>설명</span>
              <textarea
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
              />
            </label>
            <label>
              <span>Runtime Mode</span>
              <select
                value={field("mode")}
                onChange={(e) => update("mode", e.target.value)}
              >
                <option value="shared">Shared</option>
                <option value="dedicated">Dedicated</option>
                <option value="sidecar">Sidecar</option>
              </select>
            </label>
            {form.mode === "shared" ? (
              <label>
                <span>
                  Endpoint <b>*</b>
                </span>
                <input
                  required
                  type="url"
                  value={field("endpoint")}
                  onChange={(e) => update("endpoint", e.target.value)}
                />
              </label>
            ) : (
              <label>
                <span>
                  Image <b>*</b>
                </span>
                <input
                  required
                  value={field("image")}
                  onChange={(e) => update("image", e.target.value)}
                />
              </label>
            )}
            {form.mode !== "shared" && (
              <NumberInput
                label="Container Port"
                name="port"
                form={form}
                update={update}
              />
            )}
            <label>
              <span>Risk</span>
              <select
                value={field("riskLevel")}
                onChange={(e) => update("riskLevel", e.target.value)}
              >
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
                <option value="critical">Critical</option>
              </select>
            </label>
            <Check
              label="호출 전 승인"
              name="approvalRequired"
              form={form}
              update={update}
            />
            <Check label="사용" name="enabled" form={form} update={update} />
          </>
        )}
        {kind === "bundles" && (
          <>
            <label>
              <span>설명</span>
              <textarea
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
              />
            </label>
            <fieldset>
              <legend>MCP Servers</legend>
              <div className="bundle-choices">
                {servers.map((server) => {
                  const ids = form.serverIds as string[];
                  return (
                    <label key={server.id}>
                      <input
                        type="checkbox"
                        checked={ids.includes(server.id)}
                        onChange={(e) =>
                          update(
                            "serverIds",
                            e.target.checked
                              ? [...ids, server.id]
                              : ids.filter((id) => id !== server.id),
                          )
                        }
                      />
                      <span>
                        <strong>{server.name}</strong>
                        <small>
                          {String(server.mode)} · {String(server.riskLevel)}
                        </small>
                      </span>
                    </label>
                  );
                })}
              </div>
            </fieldset>
            <Check
              label="사용자 선택 허용"
              name="enabled"
              form={form}
              update={update}
            />
          </>
        )}
      </form>
    </Drawer>
  );
}
function NumberInput({
  label,
  name,
  form,
  update,
}: {
  label: string;
  name: string;
  form: Record<string, unknown>;
  update: (n: string, v: unknown) => void;
}) {
  return (
    <label>
      <span>{label}</span>
      <input
        type="number"
        min="0"
        value={Number(form[name] ?? 0)}
        onChange={(e) => update(name, Number(e.target.value))}
      />
    </label>
  );
}
function Check({
  label,
  name,
  form,
  update,
}: {
  label: string;
  name: string;
  form: Record<string, unknown>;
  update: (n: string, v: unknown) => void;
}) {
  return (
    <label className="toggle-row">
      <span>{label}</span>
      <input
        type="checkbox"
        checked={Boolean(form[name])}
        onChange={(e) => update(name, e.target.checked)}
      />
      <i />
    </label>
  );
}
