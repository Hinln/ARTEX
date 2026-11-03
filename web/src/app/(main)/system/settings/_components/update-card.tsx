"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { api, openSSE } from "@/lib/api";
import type { UpdateCheck, UpdateProgress, UpdateSettings } from "@/lib/types";

/** 等待新版本上线的最长时间。一次升级要经过三次进程启动（暂存 → 换装 → 新版），
 *  每次都是秒级，三分钟足够覆盖慢磁盘和 Docker 容器重建。 */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // 与 progress 分开：暂存完成后进程就没了，SSE 会断，此时要切到轮询 /api/version。
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  const [network, setNetwork] = React.useState<UpdateSettings | null>(null);
  const [savedNetwork, setSavedNetwork] = React.useState<UpdateSettings | null>(null);
  const [savingNetwork, setSavingNetwork] = React.useState(false);
  const [networkError, setNetworkError] = React.useState("");
  const streamRef = React.useRef<ReturnType<typeof openSSE> | null>(null);
  const networkDirty = network?.proxy !== savedNetwork?.proxy;

  React.useEffect(() => {
    let active = true;
    api
      .getUpdateSettings()
      .then((settings) => {
        if (!active) return;
        setNetwork(settings);
        setSavedNetwork(settings);
      })
      .catch((e) => {
        if (active) setNetworkError("读取更新网络设置失败：" + (e as Error).message);
      });
    return () => {
      active = false;
    };
  }, []);

  React.useEffect(() => () => streamRef.current?.close(), []);

  // quiet 同时决定要不要绕过后端缓存：进页面时的自动检查用缓存（顶栏刚查过），
  // 用户手动点「检查更新」则强制回源，否则刚发布的版本要等缓存过期才看得到。
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("检查更新失败：" + r.error);
          else if (r.has_update) toast.success(`发现新版本 ${r.latest}`);
          else if (r.comparable) toast.success("当前已是最新版本");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("检查更新失败：" + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  const saveNetwork = async () => {
    if (!network) return;
    setSavingNetwork(true);
    setNetworkError("");
    try {
      const settings = await api.saveUpdateSettings({
        proxy: network.proxy,
      });
      setNetwork(settings);
      setSavedNetwork(settings);
      toast.success("更新网络设置已保存");
      check();
    } catch (e) {
      setNetworkError((e as Error).message);
    } finally {
      setSavingNetwork(false);
    }
  };

  // 携带管理员 JWT 轮询 /api/version 直到版本号变化。
  //
  // 判据必须是"版本变了"而不是"能连上了"：换装过程中旧版本会短暂地重新起来一次
  // （那一次只负责把 artex.new 换上去然后立刻退出），只看连通性会误判成功。
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const { version } = await api.version();
        if (version && version !== fromVersion) {
          toast.success(`已更新到 ${version}，正在重新加载页面`);
          await sleep(800);
          window.location.reload();
          return;
        }
      } catch {
        // 重启窗口内连不上是预期的，继续轮询。
      }
    }
    setRestarting(false);
    setBusy(false);
    toast.error("等待服务重启超时。请检查后端日志，或确认 artex 是通过 start.sh / start.bat 启动的。");
  }, []);

  // 订阅更新进度。SSE 不走 Next 的 /api 重写（那层会缓冲，事件推不出来）。
  const openStream = React.useCallback(
    (fromVersion: string) => {
      streamRef.current?.close();
      const es = openSSE("/api/update/stream");
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("更新失败：" + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      streamRef.current = es;
      es.onerror = () => {
        // 进程退出时 SSE 必然断开。如果已经进入等待重启，这属于正常现象，
        // 交给 /api/version 轮询继续判定即可。
        // EventSource 自动重连，恢复服务端保存的当前进度。
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `确定更新到 ${info.latest}？\n\n` +
        "更新会重启程序，正在运行的任务会被中断。\n" +
        (info.mode === "docker"
          ? "\n注意：容器内更新只替换程序本身，不会更新镜像里的 playwright / nmap 等工具链；" +
            "若新版本依赖新工具，请改用 docker compose pull。"
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "准备中…" });
    api
      .applyUpdate()
      .then(() => {
        openStream(from);
      })
      .catch((e) => {
        setBusy(false);
        setProgress(null);
        toast.error("启动更新失败：" + (e as Error).message);
      });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "回滚到上一版本？\n\n程序会重启，正在运行的任务会被中断。\n注意：数据库结构不会回退，旧版本可能无法识别新版写入的数据。",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("已切换到上一版本，正在重启…");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("回滚失败：" + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // 只有下载阶段拿得到真实百分比（按 Content-Length 算）。校验/解压/等待重启都是
  // 时长不可知的阶段，进度条填满并加个脉冲动画表示"在忙但说不准还要多久"。
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // 设置页是多列瀑布流布局，卡片自己负责行间距并禁止跨列断开（见 page.tsx 的注释）。
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          版本与更新
        </CardTitle>
        <CardDescription>从 GitHub 检查并安装新版本。更新会重启程序，正在运行的任务会被中断。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <FieldGroup>
          <Field data-invalid={!!networkError} data-disabled={busy || restarting || savingNetwork || !network}>
            <FieldLabel htmlFor="update-proxy" className="text-sm font-normal text-muted-foreground">
              更新代理（可选）
            </FieldLabel>
            <div className="flex items-center gap-2">
              <Input
                id="update-proxy"
                autoComplete="off"
                placeholder="http://host:port 或 socks5://host:port（留空=直连）"
                value={network?.proxy ?? ""}
                disabled={busy || restarting || savingNetwork || !network}
                aria-invalid={!!networkError}
                onChange={(e) =>
                  network &&
                  setNetwork({
                    proxy: e.target.value,
                  })
                }
              />
              <Button
                type="button"
                onClick={() => void saveNetwork()}
                disabled={!network || savingNetwork || checking || busy || restarting}
              >
                {savingNetwork ? "保存中…" : "保存"}
              </Button>
            </div>
            <FieldDescription className="text-xs">
              独立出口代理，仅用于检查版本和下载更新，支持 HTTP / HTTPS / SOCKS5。留空保存后直连；不影响任务、网络搜索或
              LLM 的代理。
            </FieldDescription>
            {networkDirty && <p className="text-xs text-muted-foreground">修改尚未保存，保存后将自动检查更新。</p>}
            {networkError && (
              <p role="alert" className="text-xs text-destructive">
                {networkError}
              </p>
            )}
          </Field>
        </FieldGroup>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">当前版本</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "独立程序"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">最新版本</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              更新日志 <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            无法连接 GitHub：{info.error}
            {"　"}可配置更新代理或切换为直连后重试。
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} 没有提供 {info.os}/{info.arch} 的发布包（缺少 {info.asset}），无法自动更新。
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            将下载 <span className="font-mono">{info.asset}</span>
            {info.size ? `（${humanSize(info.size)}）` : ""}，校验 SHA256 并冒烟测试后才会替换，失败自动保留当前版本。
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            当前已是最新版本。
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Docker 下的更新只替换程序本身，不更新镜像里的 playwright / nmap 等工具链，且
            <span className="font-mono"> docker compose up -d </span>
            重建容器后会退回镜像自带的版本。需要连镜像一起升级请执行
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>。
          </p>
        )}

        {progress?.phase === "failed" && (
          <p role="alert" className="text-xs text-destructive">
            更新失败：{progress.error || progress.message}
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "正在重启并应用新版本，请稍候（页面会自动刷新）…" : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => check(false)}
            disabled={checking || busy || restarting || networkDirty || savingNetwork || !network}
          >
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            检查更新
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={
              checking ||
              networkDirty ||
              savingNetwork ||
              !network ||
              busy ||
              restarting ||
              !!info?.error ||
              !info?.has_update ||
              info?.asset_available === false
            }
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `更新到 ${info.latest}` : "立即更新"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              回滚到上一版本
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          一键更新依赖守护脚本重启程序。请通过 <span className="font-mono">start.sh</span>（Windows 为
          <span className="font-mono"> start.bat</span>）启动 ARTEX；直接运行 artex 本体时，程序退出后不会被自动拉起。
        </p>
      </CardContent>
    </Card>
  );
}
