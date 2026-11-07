"use client";

// 信息搜集（Recon）配置页。
//
// 只做一件事：管理 FOFA key 池并同步到 MCP。之所以单独一页而不是塞进 MCP 页，
// 是因为用户心智里「我有一个/几个 FOFA key」和「我挂了一个 MCP 服务」不是同一件事：
// 前者需要看每个 key 的可用性、调整池子顺序；后者是通用服务配置。
//
// 明文 key 永不出服务端：读取只返回脱敏值，编辑时留空 = 保持原 key。
// 这条约定让「删掉池里第二个 key」不需要前端知道任何明文。

import * as React from "react";

import { KeyRoundIcon, Loader2Icon, PlusIcon, RefreshCwIcon, SaveIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";

// 一行 key：已有 key 只显示 masked，value 为空表示不修改。
type KeyRow = { masked: string; value: string };

export default function ReconPage() {
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [enabled, setEnabled] = React.useState(false);
  const [keys, setKeys] = React.useState<KeyRow[]>([]);
  const [maxAttempts, setMaxAttempts] = React.useState("3");
  const [accountCool, setAccountCool] = React.useState("1800");
  const [networkCool, setNetworkCool] = React.useState("30");
  const [toolCached, setToolCached] = React.useState(0);

  const apply = React.useCallback((d: Awaited<ReturnType<typeof api.fofaRecon>>) => {
    setEnabled(!!d.enabled);
    setKeys(
      (d.keys ?? []).map((k) => ({ masked: k.masked, value: "" })),
    );
    setMaxAttempts(String(d.max_attempts || 3));
    setAccountCool(String(d.account_cooldown || 1800));
    setNetworkCool(String(d.network_cooldown || 30));
    setToolCached(d.tool_cached || 0);
  }, []);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      apply(await api.fofaRecon());
    } catch (e) {
      toast.error(`加载失败：${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, [apply]);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    // 只把「有值的新 key」和「已有 key 的占位」一起提交；占位 value 为空
    // 表示沿用服务端原值，服务端按位置合并。
    const payload = keys
      .map((k) => ({ masked: k.masked, value: k.value.trim() }))
      .filter((k) => k.value !== "" || k.masked !== "");
    if (payload.length === 0) {
      toast.error("至少填写一个 FOFA key");
      return;
    }
    setSaving(true);
    try {
      const r = await api.saveFofaRecon({
        enabled,
        keys: payload,
        max_attempts: Number(maxAttempts) || 3,
        account_cooldown: Number(accountCool) || 1800,
        network_cooldown: Number(networkCool) || 30,
      });
      toast.success(`已保存：${r.key_count} 个 key${r.tool_cached ? `，发现 ${r.tool_cached} 个工具` : ""}`);
      await load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex flex-col gap-6 p-6">
      <div>
        <h1 className="font-semibold text-2xl">信息搜集</h1>
        <p className="text-muted-foreground text-sm">
          配置外部资产测绘数据源。当前支持 FOFA：多个 key 组成池，失败自动轮询下一个。
        </p>
      </div>

      <Card>
        <CardHeader className="flex-row items-start justify-between gap-4">
          <div className="grid gap-1">
            <CardTitle className="flex items-center gap-2">
              <KeyRoundIcon className="size-4" />
              FOFA Key 池
              {toolCached > 0 && <Badge variant="secondary">已接入 {toolCached} 个工具</Badge>}
            </CardTitle>
            <CardDescription>
              按顺序轮询；连续失败的 key 进入冷却，冷却期内跳过。账号类错误（余额/权限/过期）冷却较久，网络类错误指数退避。
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="recon-enabled" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="recon-enabled" className="text-sm">
              {enabled ? "已启用" : "已停用"}
            </Label>
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          {loading ? (
            <div className="text-muted-foreground flex items-center gap-2 text-sm">
              <Loader2Icon className="size-4 animate-spin" />
              加载中…
            </div>
          ) : (
            <>
              <div className="grid gap-2">
                <Label>Key 列表</Label>
                {keys.length === 0 && (
                  <p className="text-muted-foreground text-sm">尚未配置，添加第一个 key。</p>
                )}
                {keys.map((k, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <span className="text-muted-foreground w-6 shrink-0 text-right text-xs tabular-nums">{i + 1}</span>
                    <Input
                      className="font-mono"
                      placeholder={k.masked || "粘贴 FOFA key"}
                      value={k.value}
                      onChange={(e) =>
                        setKeys((prev) => prev.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
                      }
                    />
                    {k.masked && !k.value && (
                      <span className="text-muted-foreground shrink-0 font-mono text-xs">{k.masked}</span>
                    )}
                    <Button
                      variant="ghost"
                      size="icon"
                      title="移除"
                      onClick={() => setKeys((prev) => prev.filter((_, j) => j !== i))}
                    >
                      <Trash2Icon className="size-4" />
                    </Button>
                  </div>
                ))}
                <div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setKeys((prev) => [...prev, { masked: "", value: "" }])}
                  >
                    <PlusIcon className="size-4" />
                    添加 key
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  已有 key 只显示脱敏值；留空表示保持不变。留空新增行会被忽略。
                </p>
              </div>

              <Separator />

              <div className="grid gap-4 sm:grid-cols-3">
                <div className="grid gap-1.5">
                  <Label htmlFor="max-attempts">单次最多尝试 key 数</Label>
                  <Input
                    id="max-attempts"
                    type="number"
                    min={1}
                    value={maxAttempts}
                    onChange={(e) => setMaxAttempts(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">一次检索最多切换几个 key，超过则报错。</p>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="account-cool">账号错误冷却（秒）</Label>
                  <Input
                    id="account-cool"
                    type="number"
                    min={60}
                    value={accountCool}
                    onChange={(e) => setAccountCool(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">余额不足/权限/过期这类错误的重试无意义，冷却较久。</p>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="network-cool">网络错误基础冷却（秒）</Label>
                  <Input
                    id="network-cool"
                    type="number"
                    min={5}
                    value={networkCool}
                    onChange={(e) => setNetworkCool(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">超时/连接重置按指数退避，逐次翻倍，封顶 8 倍。</p>
                </div>
              </div>

              <div className="flex items-center gap-2">
                <Button onClick={save} disabled={saving}>
                  {saving ? <Loader2Icon className="size-4 animate-spin" /> : <SaveIcon className="size-4" />}
                  保存并同步到 MCP
                </Button>
                <Button variant="outline" onClick={() => void load()} disabled={saving}>
                  <RefreshCwIcon className="size-4" />
                  重新加载
                </Button>
              </div>
            </>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Agent 如何使用</CardTitle>
        </CardHeader>
        <CardContent className="text-muted-foreground grid gap-2 text-sm">
          <p>
            保存后 FOFA 作为一个 MCP 服务接入，agent 可见两个工具：
            <code className="bg-muted mx-1 rounded px-1 py-0.5 text-xs">mcp__fofa__fofa_search</code>
            用于检索，
            <code className="bg-muted mx-1 rounded px-1 py-0.5 text-xs">mcp__fofa__fofa_key_status</code>
            用于查看 key 池健康度。
          </p>
          <p>
            还需在「系统 → MCP」把这两个工具绑定给 planner / worker，agent 才会真正拿到；也可在「系统 → Agent」里按角色调整。
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
