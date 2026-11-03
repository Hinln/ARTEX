const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const ts = require("typescript");

const compiled = ts.transpileModule(fs.readFileSync(path.join(__dirname, "api.ts"), "utf8"), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;

function client(fetch) {
  const exports = {};
  vm.runInNewContext(compiled, {
    exports,
    process: { env: {} },
    URLSearchParams,
    window: {},
    localStorage: { getItem: () => "test-admin-token" },
    require: (id) => {
      if (id === "@/lib/mock/enabled") return { MOCK: false };
      if (id === "@/lib/mock/handler") return { mockHandle: () => assert.fail("unexpected demo request") };
      throw new Error(`Unexpected runtime import: ${id}`);
    },
    fetch,
  });
  return exports.api;
}

test("version queries carry the admin token and request fresh results after restart", async () => {
  const versions = ["v1.0.0", "v1.0.1"];
  const api = client(async (url, init) => {
    assert.equal(url, "/api/version");
    assert.equal(init.headers.Authorization, "Bearer test-admin-token");
    assert.equal(init.cache, "no-store");
    return { ok: true, status: 200, json: async () => ({ version: versions.shift() }) };
  });

  assert.equal((await api.version()).version, "v1.0.0");
  assert.equal((await api.version()).version, "v1.0.1");
});

test("version queries can resume after a temporary restart connection failure", async () => {
  let attempts = 0;
  const api = client(async () => {
    if (++attempts === 1) throw new TypeError("connection unavailable during restart");
    return { ok: true, status: 200, json: async () => ({ version: "v1.0.1" }) };
  });

  await assert.rejects(api.version(), /connection unavailable/);
  assert.equal((await api.version()).version, "v1.0.1");
});
