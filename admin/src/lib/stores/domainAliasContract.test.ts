import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { JsonResponse, RequestBody } from "../api-contract";
import { useDomainStore } from "./domainStore";
import { useAliasStore } from "./aliasStore";

const domain: JsonResponse<"getDomain", 200>["data"] = {
  id: 7,
  name: "example.com",
  active: false,
  server_type: "traditional",
  default_quota_bytes: 1024,
  dkim_selector: "mail",
  dmarc_policy: "none",
  sending_policy: "",
  created_at: "2026-10-25T08:00:00Z",
  updated_at: "2026-10-25T08:00:00Z",
};
const alias: JsonResponse<"listAliases", 200>["data"][number] = {
  id: 11,
  domain_id: domain.id,
  source_address: "sales@example.com",
  destination_address: "owner@example.com",
  active: true,
  created_at: domain.created_at,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  useDomainStore.setState({
    domains: [],
    currentDomain: null,
    error: null,
    isLoading: false,
  });
  useAliasStore.setState({
    aliases: [],
    error: null,
    isLoading: false,
    selectedDomain: null,
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("admin domain and alias API wiring", () => {
  it("reads list and detail data from the generated domain envelopes", async () => {
    const list: JsonResponse<"listDomains", 200> = { data: [domain] };
    const detail: JsonResponse<"getDomain", 200> = { data: domain };
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(json(list))
        .mockResolvedValueOnce(json(detail)),
    );

    await useDomainStore.getState().fetchDomains();
    await useDomainStore.getState().fetchDomain("7");
    expect(useDomainStore.getState().domains).toEqual([domain]);
    expect(useDomainStore.getState().currentDomain).toEqual(domain);
  });

  it("creates an inactive domain with only the supported request fields", async () => {
    const body: RequestBody<"createDomain"> = {
      name: domain.name,
      server_type: "traditional",
    };
    const created: JsonResponse<"createDomain", 201> = { data: domain };
    const fetch = vi.fn().mockResolvedValue(json(created, 201));
    vi.stubGlobal("fetch", fetch);

    await useDomainStore.getState().createDomain(body);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/domains");
    expect(fetch.mock.calls[0][1].method).toBe("POST");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(body);
    expect(useDomainStore.getState().domains).toEqual([domain]);
  });

  it("uses PATCH and replaces the domain with the response data, not its envelope", async () => {
    useDomainStore.setState({ domains: [domain], currentDomain: domain });
    const body: RequestBody<"updateDomain"> = { active: true };
    const updated: JsonResponse<"updateDomain", 200> = {
      data: { ...domain, active: true },
    };
    const fetch = vi.fn().mockResolvedValue(json(updated));
    vi.stubGlobal("fetch", fetch);

    await useDomainStore.getState().updateDomain("7", body);
    expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/domains/7");
    expect(fetch.mock.calls[0][1].method).toBe("PATCH");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(body);
    expect(useDomainStore.getState().domains).toEqual([updated.data]);
    expect(useDomainStore.getState().currentDomain).toEqual(updated.data);
  });

  it("preserves the server refusal and existing data when activation is refused", async () => {
    useDomainStore.setState({ domains: [domain], currentDomain: domain });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        json(
          {
            error: {
              code: "certificate_required",
              message: "Add a valid certificate first.",
            },
          },
          409,
        ),
      ),
    );

    await expect(
      useDomainStore.getState().updateDomain("7", { active: true }),
    ).rejects.toThrow("Add a valid certificate first.");
    expect(useDomainStore.getState().currentDomain).toEqual(domain);
    expect(useDomainStore.getState().domains).toEqual([domain]);
    expect(useDomainStore.getState().isLoading).toBe(false);
    expect(useDomainStore.getState().error).toBe(
      "Add a valid certificate first.",
    );
  });

  it("keeps aliases from the single list envelope, including a domain filter", async () => {
    const list: JsonResponse<"listAliases", 200> = { data: [alias] };
    const fetch = vi.fn().mockResolvedValue(json(list));
    vi.stubGlobal("fetch", fetch);

    await useAliasStore.getState().fetchAliases("7");
    expect(fetch.mock.calls[0][0]).toBe(
      "https://api.test/admin/aliases?domain_id=7",
    );
    expect(useAliasStore.getState().aliases).toEqual([alias]);
  });

  it("sends generated alias request bodies and refreshes from the list envelope", async () => {
    const body: RequestBody<"createAlias"> = {
      source_address: alias.source_address,
      destination_address: alias.destination_address,
    };
    const created: JsonResponse<"createAlias", 201> = { data: alias };
    const list: JsonResponse<"listAliases", 200> = { data: [alias] };
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(json(created, 201))
      .mockResolvedValueOnce(json(list));
    vi.stubGlobal("fetch", fetch);

    await useAliasStore.getState().createAlias(body);
    expect(fetch.mock.calls[0][1].method).toBe("POST");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(body);
    expect(useAliasStore.getState().aliases).toEqual([alias]);
  });

  it("reports a readable fallback for a non-JSON error response", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response("upstream unavailable", { status: 502 }),
        ),
    );
    await expect(useAliasStore.getState().fetchAliases()).rejects.toThrow(
      "Failed to fetch aliases",
    );
    expect(useAliasStore.getState().isLoading).toBe(false);
  });
});
