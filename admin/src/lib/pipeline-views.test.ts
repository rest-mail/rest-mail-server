import { describe, expect, it } from "vitest";
import type { components } from "./api-types";
import { customFilterView, pipelineView, traceView } from "./pipeline-views";

const stamp = "2026-10-25T10:00:00Z";
const pipeline: components["schemas"]["Pipeline"] = {
  id: 1,
  domain_id: 1,
  direction: "inbound",
  active: true,
  filters: [],
  created_at: stamp,
  updated_at: stamp,
};
const trace: components["schemas"]["MessageTrace"] = {
  id: 1,
  message_id: null,
  rfc_message_id: "",
  direction: "inbound",
  transport: "",
  mail_from: "",
  rcpt_to: "",
  client_ip: "",
  pipeline_id: 1,
  final_action: "continue",
  outcome: "delivered",
  reason_code: "",
  duration_ms: 0,
  stages: [],
  sampled: true,
  created_at: stamp,
};

describe("views of opaque JSON fields", () => {
  it("keeps a valid filter configuration and treats an unset column as empty", () => {
    const filters = [
      {
        name: "size_check",
        type: "action",
        enabled: true,
        config: { max_size_mb: 10 },
      },
    ];
    expect(pipelineView({ ...pipeline, filters }).filters).toEqual(filters);
    expect(pipelineView({ ...pipeline, filters: null }).filters).toEqual([]);
  });
  it("refuses an invalid filter configuration before the editor renders it", () => {
    expect(() =>
      pipelineView({
        ...pipeline,
        filters: [{ name: "broken", enabled: "yes" }],
      }),
    ).toThrow("invalid filter");
  });
  it("keeps the generated step shape and rejects malformed log values", () => {
    const step: components["schemas"]["StepResult"] = {
      filter_name: "size_check",
      filter_type: "action",
      action: "continue",
      duration_ms: 1,
      log: { filter: "size_check", result: "pass" },
    };
    expect(traceView({ ...trace, stages: [step] }).stages).toEqual([step]);
    expect(traceView({ ...trace, stages: null }).stages).toEqual([]);
    expect(() =>
      traceView(
        JSON.parse(
          JSON.stringify({ ...trace, stages: [{ ...step, log: "bad" }] }),
        ),
      ),
    ).toThrow("invalid stages");
  });
  it("preserves extra script configuration while rejecting a non-string script", () => {
    const custom: components["schemas"]["CustomFilter"] = {
      id: 1,
      domain_id: 1,
      name: "script",
      description: "",
      direction: "both",
      filter_type: "action",
      enabled: true,
      config: { script: "return true", timeout: 100 },
      created_at: stamp,
      updated_at: stamp,
    };
    expect(customFilterView(custom).config).toEqual(custom.config);
    expect(() =>
      customFilterView({ ...custom, config: { script: 42 } }),
    ).toThrow("invalid script");
  });
});
