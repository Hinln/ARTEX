"use client";

import * as React from "react";

import {
  CameraIcon,
  CpuIcon,
  FlaskConicalIcon,
  KeyboardIcon,
  RadioTowerIcon,
  SearchIcon,
  SettingsIcon,
  ShieldAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { EgressReport, LLMProfile, ProxyPoolEntry, Settings, WorkerPool } from "@/lib/types";

import { useEgressIp } from "../../_components/egress-ip-banner";
import { BasicAuthCard } from "./_components/basic-auth-card";
import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const [credentialAgent, setCredentialAgent] = React.useState(false);
  const [credentialBinding, setCredentialBinding] = React.useState(false);
  const [savingCredentials, setSavingCredentials] = React.useState(false);
  const [intelligenceEnabled, setIntelligenceEnabled] = React.useState(false);
  const [taskAutoTitleEnabled, setTaskAutoTitleEnabled] = React.useState(false);
  const [chatAutoTitleEnabled, setChatAutoTitleEnabled] = React.useState(false);
  const [savingIntelligence, setSavingIntelligence] = React.useState(false);
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [browserScreenshot, setBrowserScreenshot] = React.useState(false);
  const [browserScreenshotTimestamp, setBrowserScreenshotTimestamp] = React.useState(false);
  const [opsecEnabled, setOpsecEnabled] = React.useState(false);
  const [settingQuery, setSettingQuery] = React.useState("");
  const [activeCategory, setActiveCategory] = React.useState("system");
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [deepSeekKeySet, setDeepSeekKeySet] = React.useState(false);
  const [deepSeekKeyInput, setDeepSeekKeyInput] = React.useState("");
  const [savingDeepSeekKey, setSavingDeepSeekKey] = React.useState(false);
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [proxyPool, setProxyPool] = React.useState<ProxyPoolEntry[]>([]);
  const [proxyPoolActiveId, setProxyPoolActiveId] = React.useState("");
  const [proxyPoolText, setProxyPoolText] = React.useState("");
  const [proxyPoolTimed, setProxyPoolTimed] = React.useState(false);
  const [proxyPoolInterval, setProxyPoolInterval] = React.useState("30");
  const [proxyPoolFailure, setProxyPoolFailure] = React.useState(false);
  const [proxyPoolStrategy, setProxyPoolStrategy] = React.useState<"sequential" | "random">("sequential");
  const [proxyPoolThreshold, setProxyPoolThreshold] = React.useState("3");
  const [proxyPoolCooldown, setProxyPoolCooldown] = React.useState("60");
  const [proxyPoolHealthInterval, setProxyPoolHealthInterval] = React.useState("30");
  const [proxyPoolLogs, setProxyPoolLogs] = React.useState<
    Array<{ at: string; from: string; to: string; reason: string }>
  >([]);
  const [proxyReconnectMax, setProxyReconnectMax] = React.useState("5");
  const [proxyReconnectInterval, setProxyReconnectInterval] = React.useState("30");
  const [savingEgressVisibility, setSavingEgressVisibility] = React.useState(false);
  // 出口探测：测一次真实请求，拿到"直连 / 经全局代理"两条出口 IP。
  const [egressTesting, setEgressTesting] = React.useState(false);
  const [egressReport, setEgressReport] = React.useState<EgressReport | null>(null);
  // 请求序号：改了输入/保存/切捕获之后，在途请求回来时序号已经变了，直接丢弃。
  // 不这么做会出现"结果刚被清空，又被上一次的响应填回来"。
  const egressSeq = React.useRef(0);
  // 清空结果并作废在途请求。
  const clearEgressReport = () => {
    egressSeq.current += 1;
    setEgressReport(null);
    setEgressTesting(false);
  };
  // 探测地址：留空用公网回显；隔离网填内网回显服务。
  const [egressProbeUrl, setEgressProbeUrl] = React.useState("");
  const [savingEgressProbeUrl, setSavingEgressProbeUrl] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [settingsLoaded, setSettingsLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  const [workerPools, setWorkerPools] = React.useState<WorkerPool[]>([]);
  const [llmProfiles, setLlmProfiles] = React.useState<LLMProfile[]>([]);
  // 操作约束注入范围(默认都开)。
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 实验功能:noa 上下文压缩(默认关)。
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 纯前端偏好：不走 /api/settings，直接读写 localStorage。
  const sendMode = useChatSendMode();
  const {
    enabled: showEgressIp,
    isLoading: egressProbeLoading,
    setEnabled: setShowEgressIp,
    probeEgress,
  } = useEgressIp();

  const apply = React.useCallback((s: Settings) => {
    setCredentialAgent(!!s.credential_agent_enabled);
    setCredentialBinding(!!s.credential_traffic_binding);
    setIntelligenceEnabled(!!s.task_intelligence_enabled);
    setTaskAutoTitleEnabled(!!s.task_auto_title_enabled);
    setChatAutoTitleEnabled(!!s.chat_auto_title_enabled);
    setTrafficCapture(!!s.traffic_capture);
    setBrowserScreenshot(!!s.browser_screenshot_enabled);
    setBrowserScreenshotTimestamp(!!s.browser_screenshot_timestamp_enabled);
    setOpsecEnabled(!!s.opsec_enabled);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setDeepSeekKeySet(!!s.deepseek_search_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    const savedProxy = s.global_proxy ?? "";
    setGlobalProxyInput(savedProxy);
    setProxyPool(s.proxy_pool ?? []);
    setProxyPoolActiveId(s.proxy_pool_active_id ?? "");
    setProxyPoolTimed(!!s.proxy_rotate_timed_enabled);
    setProxyPoolInterval(String(s.proxy_rotate_interval_seconds ?? 30));
    setProxyPoolFailure(!!s.proxy_rotate_on_failure);
    setProxyPoolStrategy(s.proxy_rotate_strategy === "random" ? "random" : "sequential");
    setProxyPoolThreshold(String(s.proxy_health_fail_threshold ?? 3));
    setProxyPoolCooldown(String(s.proxy_health_cooldown_seconds ?? 60));
    setProxyPoolHealthInterval(String(s.proxy_health_check_interval_seconds ?? 30));
    setProxyPoolLogs(s.proxy_pool_switch_log ?? []);
    setProxyReconnectMax(String(s.proxy_reconnect_max_attempts ?? 5));
    setProxyReconnectInterval(String(s.proxy_reconnect_interval_seconds ?? 30));
    setEgressProbeUrl(s.egress_probe_url ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setWorkerPools(s.worker_pools ?? []);
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    if (!settingsLoaded) {
      toast.error("设置加载失败，请刷新后再保存 Worker 配置");
      return;
    }
    const n = Number(workers);
    if (workerPools.length > 0) {
      const total = workerPools.reduce((sum, pool) => sum + Number(pool.workers), 0);
      if (workerPools.some((pool) => !pool.profile_id)) {
        toast.error("请选择每个模型池的 LLM 配置");
        return;
      }
      if (workerPools.some((pool) => !Number.isInteger(Number(pool.workers)) || Number(pool.workers) <= 0)) {
        toast.error("每个模型池的 Worker 数必须是大于 0 的整数");
        return;
      }
      if (total > 50) {
        toast.error("Worker 总数不能超过 50");
        return;
      }
      const ids = workerPools.map((pool) => pool.profile_id);
      if (new Set(ids).size !== ids.length) {
        toast.error("不能重复选择同一个 LLM 配置");
        return;
      }
      setSavingWorkers(true);
      api
        .setSettings({
          workers: total,
          worker_pools: workerPools.map((pool) => ({ ...pool, workers: Number(pool.workers) })),
        })
        .then((s) => {
          apply(s);
          toast.success("已保存多模型 Worker 池（对之后启动的任务生效）");
        })
        .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
        .finally(() => setSavingWorkers(false));
      return;
    }
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("并发数必须是大于 0 的整数");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n, worker_pools: [] })
      .then((s) => {
        apply(s);
        toast.success("已保存并发工作 agent 数（对之后启动的任务生效）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const addWorkerPool = () => setWorkerPools((pools) => [...pools, { profile_id: "", workers: 1 }]);

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("已保存 Python 解释器配置");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then((s) => {
        apply(s);
        setSettingsLoaded(true);
        void api
          .proxyPool()
          .then((pool) => {
            setProxyPool(pool.entries);
            setProxyPoolActiveId(pool.active_id);
            setProxyPoolLogs(pool.switch_log);
          })
          .catch(() => undefined);
      })
      .catch(() => {
        setSettingsLoaded(false);
        toast.error("设置加载失败，Worker 配置已锁定，避免覆盖服务器上的模型池");
      })
      .finally(() => setLoaded(true));
  }, [apply]);

  React.useEffect(() => {
    api
      .llmProfiles()
      .then(setLlmProfiles)
      .catch(() => setLlmProfiles([]));
  }, []);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then((s) => {
        apply(s);
        // 捕获开关一变，② 的链路就变了，旧结果不能留。
        clearEgressReport();
      })
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleBrowserScreenshot = (v: boolean) => {
    setBrowserScreenshot(v);
    setSaving(true);
    api
      .setSettings({ browser_screenshot_enabled: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启浏览器截图工具" : "已关闭浏览器截图工具");
      })
      .catch((e) => {
        setBrowserScreenshot(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleBrowserScreenshotTimestamp = (v: boolean) => {
    setBrowserScreenshotTimestamp(v);
    setSaving(true);
    api
      .setSettings({ browser_screenshot_timestamp_enabled: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启截图时间标记" : "已关闭截图时间标记");
      })
      .catch((e) => {
        setBrowserScreenshotTimestamp(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleOpsec = (v: boolean) => {
    setOpsecEnabled(v);
    setSaving(true);
    api
      .setSettings({ opsec_enabled: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启 OPSEC 准则" : "已关闭 OPSEC 准则");
      })
      .catch((e) => {
        setOpsecEnabled(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const matchesSetting = (keywords: string) => {
    const query = settingQuery.trim().toLowerCase();
    return !query || keywords.toLowerCase().includes(query);
  };

  const isSettingVisible = (category: string, keywords: string) => {
    const query = settingQuery.trim();
    return query ? matchesSetting(keywords) : activeCategory === category;
  };

  const settingCategories = [
    { id: "system", label: "系统与账号", hint: "版本、登录与基础配置", icon: SettingsIcon },
    { id: "traffic", label: "流量与安全", hint: "流量、代理、截图与 OPSEC", icon: RadioTowerIcon },
    { id: "agent", label: "Agent 行为", hint: "约束、情报与自动化", icon: CpuIcon },
    { id: "network", label: "网络与工具", hint: "搜索、脚本与并发", icon: SearchIcon },
    { id: "chat", label: "对话与界面", hint: "输入与交互偏好", icon: KeyboardIcon },
  ] as const;

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启 Agent 自动绑定流量" : "已关闭 Agent 自动绑定流量");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启 noa 上下文压缩（对之后启动的运行生效）" : "已关闭 noa 上下文压缩（恢复内置压缩）");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      });
  };

  const toggleAutoTitle = (key: "task_auto_title_enabled" | "chat_auto_title_enabled", value: boolean) => {
    const setter = key === "task_auto_title_enabled" ? setTaskAutoTitleEnabled : setChatAutoTitleEnabled;
    const patch: Partial<Settings> =
      key === "task_auto_title_enabled" ? { task_auto_title_enabled: value } : { chat_auto_title_enabled: value };
    setter(value);
    api
      .setSettings(patch)
      .then(apply)
      .catch((e) => {
        setter(!value);
        toast.error("保存失败：" + (e as Error).message);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("已保存网络搜索配置");
      })
      .catch((e) => {
        toast.error("保存失败：" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("已保存 Brave API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("已保存 Tavily API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveDeepSeekKey = () => {
    setSavingDeepSeekKey(true);
    api.setSettings({ deepseek_search_api_key: deepSeekKeyInput }).then((s) => {
      apply(s);
      setDeepSeekKeyInput("");
      toast.success("已保存 DeepSeek Search API Key");
    }).catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingDeepSeekKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "已保存出口代理" : "已清除出口代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    if (proxyPool.length > 0) {
      toast.info("代理池非空时，全局代理输入框仅用于回显，请在代理池中编辑或切换");
      return;
    }
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        // 保存后链路变了，旧结果（尤其"尚未保存"）立刻失效。
        clearEgressReport();
        if (showEgressIp) void probeEgress().catch(() => undefined);
        toast.success(globalProxyInput.trim() ? "已保存全局代理" : "已清除全局代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  const importProxyPool = () => {
    api
      .importProxyPool({ text: proxyPoolText })
      .then((r) => {
        setProxyPool(r.entries);
        setProxyPoolActiveId(r.active_id);
        setProxyPoolLogs(r.switch_log);
        setProxyPoolText("");
        if (r.errors.length) toast.error(r.errors.map((e) => `第${e.line}行：${e.error}`).join("；"));
        else toast.success("代理池已导入");
      })
      .catch((e) => toast.error("导入失败：" + (e as Error).message));
  };
  const saveProxyPoolStrategy = () => {
    const interval = Number(proxyPoolInterval);
    if (!Number.isFinite(interval) || !Number.isInteger(interval) || interval < 3) {
      toast.error("定时轮换间隔必须是大于等于 3 秒的整数");
      return;
    }
    api
      .setSettings({
        proxy_rotate_timed_enabled: proxyPoolTimed,
        proxy_rotate_interval_seconds: Number(proxyPoolInterval),
        proxy_rotate_on_failure: proxyPoolFailure,
        proxy_rotate_strategy: proxyPoolStrategy,
        proxy_health_fail_threshold: Number(proxyPoolThreshold),
        proxy_health_cooldown_seconds: Number(proxyPoolCooldown),
        proxy_health_check_interval_seconds: Number(proxyPoolHealthInterval),
      })
      .then((s) => {
        apply(s);
        toast.success("代理池轮换策略已保存");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message));
  };
  const testAllProxyPool = () =>
    api
      .testAllProxyPool()
      .then((r) => {
        setProxyPool(r.entries);
        setProxyPoolActiveId(r.active_id);
        setProxyPoolLogs(r.switch_log);
        toast.success("已完成全部代理探测");
      })
      .catch((e) => toast.error("探测失败：" + (e as Error).message));
  const testOneProxyPool = (id: string) =>
    api
      .testProxyPoolEntry(id)
      .then(() => api.proxyPool())
      .then((r) => {
        setProxyPool(r.entries);
        setProxyPoolActiveId(r.active_id);
        setProxyPoolLogs(r.switch_log);
      })
      .catch((e) => toast.error("探测失败：" + (e as Error).message));
  const refreshProxyPool = () =>
    api.proxyPool().then((r) => {
      setProxyPool(r.entries);
      setProxyPoolActiveId(r.active_id);
      setProxyPoolLogs(r.switch_log);
    });
  const toggleProxyPoolEntry = (entry: ProxyPoolEntry) =>
    api
      .updateProxyPool(entry.id, { enabled: !entry.enabled })
      .then(() => refreshProxyPool())
      .catch((e) => toast.error("更新失败：" + (e as Error).message));
  const deleteProxyPoolEntry = (id: string) =>
    api
      .deleteProxyPool(id)
      .then(() => refreshProxyPool())
      .catch((e) => toast.error("删除失败：" + (e as Error).message));

  const saveProxyReconnect = () => {
    api
      .setSettings({
        proxy_reconnect_max_attempts: Number(proxyReconnectMax),
        proxy_reconnect_interval_seconds: Number(proxyReconnectInterval),
      })
      .then((s) => {
        apply(s);
        toast.success("已保存代理重连策略");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message));
  };

  const saveEgressProbeUrl = async () => {
    setSavingEgressProbeUrl(true);
    try {
      const settings = await api.setSettings({ egress_probe_url: egressProbeUrl.trim() });
      setEgressProbeUrl(settings.egress_probe_url ?? "");
      clearEgressReport();
      toast.success(settings.egress_probe_url ? "已保存 IP 探测服务" : "已恢复默认 IP 探测服务");
      if (showEgressIp) void probeEgress().catch(() => undefined);
    } catch (e) {
      toast.error(`保存 IP 探测服务失败：${(e as Error).message}`);
    } finally {
      setSavingEgressProbeUrl(false);
    }
  };

  // 测表单里当前的值，不要求先保存；后端返回直连 / 经代理两条。
  const testEgress = () => {
    const seq = ++egressSeq.current;
    setEgressTesting(true);
    setEgressReport(null);
    api
      .testEgressProxy({
        global_proxy: globalProxyInput.trim(),
        resume: true,
        probe_url: egressProbeUrl.trim(),
      })
      .then((rep) => {
        if (seq !== egressSeq.current) return; // 期间改过配置，这份结果已经过期
        setEgressReport(rep);
        if (rep.ok) {
          // 先取 ②：直连那行永远成功，先命中它会把直连 IP 当成出口报出去。
          const hit =
            rep.checks.find((c) => c.key === "global" && !c.skipped && c.result.ok) ??
            rep.checks.find((c) => !c.skipped && c.result.ok);
          toast.success(hit?.result.exit_ip ? `出口 IP ${hit.result.exit_ip}` : "探测通过");
        } else {
          toast.error("出口探测失败");
        }
      })
      .catch((e) => {
        if (seq === egressSeq.current) toast.error("探测失败：" + (e as Error).message);
      })
      .finally(() => {
        if (seq === egressSeq.current) setEgressTesting(false);
      });
  };

  const toggleEgressVisibility = async (nextEnabled: boolean) => {
    setSavingEgressVisibility(true);
    try {
      await setShowEgressIp(nextEnabled);
      toast.success(nextEnabled ? "已在顶栏显示出口 IP" : "已关闭顶栏出口 IP显示");
    } catch (e) {
      toast.error("保存顶栏出口 IP设置失败：" + (e as Error).message);
    } finally {
      setSavingEgressVisibility(false);
    }
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
        deepseek_search_api_key: deepSeekKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`搜索测试成功 · ${r.backend} 返回 ${r.count} 条结果`);
        else toast.error("搜索测试失败：" + (r.error || "未知错误"));
      })
      .catch((e) => toast.error("搜索测试失败：" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  const saveCredentialOptions = async (patch: Partial<Settings>) => {
    setSavingCredentials(true);
    try {
      const s = await api.setSettings(patch);
      setCredentialAgent(!!s.credential_agent_enabled);
      setCredentialBinding(!!s.credential_traffic_binding);
      setIntelligenceEnabled(!!s.task_intelligence_enabled);
      toast.success("凭据处理设置已保存");
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setSavingCredentials(false);
    }
  };
  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">系统配置</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关</p>
        <div className="relative mt-4 max-w-xl">
          <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2" />
          <Input
            value={settingQuery}
            onChange={(event) => setSettingQuery(event.target.value)}
            placeholder="搜索设置，例如：截图、OPSEC、代理、流量"
            className="pl-9"
            aria-label="搜索系统设置"
          />
        </div>
      </div>

      <div className="flex min-h-0 flex-1 flex-col gap-4 lg:flex-row lg:items-start">
        <aside className="shrink-0 lg:sticky lg:top-4 lg:w-64">
          <div className="rounded-xl border bg-card/60 p-2 shadow-sm">
            <div className="px-3 pb-2 pt-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
              配置目录
            </div>
            <nav className="flex gap-1 overflow-x-auto lg:flex-col" aria-label="系统配置分类">
              {settingCategories.map((category) => {
                const Icon = category.icon;
                const active = !settingQuery.trim() && activeCategory === category.id;
                return (
                  <button
                    key={category.id}
                    type="button"
                    onClick={() => {
                      setActiveCategory(category.id);
                      setSettingQuery("");
                    }}
                    className={`group flex min-w-max items-center gap-3 rounded-lg px-3 py-3 text-left transition-colors lg:min-w-0 ${
                      active ? "bg-primary text-primary-foreground shadow-sm" : "hover:bg-muted"
                    }`}
                  >
                    <Icon className="size-4 shrink-0" />
                    <span className="min-w-0">
                      <span className="block text-sm font-medium">{category.label}</span>
                      <span
                        className={`hidden truncate text-xs lg:block ${active ? "text-primary-foreground/75" : "text-muted-foreground"}`}
                      >
                        {category.hint}
                      </span>
                    </span>
                  </button>
                );
              })}
            </nav>
          </div>
        </aside>

        <main className="min-w-0 flex-1">
          <div className="mb-3 flex items-center justify-between gap-3 px-1">
            <div>
              <h2 className="text-base font-semibold">
                {settingQuery.trim() ? "搜索结果" : settingCategories.find((item) => item.id === activeCategory)?.label}
              </h2>
              <p className="text-muted-foreground text-xs">
                {settingQuery.trim()
                  ? `匹配“${settingQuery.trim()}”的设置项`
                  : settingCategories.find((item) => item.id === activeCategory)?.hint}
              </p>
            </div>
            {settingQuery.trim() ? (
              <Button type="button" variant="ghost" size="sm" onClick={() => setSettingQuery("")}>
                清除搜索
              </Button>
            ) : null}
          </div>

          <div className="flex flex-col divide-y rounded-xl border bg-card/30 px-3 [&>[data-slot=card]]:mb-0 [&>[data-slot=card]]:rounded-none [&>[data-slot=card]]:bg-transparent [&>[data-slot=card]]:py-4 [&>[data-slot=card]]:ring-0">
            {isSettingVisible("system", "更新 版本 升级") ? <UpdateCard /> : null}
            {isSettingVisible("system", "基础认证 登录 用户名 密码") ? <BasicAuthCard /> : null}

            <Card
              className={`${isSettingVisible("traffic", "流量 捕获 traffic proxy HTTP 代理") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <RadioTowerIcon className="size-4" />
                  流量捕获
                </CardTitle>
                <CardDescription>
                  开启后，所有 Agent 的 HTTP 流量经记录代理全量落库，并向 Agent 注入 traffic_search / traffic_get
                  工具与代理配置（提示词含代理说明）。
                  <br />
                  关闭（默认）时不记录任何流量：Agent
                  <b>不会</b>拿到代理配置与流量工具，提示词也<b>不含</b>代理相关内容。切换后会即时重建 Agent 生效。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex items-center justify-between gap-4">
                <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
                  {trafficCapture ? "已开启 · 正在记录流量并注入代理" : "已关闭 · 不记录、不注入代理"}
                </Label>
                <Switch
                  id="traffic-capture"
                  checked={trafficCapture}
                  disabled={!loaded || saving}
                  onCheckedChange={toggleTraffic}
                />
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("traffic", "Agent 自动绑定流量 数据包 报告 evidence") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <RadioTowerIcon className="size-4" />
                  Agent 自动绑定流量
                </CardTitle>
                <CardDescription id="agent-traffic-binding-description">
                  默认关闭。开启后，漏洞入库时触发的报告 Agent 会核对已有 HTTP 请求/响应，关联对应流量后再编写报告。
                  <b>查阅数据包及额外的工具调用会增加 Token 消耗。</b>
                  <br />
                  TCP、未抓包或没有匹配流量时仍可正常上报。此开关不影响流量捕获、人工绑定及已保存证据的查看。 对下一轮
                  Agent 生效；关闭后会立即拒绝新的自动绑定。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex items-center justify-between gap-4">
                <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
                  {agentTrafficBinding ? "已开启 · 会增加 Token 消耗" : "已关闭 · 可继续人工绑定"}
                </Label>
                <Switch
                  id="agent-traffic-binding"
                  aria-describedby="agent-traffic-binding-description"
                  checked={agentTrafficBinding}
                  disabled={!loaded || saving}
                  onCheckedChange={toggleAgentTrafficBinding}
                />
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("traffic", "截图 浏览器报告时间 OPSEC 请求头 代理 TLS 流量") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <CameraIcon className="size-4" />
                  浏览器与报告安全
                </CardTitle>
                <CardDescription>
                  集中管理报告截图、截图时间和目标交互的 OPSEC 准则。开关只影响后续 Agent 运行。
                </CardDescription>
              </CardHeader>
              <CardContent className="divide-y">
                <div className="flex items-center justify-between gap-4 py-3 first:pt-0">
                  <div className="min-w-0">
                    <Label htmlFor="browser-screenshot" className="text-sm font-medium">
                      浏览器截图
                    </Label>
                    <p className="text-muted-foreground mt-1 text-xs">
                      报告撰写 Agent 可使用 browser MCP 为复现步骤保存截图。
                    </p>
                  </div>
                  <Switch
                    id="browser-screenshot"
                    checked={browserScreenshot}
                    disabled={!loaded || saving}
                    onCheckedChange={toggleBrowserScreenshot}
                  />
                </div>
                <div className="flex items-center justify-between gap-4 py-3">
                  <div className="min-w-0">
                    <Label htmlFor="browser-screenshot-timestamp" className="text-sm font-medium">
                      截图时间标记
                    </Label>
                    <p className="text-muted-foreground mt-1 text-xs">在截图右上角显示年月日时分秒，不修改原始文件。</p>
                  </div>
                  <Switch
                    id="browser-screenshot-timestamp"
                    checked={browserScreenshotTimestamp}
                    disabled={!loaded || saving}
                    onCheckedChange={toggleBrowserScreenshotTimestamp}
                  />
                </div>
                <div className="flex items-center justify-between gap-4 py-3 last:pb-0">
                  <div className="min-w-0">
                    <Label htmlFor="opsec-enabled" className="text-sm font-medium">
                      OPSEC 准则
                    </Label>
                    <p className="text-muted-foreground mt-1 text-xs">
                      要求执行型 Agent 复用流量和浏览器特征、控制请求噪声，并遵守平台代理与授权范围。
                    </p>
                  </div>
                  <Switch
                    id="opsec-enabled"
                    checked={opsecEnabled}
                    disabled={!loaded || saving}
                    onCheckedChange={toggleOpsec}
                  />
                </div>
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("traffic", "全局代理 出口 IP socks5 http https") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <RadioTowerIcon className="size-4" />
                  全局代理
                </CardTitle>
                <CardDescription>
                  设置 Agent 目标流量的统一出口。留空时直连；网络搜索和 LLM 代理不受影响。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-5">
                <section className="space-y-2">
                  <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
                    代理地址
                  </Label>
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      id="global-proxy"
                      className="min-w-0 flex-1"
                      autoComplete="off"
                      placeholder="socks5://user:pass@host:1080 或 http://host:port（留空=直连）"
                      value={globalProxyInput}
                      disabled={!loaded || savingGlobalProxy}
                      onChange={(e) => {
                        setGlobalProxyInput(e.target.value);
                        // 旧结果对应的是上一次的地址，留着会让人以为新值已经测过。
                        clearEgressReport();
                      }}
                    />
                    <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                      保存
                    </Button>
                    {/* 测表单里当前的值，不要求先保存。 */}
                    <Button type="button" variant="outline" onClick={testEgress} disabled={!loaded || egressTesting}>
                      {egressTesting ? "测试中…" : "测试代理"}
                    </Button>
                  </div>
                  <p className="text-muted-foreground text-xs">
                    {globalProxyInput.trim() ? "已配置 · 所有目标流量经此代理出网" : "未配置 · 目标流量直连出网"}
                  </p>
                  <details className="text-xs">
                    <summary className="text-muted-foreground cursor-pointer select-none">代理工作方式</summary>
                    <p className="text-muted-foreground mt-2 max-w-4xl leading-relaxed">
                      开启流量捕获时，此代理作为记录代理的上游；关闭时，代理会注入 Agent 的 bash / WebFetch 出网。支持
                      http、https 和 socks5，可带账号密码。关闭流量捕获使用 socks5 时，命令行工具需支持
                      ALL_PROXY；Python urllib 等工具可能不支持，开启流量捕获可避免此限制。
                    </p>
                  </details>
                </section>
                <details className="text-xs">
                  <summary className="text-muted-foreground cursor-pointer select-none">异常重连策略</summary>
                  <div className="mt-3 space-y-3 rounded-md border p-3 sm:p-4">
                    <p className="text-muted-foreground">
                      代理异常暂停的任务按此策略探测；次数耗尽后保持暂停，可手动恢复。
                    </p>
                    <div className="grid grid-cols-1 items-end gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
                      <div className="space-y-1.5">
                        <Label htmlFor="proxy-reconnect-max">最大重连次数</Label>
                        <Input
                          id="proxy-reconnect-max"
                          type="number"
                          min={1}
                          value={proxyReconnectMax}
                          onChange={(e) => setProxyReconnectMax(e.target.value)}
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="proxy-reconnect-interval">重连间隔（秒）</Label>
                        <Input
                          id="proxy-reconnect-interval"
                          type="number"
                          min={1}
                          value={proxyReconnectInterval}
                          onChange={(e) => setProxyReconnectInterval(e.target.value)}
                        />
                      </div>
                      <Button type="button" variant="outline" className="w-full sm:w-auto" onClick={saveProxyReconnect}>
                        保存策略
                      </Button>
                    </div>
                  </div>
                </details>
                <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
                  <div className="min-w-0">
                    <Label htmlFor="show-egress-ip" className="text-sm font-normal">
                      在顶栏显示出口 IP
                    </Label>
                    <p className="text-muted-foreground text-xs">
                      开启后显示直连 IP 和全局代理 IP，使用已保存的全局代理探测。
                    </p>
                  </div>
                  <Switch
                    id="show-egress-ip"
                    checked={showEgressIp}
                    disabled={!settingsLoaded || savingEgressVisibility || egressProbeLoading}
                    onCheckedChange={(checked) => void toggleEgressVisibility(checked)}
                  />
                </div>
                {/* 紧贴结果区：用户看两行时正好需要它。细节放文档。 */}
                <details className="text-xs">
                  <summary className="text-muted-foreground cursor-pointer select-none">出口 IP 测试说明</summary>
                  <p className="text-muted-foreground mt-2 leading-relaxed">
                    测试会访问 IP 回显服务。未设置自定义地址时，默认访问 api.ipify.org，失败时改用 ifconfig.me。
                    配置自定义地址后仅使用该服务。结果中的直连 IP 与代理 IP 可用于确认代理是否生效。修改地址后无需保存即可测试。
                  </p>
                </details>
                {/* 内网里公网回显不可达，两行都会失败——不是代理坏了。 */}
                <details className="text-xs">
                  <summary className="text-muted-foreground cursor-pointer select-none">
                    自定义 IP 探测服务（可选）
                  </summary>
                  <div className="mt-2 flex flex-col gap-2">
                    <Label htmlFor="egress-probe-url">IP 探测服务地址</Label>
                    <div className="flex flex-col gap-2 sm:flex-row">
                      <Input
                        id="egress-probe-url"
                        autoComplete="off"
                        placeholder="留空使用默认服务；内网可填 http://<内网IP>:<端口>/"
                        value={egressProbeUrl}
                        disabled={!settingsLoaded || savingEgressProbeUrl}
                        onChange={(e) => {
                          setEgressProbeUrl(e.target.value);
                          clearEgressReport();
                        }}
                      />
                      <Button
                        type="button"
                        variant="outline"
                        disabled={!settingsLoaded || savingEgressProbeUrl}
                        onClick={() => void saveEgressProbeUrl()}
                      >
                        {savingEgressProbeUrl ? "保存中…" : "保存探测服务"}
                      </Button>
                    </div>
                    <span className="text-muted-foreground leading-relaxed">
                      保存后用于顶栏出口 IP、代理测试、健康检查和自动重连，刷新页面或重启后仍生效。清空地址并保存可恢复默认服务。
                    </span>
                    <span className="text-muted-foreground leading-relaxed">
                      自定义 IP 探测服务需返回纯文本 IP 或 <code>{'{"ip":"1.2.3.4"}'}</code> 格式的 JSON。
                      若内网环境下无法访问公网，或本机和内网代理共用同一个公网出口， 则可替换为内网 IP 探测服务，此时 ①
                      和 ② 的 IP 不同，就说明流量走了内网代理。
                    </span>
                  </div>
                </details>
                {egressReport && (
                  <div className="flex flex-col gap-1 text-xs">
                    {egressReport.checks.map((c) => {
                      // 经 MITM 时 via 是 HTTP（MITM 自己是代理），标出来会让人以为填错了。
                      const viaCapture = !!c.path && c.path !== egressReport.configured;
                      return (
                        <div key={c.key} className="flex flex-wrap items-center gap-x-2">
                          <span
                            className={
                              c.skipped
                                ? "text-muted-foreground"
                                : c.result.ok
                                  ? "text-emerald-600"
                                  : "text-destructive"
                            }
                          >
                            {c.skipped ? "—" : c.result.ok ? "✓" : "✗"}
                          </span>

                          <span
                            className="w-56 shrink-0"
                            title={
                              c.key === "direct"
                                ? "不经过任何代理，用来和 ② 对比"
                                : viaCapture
                                  ? `实际探测地址：${c.path}（请求经流量捕获转发）`
                                  : "经这个代理出去，不需要先保存"
                            }
                          >
                            {c.label}
                          </span>
                          {c.skipped ? (
                            <span className="text-muted-foreground">{c.skip_reason}</span>
                          ) : c.result.ok ? (
                            <span className="font-mono">
                              <span title="外界看到的出口 IP（探测站点回显的客户端地址）">{c.result.exit_ip}</span> ·{" "}
                              <span title="从 ARTEX 发出请求到拿到响应的完整往返耗时；经代理时包含代理那一跳">
                                {c.result.latency_ms}ms
                              </span>
                              {c.result.via && c.result.via !== "direct" && !viaCapture && (
                                <span className="text-muted-foreground">
                                  {" "}
                                  · 经 {c.result.via_label ?? c.result.via}
                                </span>
                              )}
                            </span>
                          ) : (
                            // 只显示第一个地址的原因：refused=代理没开，timeout=出不了网。
                            <span className="text-destructive">
                              {c.result.probes?.find((p) => p.error)?.error ?? c.result.error}
                            </span>
                          )}
                        </div>
                      );
                    })}
                    {egressReport.warnings?.map((w) => (
                      <p key={w} className="text-amber-700">
                        ⚠ {w}
                      </p>
                    ))}
                  </div>
                )}
                <section className="space-y-3 border-t pt-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <p className="text-sm font-medium">
                        代理池 <span className="text-muted-foreground font-normal">{proxyPool.length}</span>
                      </p>
                      <p className="text-muted-foreground mt-1 text-xs">
                        当前：
                        {proxyPool.find((entry) => entry.id === proxyPoolActiveId)?.name ??
                          (proxyPoolActiveId ? "已选择代理" : "直连")}
                      </p>
                    </div>
                    <Button type="button" variant="outline" size="sm" onClick={testAllProxyPool}>
                      测试全部
                    </Button>
                  </div>
                  <details className="rounded-md border px-3 py-2 text-xs">
                    <summary className="text-muted-foreground cursor-pointer select-none">批量导入代理</summary>
                    <Textarea
                      className="mt-3 font-mono"
                      rows={3}
                      placeholder="每行一个代理：URL|名称|备注"
                      value={proxyPoolText}
                      onChange={(e) => setProxyPoolText(e.target.value)}
                    />
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                      <Button type="button" size="sm" onClick={importProxyPool} disabled={!proxyPoolText.trim()}>
                        导入
                      </Button>
                      <span className="text-muted-foreground">显示代理地址时会隐藏账号密码。</span>
                    </div>
                  </details>
                  <div className="space-y-2">
                    {proxyPool.length === 0 ? (
                      <p className="text-muted-foreground text-xs">暂无代理池条目。</p>
                    ) : (
                      proxyPool.map((entry) => (
                        <div
                          key={entry.id}
                          className="grid gap-3 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
                        >
                          <div className="flex min-w-0 items-start gap-3">
                            <Switch
                              checked={entry.enabled}
                              onCheckedChange={() => toggleProxyPoolEntry(entry)}
                              aria-label="启用代理"
                            />
                            <div className="grid min-w-0 flex-1 gap-x-5 gap-y-2 text-xs sm:grid-cols-2">
                              <div className="min-w-0">
                                <p className="text-muted-foreground">代理地址</p>
                                <p className="truncate font-mono" title={entry.url}>
                                  {entry.name ? `${entry.name} · ` : ""}
                                  {entry.url}
                                </p>
                              </div>
                              <div>
                                <p className="text-muted-foreground">测试出口 IP</p>
                                <p className="break-all font-mono">
                                  {entry.egress_ip || (entry.last_error ? "探测失败" : "尚未测试")}
                                </p>
                              </div>
                              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                                <span
                                  className={
                                    entry.health === "healthy"
                                      ? "text-emerald-600"
                                      : entry.health === "disabled"
                                        ? "text-muted-foreground"
                                        : "text-amber-600"
                                  }
                                >
                                  {!entry.enabled
                                    ? "已停用"
                                    : entry.health === "healthy"
                                      ? "健康"
                                      : entry.health === "unhealthy-cooling"
                                        ? "冷却中"
                                        : "待检测"}
                                </span>
                                {entry.id === proxyPoolActiveId && <span className="text-primary">当前使用</span>}
                                <span className="text-muted-foreground">失败 {entry.consecutive_failures} 次</span>
                              </div>
                              <div className="text-muted-foreground">
                                {entry.latency_ms != null ? `${entry.latency_ms} ms` : "暂无延迟数据"}
                                {entry.last_checked_at &&
                                  !entry.last_checked_at.startsWith("0001-") &&
                                  ` · ${new Date(entry.last_checked_at).toLocaleTimeString()}`}
                                {entry.last_error && <span className="text-destructive"> · {entry.last_error}</span>}
                              </div>
                            </div>
                          </div>
                          <div className="flex justify-end gap-1">
                            <Button type="button" variant="ghost" size="sm" onClick={() => testOneProxyPool(entry.id)}>
                              测试
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => deleteProxyPoolEntry(entry.id)}
                            >
                              删除
                            </Button>
                          </div>
                        </div>
                      ))
                    )}
                  </div>
                  <details className="rounded-md border px-3 py-2 text-xs">
                    <summary className="text-muted-foreground cursor-pointer select-none">轮换与健康检查策略</summary>
                    <div className="mt-3 space-y-3">
                      <p className="text-muted-foreground">设置代理池的切换条件与健康检查频率。</p>
                      <div className="flex flex-wrap items-center gap-x-8 gap-y-3 border-b pb-3">
                        <div className="flex items-center gap-3">
                          <Label htmlFor="proxy-pool-timed">定时轮换</Label>
                          <Switch id="proxy-pool-timed" checked={proxyPoolTimed} onCheckedChange={setProxyPoolTimed} />
                        </div>
                        <div className="flex items-center gap-3">
                          <Label htmlFor="proxy-pool-failure">失效轮换</Label>
                          <Switch
                            id="proxy-pool-failure"
                            checked={proxyPoolFailure}
                            onCheckedChange={setProxyPoolFailure}
                          />
                        </div>
                      </div>
                      <div className="grid max-w-2xl grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
                        <div className="space-y-1.5">
                          <Label htmlFor="proxy-pool-strategy">切换顺序</Label>
                          <Select
                            value={proxyPoolStrategy}
                            onValueChange={(v) => setProxyPoolStrategy(v as "sequential" | "random")}
                          >
                            <SelectTrigger id="proxy-pool-strategy" className="w-full">
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="sequential">顺序</SelectItem>
                              <SelectItem value="random">随机</SelectItem>
                            </SelectContent>
                          </Select>
                        </div>
                        <div className="space-y-1.5">
                          <Label htmlFor="proxy-pool-interval">轮换间隔（秒，≥3）</Label>
                          <Input
                            id="proxy-pool-interval"
                            type="number"
                            min={3}
                            value={proxyPoolInterval}
                            onChange={(e) => setProxyPoolInterval(e.target.value)}
                          />
                        </div>
                        <div className="space-y-1.5">
                          <Label htmlFor="proxy-pool-threshold">连续失败阈值（次）</Label>
                          <Input
                            id="proxy-pool-threshold"
                            type="number"
                            min={1}
                            value={proxyPoolThreshold}
                            onChange={(e) => setProxyPoolThreshold(e.target.value)}
                          />
                        </div>
                        <div className="space-y-1.5">
                          <Label htmlFor="proxy-pool-cooldown">故障冷却时间（秒）</Label>
                          <Input
                            id="proxy-pool-cooldown"
                            type="number"
                            min={1}
                            value={proxyPoolCooldown}
                            onChange={(e) => setProxyPoolCooldown(e.target.value)}
                          />
                        </div>
                        <div className="space-y-1.5">
                          <Label htmlFor="proxy-pool-health-interval">健康检查间隔（秒）</Label>
                          <Input
                            id="proxy-pool-health-interval"
                            type="number"
                            min={1}
                            value={proxyPoolHealthInterval}
                            onChange={(e) => setProxyPoolHealthInterval(e.target.value)}
                          />
                        </div>
                      </div>
                      <div className="flex justify-end border-t pt-3">
                        <Button
                          type="button"
                          variant="outline"
                          className="w-full sm:w-auto"
                          onClick={saveProxyPoolStrategy}
                        >
                          保存轮换策略
                        </Button>
                      </div>
                    </div>
                  </details>
                  {proxyPoolLogs.length > 0 && (
                    <details className="text-xs">
                      <summary className="text-muted-foreground cursor-pointer select-none">
                        最近切换记录（{proxyPoolLogs.length}）
                      </summary>
                      <div className="mt-2 space-y-1">
                        {proxyPoolLogs
                          .slice(-5)
                          .reverse()
                          .map((log) => (
                            <p key={`${log.at}-${log.to}`} className="text-muted-foreground">
                              {new Date(log.at).toLocaleTimeString()} · {log.from || "直连"} → {log.to || "直连"} ·{" "}
                              {log.reason}
                            </p>
                          ))}
                      </div>
                    </details>
                  )}
                </section>
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("agent", "操作约束 allow deny planner worker 爆破") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <ShieldAlertIcon className="size-4" />
                  操作约束注入
                </CardTitle>
                <CardDescription>
                  开启后，把每个任务的<b>操作约束</b>（在任务总览「操作约束」里维护的 allow/deny 条目）拼进对应 Agent
                  的系统提示，用来框定探索边界（如「仅测当前端口」「禁止爆破」）。
                  <br />
                  可分别控制注入到 <b>规划者（planner）</b>与 <b>执行者（worker）</b>
                  ；默认都开。切换即时生效（下一轮读取），无需重建 Agent。关闭后该 Agent 不再看到约束。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-4">
                <div className="flex items-center justify-between gap-4">
                  <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                    注入规划者（planner）{injectPlanner ? " · 已开启" : " · 已关闭"}
                  </Label>
                  <Switch
                    id="inject-planner"
                    checked={injectPlanner}
                    disabled={!loaded}
                    onCheckedChange={toggleInjectPlanner}
                  />
                </div>
                <div className="flex items-center justify-between gap-4">
                  <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                    注入执行者（worker）{injectWorker ? " · 已开启" : " · 已关闭"}
                  </Label>
                  <Switch
                    id="inject-worker"
                    checked={injectWorker}
                    disabled={!loaded}
                    onCheckedChange={toggleInjectWorker}
                  />
                </div>
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("agent", "实验 noa 压缩") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <FlaskConicalIcon className="size-4" />
                  实验功能
                </CardTitle>
                <CardDescription>
                  尚在验证中的机制，默认关闭。可能改变 Agent 行为或影响稳定性，请在了解影响后启用。
                  <br />
                  <b>noa 上下文压缩</b>：由模型主动压缩长对话历史（norma v0.4.0）。开启后平台接入的四类 Agent（
                  <b>规划者 / 执行者 / 主 Agent / 对话</b>）改用 noa 接管上下文，取代内置压缩，
                  压缩原文会归档到任务工作目录下便于回溯。切换即时生效（对之后启动的运行生效），无需重建 Agent；
                  关闭后立即恢复内置压缩。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex items-center justify-between gap-4">
                <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
                  noa 上下文压缩{noaCompaction ? " · 已开启" : " · 已关闭"}
                </Label>
                <Switch
                  id="noa-compaction"
                  checked={noaCompaction}
                  disabled={!loaded}
                  onCheckedChange={toggleNoaCompaction}
                />
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("agent", "自动标题 任务 对话") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle>自动标题</CardTitle>
                <CardDescription>
                  为 Agent 创建的任务和对话首条消息生成简短标题。两个开关独立生效，默认关闭。
                </CardDescription>
              </CardHeader>
              <CardContent>
                <Field orientation="horizontal">
                  <FieldContent>
                    <FieldLabel htmlFor="task-auto-title">任务名称</FieldLabel>
                    <FieldDescription>Agent 创建任务时自动生成任务名称；失败时保留未命名任务。</FieldDescription>
                  </FieldContent>
                  <Switch
                    id="task-auto-title"
                    checked={taskAutoTitleEnabled}
                    disabled={!loaded}
                    onCheckedChange={(value) => toggleAutoTitle("task_auto_title_enabled", value)}
                  />
                </Field>
                <Field orientation="horizontal" className="mt-5">
                  <FieldContent>
                    <FieldLabel htmlFor="chat-auto-title">会话标题</FieldLabel>
                    <FieldDescription>
                      对话首条消息后自动生成会话标题；先显示消息截断文本，失败时保留截断标题。
                    </FieldDescription>
                  </FieldContent>
                  <Switch
                    id="chat-auto-title"
                    checked={chatAutoTitleEnabled}
                    disabled={!loaded}
                    onCheckedChange={(value) => toggleAutoTitle("chat_auto_title_enabled", value)}
                  />
                </Field>
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("agent", "任务情报 凭据 线索") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle>任务情报</CardTitle>
                <CardDescription>自动记录任务中获取的凭据、重要线索和敏感数据，并保留来源证据。</CardDescription>
              </CardHeader>
              <CardContent>
                <Field orientation="horizontal">
                  <FieldContent>
                    <FieldLabel htmlFor="task-intelligence">Agent 自动记录</FieldLabel>
                    <FieldDescription>
                      默认关闭。任务执行时上报凭据、重要线索和敏感数据，会增加任务 Agent 的提示与上报
                      Token。仅负责记录，不启动凭据整理。关闭后同时停用下方 Agent
                      与流量绑定，已有情报和整理结果仍可查看。
                    </FieldDescription>
                  </FieldContent>
                  <Switch
                    id="task-intelligence"
                    checked={intelligenceEnabled}
                    disabled={!loaded || savingIntelligence || savingCredentials}
                    onCheckedChange={async (value) => {
                      setSavingIntelligence(true);
                      try {
                        const settings = await api.setSettings({ task_intelligence_enabled: value });
                        setIntelligenceEnabled(!!settings.task_intelligence_enabled);
                        setCredentialAgent(!!settings.credential_agent_enabled);
                        setCredentialBinding(!!settings.credential_traffic_binding);
                        toast.success(value ? "任务情报自动记录已开启" : "任务情报自动记录已关闭");
                      } catch (error) {
                        toast.error("保存失败：" + (error as Error).message);
                      } finally {
                        setSavingIntelligence(false);
                      }
                    }}
                  />
                </Field>
                <Field orientation="horizontal" className="mt-5">
                  <FieldContent>
                    <FieldLabel htmlFor="credential-agent">Agent 开关</FieldLabel>
                    <FieldDescription>
                      默认关闭。独立整理已有和新增凭据，拆分并高亮账号、密码、Token 和
                      Cookie，不改变原始上报或验证状态。复杂文本会使用额外模型 Token；可解析的 JSON
                      无需模型。需先开启上方自动记录。模型可在「Agent → 凭据整理」中配置。
                    </FieldDescription>
                  </FieldContent>
                  <Switch
                    id="credential-agent"
                    checked={credentialAgent}
                    disabled={!loaded || savingCredentials || savingIntelligence || !intelligenceEnabled}
                    onCheckedChange={(value) => void saveCredentialOptions({ credential_agent_enabled: value })}
                  />
                </Field>
                {credentialAgent ? (
                  <Field orientation="horizontal" className="mt-5">
                    <FieldContent>
                      <FieldLabel htmlFor="credential-traffic">绑定流量</FieldLabel>
                      <FieldDescription>
                        默认关闭，仅在 Agent
                        开关开启时可用。核对已抓取流量中的资产与凭据，关联真实请求或响应并保存匹配片段；不主动登录，也不把匹配流量视为验证成功。关闭
                        Agent 时同时停用，已有绑定保留。
                      </FieldDescription>
                    </FieldContent>
                    <Switch
                      id="credential-traffic"
                      checked={credentialBinding}
                      disabled={!loaded || savingCredentials}
                      onCheckedChange={(value) => void saveCredentialOptions({ credential_traffic_binding: value })}
                    />
                  </Field>
                ) : null}
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("network", "网络搜索 Brave Tavily DuckDuckGo DeepSeek web_search") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <SearchIcon className="size-4" />
                  网络搜索
                </CardTitle>
                <CardDescription>
                  这是网络搜索的<b>总开关 + 来源配置</b>。开启后，才能在<b>每个 Agent 的配置</b>里单独选择是否启用
                  <b>web_search</b>（仅返回标题/链接/摘要，不抓取正文；抓取由 WebFetch 负责）。网络搜索<b>不走</b>
                  记录代理，独立于流量捕获。
                  <br />
                  来源可选 <b>DuckDuckGo（ddgs）</b>（无需 Key）、<b>Brave（免费版）</b>（需填写 Brave API Key）、{" "}
                  <b>Tavily</b>（需填写 Tavily API Key）或 <b>DeepSeek</b>（需单独配置 Search API Key）。总开关关闭时，各 Agent
                  的网络搜索开关不可用。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-4">
                <div className="flex items-center justify-between gap-4">
                  <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                    {webSearch ? "总开关已开启 · 可在各 Agent 配置里单独启用" : "已关闭 · 各 Agent 无法启用网络搜索"}
                  </Label>
                  <Switch
                    id="web-search"
                    checked={webSearch}
                    disabled={!loaded || saving}
                    onCheckedChange={(v) => {
                      setWebSearch(v); // optimistic
                      saveWebSearch({ web_search_enabled: v });
                    }}
                  />
                </div>

                {webSearch && (
                  <div className="flex items-center justify-between gap-4">
                    <Label className="text-sm font-normal text-muted-foreground">搜索来源</Label>
                    <Select
                      value={backend}
                      disabled={!loaded || saving}
                      onValueChange={(v) => {
                        setBackend(v); // optimistic
                        saveWebSearch({ web_search_backend: v });
                      }}
                    >
                      <SelectTrigger className="w-48 shrink-0">
                        <SelectValue placeholder="选择来源" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="ddgs">DuckDuckGo（ddgs · 免费无 Key）</SelectItem>
                        <SelectItem value="brave-free">Brave（免费版 · 需 Key）</SelectItem>
                        <SelectItem value="tavily">Tavily（需 Key）</SelectItem>
                        <SelectItem value="deepseek">DeepSeek（官方）</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                )}

                {webSearch && backend === "deepseek" && (
                  <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                    <p className="text-sm font-medium">DeepSeek 联网搜索</p>
                    <p className="text-muted-foreground text-xs leading-relaxed">
                      通过 DeepSeek 独立 Search / Fetch API 提供搜索与网页提取，使用当前 Agent 选择的模型生成查询和整理结果。
                    </p>
                    <p className="text-muted-foreground text-xs leading-relaxed">
                      搜索请求由 ARTEX 服务端发往 DeepSeek，可使用上方独立出口代理。API Key 只保存于服务端设置，不会回显。
                    </p>
                    <div className="flex items-center gap-2">
                      <Input
                        type="password"
                        autoComplete="off"
                        placeholder={deepSeekKeySet ? "已配置（留空则不变）" : "DeepSeek Search API Key"}
                        value={deepSeekKeyInput}
                        disabled={!loaded || savingDeepSeekKey}
                        onChange={(e) => setDeepSeekKeyInput(e.target.value)}
                      />
                      <Button
                        type="button"
                        onClick={saveDeepSeekKey}
                        disabled={!loaded || savingDeepSeekKey || deepSeekKeyInput.trim() === ""}
                      >
                        保存
                      </Button>
                    </div>
                    {deepSeekKeySet && <span className="text-xs text-emerald-500">API Key 已配置</span>}
                  </div>
                )}

                {webSearch && backend === "brave-free" && (
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                      Brave Search API Key
                      {braveKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                    </Label>
                    <div className="flex items-center gap-2">
                      <Input
                        id="brave-key"
                        type="password"
                        autoComplete="off"
                        placeholder={braveKeySet ? "已配置（留空则不变）" : "输入 Brave API Key"}
                        value={braveKeyInput}
                        disabled={!loaded || savingKey}
                        onChange={(e) => setBraveKeyInput(e.target.value)}
                      />
                      <Button
                        type="button"
                        onClick={saveBraveKey}
                        disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                      >
                        保存
                      </Button>
                    </div>
                    {braveNeedsKey && (
                      <p className="text-xs text-amber-500">
                        已选择 Brave 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                      </p>
                    )}
                    <p className="text-muted-foreground text-xs">
                      免费版额度约 2,000 次/月。前往 https://brave.com/search/api/ 获取 Key。
                    </p>
                  </div>
                )}

                {webSearch && backend === "tavily" && (
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                      Tavily Search API Key
                      {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                    </Label>
                    <div className="flex items-center gap-2">
                      <Input
                        id="tavily-key"
                        type="password"
                        autoComplete="off"
                        placeholder={tavilyKeySet ? "已配置（留空则不变）" : "输入 Tavily API Key（tvly-…）"}
                        value={tavilyKeyInput}
                        disabled={!loaded || savingTavilyKey}
                        onChange={(e) => setTavilyKeyInput(e.target.value)}
                      />
                      <Button
                        type="button"
                        onClick={saveTavilyKey}
                        disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                      >
                        保存
                      </Button>
                    </div>
                    {webSearch && backend === "tavily" && !tavilyKeySet && (
                      <p className="text-xs text-amber-500">
                        已选择 Tavily 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                      </p>
                    )}
                    <p className="text-muted-foreground text-xs">前往 https://tavily.com 注册并获取 API Key。</p>
                  </div>
                )}

                {webSearch && (
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                      出口代理（可选）
                    </Label>
                    <div className="flex items-center gap-2">
                      <Input
                        id="ws-proxy"
                        autoComplete="off"
                        placeholder="http://host:port 或 socks5://host:port（留空=直连）"
                        value={proxyInput}
                        disabled={!loaded || savingProxy}
                        onChange={(e) => setProxyInput(e.target.value)}
                      />
                      <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                        保存
                      </Button>
                    </div>
                    <p className="text-muted-foreground text-xs">
                      独立出口代理，仅用于访问搜索端点（VPN/SOCKS 等）。与记录流量的 MITM
                      代理无关；网络不通时经此代理访问。
                    </p>
                  </div>
                )}

                {webSearch && (
                  <div className="flex items-center justify-between gap-4 border-t pt-4">
                    <p className="text-muted-foreground text-xs">
                      用当前配置（来源 + 代理 + Key）实际搜索一次「test」，验证是否可用。
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      onClick={runTest}
                      disabled={!loaded || testing}
                      className="shrink-0"
                    >
                      {testing ? "测试中…" : "测试搜索"}
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("network", "Python 解释器 自定义脚本 script") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <RadioTowerIcon className="size-4" />
                  自定义脚本 · Python 解释器
                </CardTitle>
                <CardDescription>
                  自定义 <b>script</b> 类型工具用它跑 Python。开机会自动检测（python3 优先）；此处可手填 venv /
                  特定版本的绝对路径，留空则运行时自动检测。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="flex items-center gap-2">
                  <Input
                    className="font-mono text-sm"
                    placeholder="/usr/bin/python3（留空=自动检测）"
                    value={pyInterp}
                    disabled={!loaded || saving}
                    onChange={(e) => setPyInterp(e.target.value)}
                  />
                  <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                    重新检测
                  </Button>
                  <Button onClick={savePython} disabled={!loaded || saving}>
                    保存
                  </Button>
                </div>
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("network", "工作并发 Worker 并发模型池") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <CpuIcon className="size-4" />
                  工作并发 · Work Agent 数
                </CardTitle>
                <CardDescription>
                  每个任务并发运行的工作 agent 数量（默认 3）。数值越大并发探测越多、消耗也越高。修改后
                  <b>对之后启动的任务生效</b>，正在运行的任务不受影响。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="flex flex-col gap-2 rounded-md border p-3">
                  <div className="flex items-center justify-between">
                    <Label className="text-sm">多模型 Worker 池</Label>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={addWorkerPool}
                      disabled={!loaded || !settingsLoaded || savingWorkers}
                    >
                      添加模型池
                    </Button>
                  </div>
                  {workerPools.map((pool, index) => {
                    const selected = llmProfiles.find((profile) => profile.id === pool.profile_id);
                    const placeholder =
                      selected?.name || (pool.profile_id ? `配置 #${pool.profile_id} 已删除/不可用` : "选择 LLM 配置");
                    return (
                      // biome-ignore lint/suspicious/noArrayIndexKey: pool rows are edited in place and only deleted.
                      <div className="flex items-center gap-2" key={`${pool.profile_id}-${index}`}>
                        <Select
                          value={pool.profile_id || undefined}
                          disabled={!settingsLoaded || savingWorkers}
                          onValueChange={(value) =>
                            setWorkerPools((current) =>
                              current.map((item, itemIndex) =>
                                itemIndex === index ? { ...item, profile_id: value } : item,
                              ),
                            )
                          }
                        >
                          <SelectTrigger className="min-w-0 flex-1">
                            <SelectValue placeholder={placeholder} />
                          </SelectTrigger>
                          <SelectContent>
                            {!selected && pool.profile_id ? (
                              <SelectItem value={pool.profile_id}>配置 #{pool.profile_id} 已删除/不可用</SelectItem>
                            ) : null}
                            {llmProfiles
                              .filter((profile) => profile.format !== "jev")
                              .map((profile) => (
                                <SelectItem key={profile.id} value={profile.id}>
                                  {profile.name} · {profile.model}
                                </SelectItem>
                              ))}
                          </SelectContent>
                        </Select>
                        <Input
                          type="number"
                          min={1}
                          className="w-24 font-mono text-sm"
                          value={pool.workers}
                          disabled={!settingsLoaded || savingWorkers}
                          onChange={(event) =>
                            setWorkerPools((current) =>
                              current.map((item, itemIndex) =>
                                itemIndex === index ? { ...item, workers: Number(event.target.value) } : item,
                              ),
                            )
                          }
                        />
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={!settingsLoaded || savingWorkers}
                          onClick={() =>
                            setWorkerPools((current) => current.filter((_, itemIndex) => itemIndex !== index))
                          }
                        >
                          删除
                        </Button>
                      </div>
                    );
                  })}
                  {workerPools.length > 0 ? (
                    <div className="text-muted-foreground text-sm">
                      当前合计：{workerPools.reduce((sum, pool) => sum + (Number(pool.workers) || 0), 0)} 个 Worker 会话
                    </div>
                  ) : null}
                </div>
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min={1}
                    className="w-32 font-mono text-sm"
                    placeholder="3"
                    value={workers}
                    disabled={!loaded || !settingsLoaded || savingWorkers || workerPools.length > 0}
                    onChange={(e) => setWorkers(e.target.value)}
                  />
                  {workerPools.length > 0 ? (
                    <span className="text-muted-foreground text-xs">已配置模型池时由各池数量决定并发。</span>
                  ) : null}
                  <Button onClick={saveWorkers} disabled={!loaded || !settingsLoaded || savingWorkers}>
                    保存
                  </Button>
                </div>
                {!settingsLoaded && loaded ? (
                  <p className="text-destructive text-xs">
                    设置加载失败，Worker 配置已锁定；刷新成功后才能编辑或保存。
                  </p>
                ) : null}
              </CardContent>
            </Card>

            <Card
              className={`${isSettingVisible("chat", "会话输入框 发送键位 Enter Shift") ? "" : "hidden"} mb-4 break-inside-avoid md:mb-6`}
            >
              <CardHeader>
                <CardTitle className="flex items-center gap-2 text-base">
                  <KeyboardIcon className="size-4" />
                  会话输入框发送键位
                </CardTitle>
                <CardDescription>
                  对话页与任务详情的主 Agent 会话输入框共用此设置，选择后立即生效、无需保存。
                  <br />
                  该偏好<b>只存在本浏览器</b>，不随账号同步，换浏览器或清理站点数据后需重新设置。
                </CardDescription>
              </CardHeader>
              <CardContent className="flex items-center justify-between gap-4">
                <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
                  发送方式
                </Label>
                <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
                  <SelectTrigger id="chat-send-mode" className="w-72">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {CHAT_SEND_MODE_OPTIONS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </CardContent>
            </Card>
          </div>
        </main>
      </div>
    </div>
  );
}
