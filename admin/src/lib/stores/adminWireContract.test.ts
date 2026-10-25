import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api-types";
import type { JsonResponse } from "../api-contract";
import { useMailboxStore } from "./mailboxStore";
import { useMTASTSStore } from "./mtastsStore";
import { usePipelineStore } from "./pipelineStore";
import { useCustomFilterStore } from "./customFilterStore";
import { useQueueStore } from "./queueStore";
import { useBanStore } from "./banStore";
import { useCertificateStore } from "./certificateStore";
import { useDkimStore } from "./dkimStore";
import { useTLSReportStore } from "./tlsReportStore";
import { useAuthStore } from "./authStore";

vi.hoisted(() => {
  const values = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  });
});

const stamp = "2026-10-25T10:00:00Z";
const pipeline: components["schemas"]["Pipeline"] = {
  id: 7,
  domain_id: 1,
  direction: "inbound",
  active: true,
  filters: [
    {
      name: "size_check",
      type: "action",
      enabled: true,
      config: { max_size_mb: 10 },
    },
  ],
  created_at: stamp,
  updated_at: stamp,
};
const custom: components["schemas"]["CustomFilter"] = {
  id: 3,
  domain_id: 1,
  name: "custom",
  description: "",
  direction: "inbound",
  filter_type: "action",
  enabled: true,
  config: { script: "return true" },
  created_at: stamp,
  updated_at: stamp,
};
const policy: JsonResponse<"getMtaStsPolicy", 200>["data"] = {
  id: 4,
  domain_id: 1,
  mode: "testing",
  mx_hosts: "mail.example.test",
  max_age: 86400,
  active: true,
  created_at: stamp,
  updated_at: stamp,
};
const queued: JsonResponse<"getQueueEntry", 200>["data"] = {
  id: 9,
  message_id: null,
  sender: "sender@example.test",
  recipient: "to@example.test",
  domain: "example.test",
  body_type: "7BIT",
  status: "delivering",
  attempts: 1,
  last_attempt: null,
  next_attempt: stamp,
  last_error: "",
  last_error_code: 0,
  failure_category: "",
  max_retries: 3,
  created_at: stamp,
  expires_at: stamp,
};
const session: JsonResponse<"login", 200> = {
  data: {
    user: { id: 1, email: "owner@example.test", display_name: "Owner" },
    expires_in: 900,
    capabilities: ["mail:read"],
    setup_required: [],
  },
};

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function reply(...responses: Response[]) {
  const fetch = vi.fn();
  for (const response of responses) fetch.mockResolvedValueOnce(response);
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

beforeEach(() => {
  useMailboxStore.setState({ mailboxes: [], error: null, isLoading: false });
  usePipelineStore.setState({
    pipelines: [],
    currentPipeline: null,
    error: null,
    isLoading: false,
  });
  useCustomFilterStore.setState({
    filters: [],
    currentFilter: null,
    error: null,
    isLoading: false,
  });
  useQueueStore.setState({
    entries: [],
    currentEntry: null,
    selectedIds: [],
    error: null,
    filter: "all",
  });
  useMTASTSStore.setState({
    policies: new Map(),
    currentPolicy: null,
    error: null,
  });
  useAuthStore.setState({
    user: null,
    accessToken: null,
    isAuthenticated: false,
    error: null,
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("admin callers use the generated wire contract", () => {
  it("reads ordinary list envelopes and top-level pagination", async () => {
    reply(
      json({
        data: [],
        pagination: { total: 3, has_more: true },
      } satisfies JsonResponse<"listBans", 200>),
      json({ data: [] } satisfies JsonResponse<"listCertificates", 200>),
      json({ data: [] } satisfies JsonResponse<"listDKIMKeys", 200>),
      json({
        data: [],
        pagination: { total: 6, has_more: true },
      } satisfies JsonResponse<"listTlsReports", 200>),
      json({ data: [] } satisfies JsonResponse<"listMailboxes", 200>),
    );
    await useBanStore.getState().fetchBans({});
    await useCertificateStore.getState().fetchCertificates();
    await useDkimStore.getState().fetchDkimKeys();
    await useTLSReportStore.getState().fetchReports({});
    await useMailboxStore.getState().fetchMailboxes();
    expect(useBanStore.getState().pagination).toEqual({
      total: 3,
      hasMore: true,
    });
    expect(useTLSReportStore.getState().pagination).toEqual({
      total: 6,
      hasMore: true,
    });
    expect(useCertificateStore.getState().certificates).toEqual([]);
    expect(useDkimStore.getState().entries).toEqual([]);
    expect(useMailboxStore.getState().mailboxes).toEqual([]);
  });

  it("keeps the MTA-STS response data rather than its envelope", async () => {
    reply(
      json({ data: policy } satisfies JsonResponse<"setMtaStsPolicy", 200>),
    );
    await useMTASTSStore
      .getState()
      .setPolicy(1, { mode: "testing", mx_hosts: policy.mx_hosts });
    expect(useMTASTSStore.getState().currentPolicy).toEqual(policy);
    expect(useMTASTSStore.getState().policies.get(1)).toEqual(policy);
  });

  it("sends an explicit zero quota and reports a rejected follow-up update", async () => {
    const fetch = reply(
      json({ data: { id: 12 } }, 201),
      json({ error: { code: "forbidden", message: "Quota denied" } }, 403),
    );
    await expect(
      useMailboxStore
        .getState()
        .createMailbox({ email: "new@example.test", quota_bytes: 0 }),
    ).rejects.toThrow("Quota denied");
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[1][1].method).toBe("PATCH");
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ quota_bytes: 0 });
    expect(useMailboxStore.getState().error).toBe("Quota denied");
  });

  it("creates and updates pipelines using the response envelope", async () => {
    const fetch = reply(
      json(
        { data: pipeline } satisfies JsonResponse<"createPipeline", 201>,
        201,
      ),
      json({ data: { ...pipeline, active: false } } satisfies JsonResponse<
        "updatePipeline",
        200
      >),
    );
    await usePipelineStore
      .getState()
      .createPipeline({
        domain_id: 1,
        direction: "inbound",
        filters: pipeline.filters,
      });
    const updated = await usePipelineStore
      .getState()
      .updatePipeline(7, { active: false });
    expect(updated.id).toBe(7);
    expect(updated.active).toBe(false);
    expect(usePipelineStore.getState().currentPipeline).toEqual(updated);
    expect(fetch.mock.calls[1][1].method).toBe("PATCH");
  });

  it("patches mailboxes without dropping empty names, zero quotas or false flags", async () => {
    const mailbox: JsonResponse<"updateMailbox", 200>["data"] = {
      id: 12,
      domain_id: 1,
      local_part: "owner",
      address: "owner@example.test",
      display_name: "",
      quota_bytes: 0,
      quota_used_bytes: 0,
      active: false,
      last_login_at: null,
      password_change_required: false,
      two_factor_required: false,
      created_at: stamp,
      updated_at: stamp,
    };
    const fetch = reply(
      json({ data: mailbox } satisfies JsonResponse<"updateMailbox", 200>),
    );
    const body = { display_name: "", quota_bytes: 0, active: false };
    expect(await useMailboxStore.getState().updateMailbox(12, body)).toEqual(
      mailbox,
    );
    expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/mailboxes/12");
    expect(fetch.mock.calls[0][1].method).toBe("PATCH");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(body);
  });

  it("finds pipeline details through supported, paginated collection requests", async () => {
    const fetch = reply(
      json({
        data: [{ ...pipeline, id: 6 }],
        pagination: { has_more: true },
      } satisfies JsonResponse<"listPipelines", 200>),
      json({
        data: [pipeline],
        pagination: { has_more: false },
      } satisfies JsonResponse<"listPipelines", 200>),
    );
    expect((await usePipelineStore.getState().fetchPipeline(7)).id).toBe(7);
    expect(fetch.mock.calls.map((call) => call[0])).toEqual([
      "https://api.test/admin/pipelines?limit=200&offset=0",
      "https://api.test/admin/pipelines?limit=200&offset=1",
    ]);
  });

  it("stops if a pipeline page cannot advance", async () => {
    const fetch = reply(
      json({ data: [], pagination: { has_more: true } } satisfies JsonResponse<
        "listPipelines",
        200
      >),
    );
    await expect(usePipelineStore.getState().fetchPipeline(7)).rejects.toThrow(
      "did not advance",
    );
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("creates and updates custom filters without storing their envelope", async () => {
    reply(
      json(
        { data: custom } satisfies JsonResponse<"createCustomFilter", 201>,
        201,
      ),
      json({ data: { ...custom, enabled: false } } satisfies JsonResponse<
        "updateCustomFilter",
        200
      >),
    );
    expect(
      (
        await useCustomFilterStore
          .getState()
          .createFilter({ name: "custom", config: custom.config })
      ).id,
    ).toBe(3);
    const updated = await useCustomFilterStore
      .getState()
      .updateFilter(3, { enabled: false });
    expect(updated.enabled).toBe(false);
    expect(useCustomFilterStore.getState().currentFilter).toEqual(updated);
  });

  it("preserves the script validation error and arbitrary script result with logs", async () => {
    const validation = {
      valid: false,
      error: "Syntax error",
      phase: "compile",
    } satisfies JsonResponse<"validateCustomFilter", 200>;
    const tested = {
      result: { action: "reject", detail: "Script choice" },
      logs: ["executed"],
    } satisfies JsonResponse<"testCustomFilter", 200>;
    reply(json(validation), json(tested));
    expect(
      await useCustomFilterStore.getState().validateScript("invalid"),
    ).toEqual(validation);
    expect(await useCustomFilterStore.getState().testFilter(3)).toEqual(tested);
  });

  it("reads queue items and keeps numeric selections", async () => {
    reply(
      json({
        data: { items: [queued], total: 1, limit: 100, offset: 0 },
      } satisfies JsonResponse<"listQueue", 200>),
    );
    await useQueueStore.getState().fetchQueue();
    useQueueStore.getState().toggleSelection(9);
    expect(useQueueStore.getState().entries).toEqual([queued]);
    expect(useQueueStore.getState().selectedIds).toEqual([9]);
  });

  it.each(["retryBulk", "deleteBulk"] as const)(
    "does not claim %s succeeded after a refusal",
    async (action) => {
      useQueueStore.setState({ selectedIds: [9, 10] });
      const fetch = reply(
        json({ error: { code: "forbidden", message: "Queue denied" } }, 403),
      );
      await expect(useQueueStore.getState()[action]([9, 10])).rejects.toThrow(
        "Queue denied",
      );
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(useQueueStore.getState().selectedIds).toEqual([9, 10]);
    },
  );

  it("restores a cookie session from the generated identity envelope", async () => {
    reply(json(session));
    await useAuthStore.getState().checkSession();
    expect(useAuthStore.getState().isAuthenticated).toBe(true);
    expect(useAuthStore.getState().user).toEqual({
      email: "owner@example.test",
      capabilities: ["mail:read"],
    });
    expect(useAuthStore.getState().accessToken).toBe("cookie-session");
  });
});
